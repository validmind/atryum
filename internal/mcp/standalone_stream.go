package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// progressWaiter routes a shared-stream message to the call goroutine that
// owns the sink. This prevents shared-reader writes from racing the call's
// terminal response.
type progressWaiter struct {
	events chan StreamEvent
}

// A full waiter buffer drops that call's event rather than blocking the one
// reader shared by every active call.
const standaloneWaiterEventBuffer = 32

// standaloneStream manages the shared SSE GET used for server-initiated
// messages. It is needed for SDKs that send progress without a related
// request ID, placing it on this connection instead of the tools/call POST.
// One ref-counted stream serves all calls sharing an upstream session. It is
// not resumable; a later acquire opens a fresh connection.
//
// standaloneWaiterGracePeriod lets an in-flight progress message arrive after
// the POST terminal response without keeping its unique-token waiter forever.
const standaloneWaiterGracePeriod = 2 * time.Second

// terminalSettleWindow briefly drains progress that races the terminal across
// the independent POST and standalone connections. Each arrival resets it so
// a trailing burst is drained completely.
const terminalSettleWindow = 25 * time.Millisecond

type standaloneStream struct {
	mu       sync.Mutex
	refCount int
	cancel   context.CancelFunc
	done     chan struct{}
	waiters  map[string]progressWaiter
	// unsupported is set once opening the connection fails outright (e.g. a
	// 404/405, which some upstreams legitimately return for this endpoint
	// per spec). It stops every later acquire from re-attempting a doomed
	// connection on every single streaming call; it resets naturally the
	// next time refCount drops to zero and this entry is evicted.
	unsupported bool
}

// acquireStandaloneStream returns the shared standaloneStream for upstream,
// creating it and starting its reader goroutine if this is the first
// waiter. Callers must pair this with exactly one releaseStandaloneStream.
func (c *Client) acquireStandaloneStream(upstream Upstream) *standaloneStream {
	c.standaloneMu.Lock()
	s := c.standaloneStreams[upstream.Name]
	if s == nil {
		s = &standaloneStream{waiters: make(map[string]progressWaiter)}
		c.standaloneStreams[upstream.Name] = s
	}
	c.standaloneMu.Unlock()

	s.mu.Lock()
	s.refCount++
	start := s.refCount == 1 && !s.unsupported
	if start {
		streamCtx, cancel := context.WithCancel(context.Background())
		s.cancel = cancel
		s.done = make(chan struct{})
		go c.runStandaloneStream(streamCtx, upstream, s)
	}
	s.mu.Unlock()
	return s
}

// releaseStandaloneStream drops one reference acquired via
// acquireStandaloneStream. Once the last reference is gone, it cancels the
// reader goroutine, waits for it to fully exit, and evicts the entry so a
// future acquire opens a fresh connection (picking up, e.g., a session that
// was reinitialized in the meantime).
func (c *Client) releaseStandaloneStream(upstream Upstream, s *standaloneStream) {
	s.mu.Lock()
	s.refCount--
	last := s.refCount <= 0
	var cancel context.CancelFunc
	var done chan struct{}
	if last {
		cancel = s.cancel
		done = s.done
		s.cancel = nil
		s.done = nil
	}
	s.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
	if last {
		c.standaloneMu.Lock()
		if c.standaloneStreams[upstream.Name] == s {
			delete(c.standaloneStreams, upstream.Name)
		}
		c.standaloneMu.Unlock()
	}
}

func (s *standaloneStream) registerWaiter(token string, w progressWaiter) {
	s.mu.Lock()
	s.waiters[token] = w
	s.mu.Unlock()
}

func (s *standaloneStream) unregisterWaiter(token string) {
	s.mu.Lock()
	delete(s.waiters, token)
	s.mu.Unlock()
}

// openStandaloneGET opens the standalone SSE stream: a bare GET carrying the
// session's headers, no Last-Event-ID (see standaloneStream doc comment).
// Mirrors resumeSSEStream's header handling.
func (c *Client) openStandaloneGET(ctx context.Context, upstream Upstream) (*http.Response, error) {
	endpoint := strings.TrimRight(upstream.BaseURL, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	if protocol := c.getSessionProtocol(upstream.Name); protocol != "" {
		req.Header.Set("MCP-Protocol-Version", protocol)
	}
	if sessionID := c.getSession(upstream.Name); sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	applyAuthHeaders(req, upstream)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= http.StatusBadRequest {
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		return nil, fmt.Errorf("upstream %q standalone stream returned HTTP %d: %s", upstream.Name, resp.StatusCode, extractErrorDetail(body))
	}
	if !strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		defer resp.Body.Close()
		return nil, fmt.Errorf("upstream %q standalone stream returned content type %q, want text/event-stream", upstream.Name, resp.Header.Get("Content-Type"))
	}
	return resp, nil
}

func (c *Client) runStandaloneStream(ctx context.Context, upstream Upstream, s *standaloneStream) {
	defer close(s.done)
	resp, err := c.openStandaloneGET(ctx, upstream)
	if err != nil {
		c.debugf("standalone stream unavailable server=%s err=%v", upstream.Name, err)
		s.mu.Lock()
		s.unsupported = true
		s.mu.Unlock()
		return
	}
	defer resp.Body.Close()
	reader := newSSEEventReader(resp.Body)
	for {
		evt, err := reader.NextEvent()
		if err != nil {
			return
		}
		if !evt.HasData {
			continue
		}
		c.routeStandaloneEvent(s, evt.Data)
	}
}

// routeStandaloneEvent attributes one standalone-stream message to whichever
// registered call it belongs to. Progress notifications carry the token
// Atryum minted for that call (see rewriteProgressToken) in
// params.progressToken, giving an unambiguous match. Anything else (e.g. a
// logging notification) carries no per-call correlator at all; it is
// delivered only when exactly one call is currently waiting on this stream,
// since there is no way to attribute it correctly when several calls are
// in flight concurrently — and silently guessing wrong would leak one
// caller's message to another.
func (c *Client) routeStandaloneEvent(s *standaloneStream, payload []byte) {
	var message map[string]json.RawMessage
	if err := json.Unmarshal(payload, &message); err != nil {
		return
	}
	if _, hasMethod := message["method"]; !hasMethod {
		return
	}
	wireToken, hasToken := extractProgressToken(message)

	s.mu.Lock()
	var waiter progressWaiter
	var ok bool
	if hasToken {
		waiter, ok = s.waiters[wireToken]
	} else if len(s.waiters) == 1 {
		for _, w := range s.waiters {
			waiter, ok = w, true
		}
	}
	s.mu.Unlock()
	if !ok {
		return
	}

	// Handed off to the matching call's own goroutine via its channel — see
	// progressWaiter for why this indirection matters. callSink.Event (on
	// the receiving end) restores the caller's original progressToken
	// itself (matching on its own wireToken), so the raw payload is sent
	// through unmodified here.
	select {
	case waiter.events <- StreamEvent{Data: payload}:
	default:
		// Buffer full, or the receiving call already stopped draining it —
		// drop rather than block this shared reader goroutine, which also
		// serves every other call currently sharing this connection.
	}
}

// extractProgressToken reads params.progressToken from an already-decoded
// JSON-RPC message, normalizing it to a bare string for map lookup
// regardless of whether the upstream echoed it back as a JSON string or a
// number.
func extractProgressToken(message map[string]json.RawMessage) (string, bool) {
	paramsRaw, ok := message["params"]
	if !ok {
		return "", false
	}
	var params struct {
		ProgressToken json.RawMessage `json:"progressToken"`
	}
	if err := json.Unmarshal(paramsRaw, &params); err != nil || len(params.ProgressToken) == 0 {
		return "", false
	}
	return strings.Trim(string(params.ProgressToken), `"`), true
}

// rewriteProgressTokenInPayload replaces params.progressToken in an
// already-wire-formatted JSON-RPC message with originalToken, restoring the
// value the caller actually supplied before relaying the message onward.
func rewriteProgressTokenInPayload(payload []byte, originalToken any) ([]byte, error) {
	var generic map[string]any
	if err := json.Unmarshal(payload, &generic); err != nil {
		return nil, err
	}
	params, ok := generic["params"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("message has no params object")
	}
	params["progressToken"] = originalToken
	generic["params"] = params
	return json.Marshal(generic)
}

// rewriteProgressToken replaces meta's progressToken, if any, with a value
// unique to this specific call, returning the rewritten meta, that wire
// token, and the caller's original token. Atryum multiplexes every
// downstream caller of a given upstream onto one shared session, so two
// unrelated concurrent calls could independently pick the same
// caller-supplied progressToken; rewriting to a per-call value here is what
// lets routeStandaloneEvent attribute a notification to the right call
// instead of risking a cross-call delivery.
func (c *Client) rewriteProgressToken(meta map[string]any) (rewritten map[string]any, wireToken string, original any, ok bool) {
	if meta == nil {
		return meta, "", nil, false
	}
	original, ok = meta["progressToken"]
	if !ok {
		return meta, "", nil, false
	}
	wireToken = fmt.Sprintf("atryum-pt-%d", c.nextID.Add(1))
	rewritten = make(map[string]any, len(meta))
	for k, v := range meta {
		rewritten[k] = v
	}
	rewritten["progressToken"] = wireToken
	return rewritten, wireToken, original, true
}

// callSink restores the caller's progressToken after Atryum replaces it with
// a per-call wire token. It wraps both POST and standalone-stream delivery so
// the internal token cannot leak through either path.
type callSink struct {
	inner         StreamSink
	wireToken     string
	originalToken any
}

func newCallSink(inner StreamSink, wireToken string, originalToken any) *callSink {
	return &callSink{inner: inner, wireToken: wireToken, originalToken: originalToken}
}

func (s *callSink) StreamStarted() {
	s.inner.StreamStarted()
}

func (s *callSink) Event(evt StreamEvent) error {
	var message map[string]json.RawMessage
	if err := json.Unmarshal(evt.Data, &message); err == nil {
		if token, ok := extractProgressToken(message); ok && token == s.wireToken {
			if rewritten, err := rewriteProgressTokenInPayload(evt.Data, s.originalToken); err == nil {
				evt.Data = rewritten
			}
		}
	}
	return s.inner.Event(evt)
}
