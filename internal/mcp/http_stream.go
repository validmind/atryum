package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// This file merges two independent sources of upstream messages into one
// ordered sequence for a single tools/call, while enforcing timeouts and
// supporting reconnection. Read it in this order:
//
//  1. invokeHTTPStream, the entry point. It mints a per-call progress token
//     (rewriteProgressToken, in standalone_stream.go) so the shared
//     standalone stream can attribute progress to this specific call, then
//     calls doHTTPToolCallStream and retries once on a missing-session
//     response.
//  2. doHTTPToolCallStream sends the request. A plain JSON response is
//     handled right there and never reaches step 3; an SSE response is
//     handed to relaySSEToolCall.
//  3. relaySSEToolCall is the merge point: one select loop reading two
//     channels — postStreamPump's msgs (this call's own POST response,
//     including any resume after a disconnect) and progressCh (this call's
//     slice of the standalone stream, routed by standalone_stream.go's
//     routeStandaloneEvent). Every sink call happens on this one goroutine,
//     so the sink itself never needs its own locking.
//  4. postStreamPump owns the POST response's read loop on its own
//     goroutine, purely so relaySSEToolCall's select never blocks on it. It
//     is the only place that resumes a disconnected stream.
//
// The three time limits live in callTimeoutGuard (stream_timeout.go). The
// stdio equivalent of this whole file is stdio_stream.go, and is much
// simpler: one reader, no shared connection, no resume.

// streamCallOutcome is the result of one attempt to send a streaming
// tools/call request. missingSession mirrors doHTTPEnvelope's
// SessionExpired signal so invokeHTTPStream can apply the same
// reinitialize-and-retry-once policy invokeHTTP uses — but only when
// eventsRelayed is 0: once anything has reached the sink, the downstream
// has already seen stream bytes, so retrying would relay a second copy of
// everything. In that case the caller fails instead of retrying.
type streamCallOutcome struct {
	invoke         InvokeResult
	missingSession bool
	sessionID      string
	eventsRelayed  int
}

// doHTTPToolCallStream sends one tools/call request and either reads a
// plain JSON body (mapped exactly like the buffered path) or, for an SSE
// response, relays intermediate events to sink live and returns once the
// terminal response is read.
func (c *Client) doHTTPToolCallStream(ctx context.Context, upstream Upstream, body []byte, sink StreamSink, progressCh <-chan StreamEvent, opts StreamOptions) (streamCallOutcome, error) {
	guard := newCallTimeoutGuard(ctx)
	defer guard.stop()
	guard.armSetupTimeout(opts.HeaderTimeout)

	h, err := c.doHTTPEnvelopeHeaders(guard.ctx, upstream, body, DefaultMCPProtocolVersion, true)
	guard.disarmSetupTimeout()
	if err != nil {
		return streamCallOutcome{}, guard.timeoutErr(upstream.Name, "", err)
	}
	resp := h.resp

	// Armed as soon as headers are back, before any body is read — the
	// header timeout only ever bounded waiting for headers, so every
	// body-reading branch below (including the two early returns, not just
	// the SSE relay) needs its own bound. Without this, a slow/hanging body
	// on a 404-session-expired or plain-JSON response during a streaming
	// call would be unbounded: the per-call http.Client.Timeout that would
	// normally catch this is deliberately skipped in streaming mode (see
	// doHTTPEnvelopeRaw's streaming param).
	guard.armBodyTimeouts(opts.IdleTimeout, opts.MaxDuration)

	if h.sessionExpired {
		defer resp.Body.Close()
		// Drain (bounded) so the connection can be reused. A session-expired
		// body is a small error payload; cap it rather than trust the
		// upstream, since the guard's timers are the only other bound here
		// and both are disableable by configuration.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
		return streamCallOutcome{missingSession: true, sessionID: h.sessionID}, nil
	}

	if !strings.Contains(strings.ToLower(h.contentType), "text/event-stream") {
		defer resp.Body.Close()
		bodyBytes, err := readAllLimited(resp.Body, opts.maxMessageBytes())
		if err != nil {
			return streamCallOutcome{}, guard.timeoutErr(upstream.Name, "", err)
		}
		forward := ForwardResult{StatusCode: resp.StatusCode, Body: bodyBytes, ContentType: h.contentType, ProtocolVersion: h.protocolVersion, SessionID: h.sessionID}
		invoke, missingSession, err := toolCallResultFromForward(forward)
		if err != nil {
			return streamCallOutcome{}, err
		}
		return streamCallOutcome{invoke: invoke, missingSession: missingSession, sessionID: h.sessionID}, nil
	}

	// relaySSEToolCall owns resp.Body because it may replace this response
	// with one or more resumed GET streams before the terminal response.
	return c.relaySSEToolCall(resp, sink, progressCh, guard, upstream, h.sessionID, opts.maxMessageBytes())
}

func readAllLimited(r io.Reader, maxBytes int) ([]byte, error) {
	if maxBytes <= 0 {
		maxBytes = defaultStreamMaxMessageBytes
	}
	const maxInt64 = int64(^uint64(0) >> 1)
	limit := int64(maxBytes)
	if limit < maxInt64 {
		limit++
	}
	body, err := io.ReadAll(io.LimitReader(r, limit))
	if err != nil {
		return nil, err
	}
	if len(body) > maxBytes {
		return nil, ErrStreamMessageTooLarge
	}
	return body, nil
}

// postStreamMsg is one message pumped from a tools/call POST response by
// postStreamPump: either a data-bearing JSON-RPC payload (data != nil), or a
// terminal error ending the stream (err != nil).
type postStreamMsg struct {
	data []byte
	err  error
}

// postStreamPump owns the tools/call POST response's read loop — including
// SSE resumption — on its own goroutine, feeding relaySSEToolCall with only
// the data-bearing JSON-RPC payloads (or a final error) through msgs. This
// lets relaySSEToolCall select between this stream and a per-call
// standalone-stream channel (progressCh) without either blocking the
// other, so a call is only ever done reading (and only ever returns to its
// caller) once both are accounted for — see progressWaiter for why that
// matters.
type postStreamPump struct {
	msgs chan postStreamMsg

	mu       sync.Mutex
	current  *http.Response
	stopped  bool
	stopOnce sync.Once
	done     chan struct{}
}

func newPostStreamPump(c *Client, guard *callTimeoutGuard, upstream Upstream, resp *http.Response, maxMessageBytes int) *postStreamPump {
	p := &postStreamPump{msgs: make(chan postStreamMsg), current: resp, done: make(chan struct{})}
	go p.run(c, guard, upstream, maxMessageBytes)
	return p
}

// stop closes the currently-active response body, if any — causing a
// blocked Read to return promptly — and marks the pump stopped so it exits
// instead of trying to resume. Safe to call more than once; only the first
// call has any effect. Always safe to call even if the pump has already
// finished on its own.
func (p *postStreamPump) stop() {
	p.stopOnce.Do(func() {
		p.mu.Lock()
		p.stopped = true
		cur := p.current
		p.mu.Unlock()
		close(p.done)
		if cur != nil {
			_ = cur.Body.Close()
		}
	})
}

// setCurrent installs resp as the response the pump is currently reading
// from (after a resume). Returns false — and leaves resp to the caller to
// close — if stop was already called, so a resume racing a stop can't
// resurrect a pump that's supposed to be shutting down.
func (p *postStreamPump) setCurrent(resp *http.Response) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopped {
		return false
	}
	p.current = resp
	return true
}

// send delivers msg, or exits early if stop is called while blocked trying
// to (msgs is unbuffered: without this, a caller that stops reading msgs
// after its own terminal response — see relaySSEToolCall — would otherwise
// leave this goroutine permanently blocked on a send nobody will ever
// receive).
func (p *postStreamPump) send(msg postStreamMsg) {
	select {
	case p.msgs <- msg:
	case <-p.done:
	}
}

const (
	minSSEReconnectDelay = 200 * time.Millisecond
	maxSSEReconnectDelay = 30 * time.Second
)

func sseReconnectJitter(base time.Duration) time.Duration {
	return base / 5
}

func sseReconnectDelay(serverDelay time.Duration, attempt int) time.Duration {
	base := serverDelay
	if base <= 0 {
		base = minSSEReconnectDelay
		for range min(attempt, 8) {
			if base >= maxSSEReconnectDelay/2 {
				base = maxSSEReconnectDelay
				break
			}
			base *= 2
		}
	}
	base = max(base, minSSEReconnectDelay)
	base = min(base, maxSSEReconnectDelay)
	jitter := sseReconnectJitter(base)
	if jitter == 0 || base == maxSSEReconnectDelay {
		return base
	}
	return min(base+time.Duration(rand.Int64N(int64(jitter)+1)), maxSSEReconnectDelay)
}

func (p *postStreamPump) run(c *Client, guard *callTimeoutGuard, upstream Upstream, maxMessageBytes int) {
	defer close(p.msgs)
	// Per-line activity (not per-event) resets the idle timer, so an
	// upstream keepalive comment during a long-running tool counts as
	// liveness. This subsumes a per-event reset: every event is lines.
	reader := newSSEEventReaderWithLimit(p.current.Body, maxMessageBytes)
	reader.onActivity = guard.resetIdle
	lastEventID := ""
	retryDelay := time.Duration(0)
	reconnectAttempt := 0
	// resumedFrom holds, after a resume, the cursor id the Last-Event-ID
	// header carried. Replay semantics are exclusive of the cursor, but the
	// classic server off-by-one replays it inclusively — without this guard
	// the cursor event's data would be relayed to the agent a second time.
	// The guard window closes at the first event bearing any other id, so a
	// server legitimately reusing the id much later is unaffected.
	resumedFrom := ""
	for {
		evt, err := reader.NextEvent()
		if err != nil {
			p.mu.Lock()
			stopped := p.stopped
			p.mu.Unlock()
			if stopped {
				return
			}
			if timeoutErr := guard.timeoutErr(upstream.Name, "", nil); timeoutErr != nil {
				p.send(postStreamMsg{err: timeoutErr})
				return
			}
			if err != io.EOF {
				p.send(postStreamMsg{err: err})
				return
			}
			if lastEventID == "" {
				p.send(postStreamMsg{err: fmt.Errorf("upstream %q closed the stream without a JSON-RPC response or resumable event id", upstream.Name)})
				return
			}
			delay := sseReconnectDelay(retryDelay, reconnectAttempt)
			reconnectAttempt++
			if err := waitForSSEReconnect(guard.ctx, delay); err != nil {
				p.send(postStreamMsg{err: guard.timeoutErr(upstream.Name, "while waiting to resume", err)})
				return
			}
			resumed, err := c.resumeSSEStream(guard.ctx, upstream, lastEventID)
			if err != nil {
				p.send(postStreamMsg{err: guard.timeoutErr(upstream.Name, "while resuming", err)})
				return
			}
			if !p.setCurrent(resumed) {
				_ = resumed.Body.Close()
				return
			}
			reader = newSSEEventReaderWithLimit(resumed.Body, maxMessageBytes)
			reader.onActivity = guard.resetIdle
			resumedFrom = lastEventID
			continue
		}
		reconnectAttempt = 0
		if evt.HasRetry {
			retryDelay = evt.Retry
		}
		if evt.HasID {
			if resumedFrom != "" && evt.ID == resumedFrom {
				// Inclusive replay of the cursor event we already relayed
				// before the disconnect: keep the bookkeeping, skip the data.
				lastEventID = evt.ID
				continue
			}
			resumedFrom = ""
			lastEventID = evt.ID
		}
		if !evt.HasData {
			continue
		}
		p.send(postStreamMsg{data: evt.Data})
	}
}

// relaySSEToolCall selects between the POST response and this call's
// standalone progress channel, keeping all sink calls on one goroutine. The
// pump owns the response body, including resumed responses. StreamStarted is
// withheld only when a zero-event missing-session response will be retried.
func (c *Client) relaySSEToolCall(resp *http.Response, sink StreamSink, progressCh <-chan StreamEvent, guard *callTimeoutGuard, upstream Upstream, sessionID string, maxMessageBytes int) (streamCallOutcome, error) {
	expectedID := toolCallEnvelopeID
	statusCode := resp.StatusCode
	relayed := 0
	started := false
	ensureStarted := func() {
		if !started {
			started = true
			sink.StreamStarted()
		}
	}
	deliver := func(evt StreamEvent) error {
		guard.resetIdle()
		ensureStarted()
		relayed++
		return sink.Event(evt)
	}
	// fail builds the outcome every early-return-with-error site below
	// shares: relayed and sessionID as they stood at the moment of failure,
	// paired with whatever error caused it.
	fail := func(err error) (streamCallOutcome, error) {
		return streamCallOutcome{eventsRelayed: relayed, sessionID: sessionID}, err
	}

	pump := newPostStreamPump(c, guard, upstream, resp, maxMessageBytes)
	defer pump.stop()

	for {
		select {
		case evt, ok := <-progressCh:
			if !ok {
				// Never actually closed (its registration outlives this
				// call — see invokeHTTPStream's grace period), but nil this
				// out defensively so a closed channel can't busy-loop.
				progressCh = nil
				continue
			}
			if err := deliver(evt); err != nil {
				return fail(err)
			}
		case msg, ok := <-pump.msgs:
			if !ok {
				return fail(fmt.Errorf("upstream %q: stream ended unexpectedly", upstream.Name))
			}
			if msg.err != nil {
				return fail(msg.err)
			}
			payload := msg.data
			switch classifyRPCMessage(payload, expectedID) {
			case rpcMessageTerminalResponse:
				var rpcResp rpcResponse
				if err := json.Unmarshal(payload, &rpcResp); err != nil {
					return fail(err)
				}
				invoke, missingSession := toolCallResultFromRPCResponse(rpcResp, statusCode)
				if progressCh != nil {
					if err := drainTrailingProgress(progressCh, deliver, terminalSettleWindow); err != nil {
						return fail(err)
					}
				}
				if !(missingSession && relayed == 0) {
					ensureStarted()
				}
				return streamCallOutcome{invoke: invoke, missingSession: missingSession, eventsRelayed: relayed, sessionID: sessionID}, nil
			case rpcMessageServerRequest:
				if err := deliver(StreamEvent{Data: payload, ServerRequest: true}); err != nil {
					return fail(err)
				}
			case rpcMessageNotification:
				if err := deliver(StreamEvent{Data: payload}); err != nil {
					return fail(err)
				}
			default:
				// Unrecognized payload shape (e.g. a response to some other id).
				// Not ours to interpret; ignore and keep reading.
			}
		}
	}
}

// drainTrailingProgress gives a standalone-stream notification already in
// flight a brief, bounded chance to arrive before the terminal response is
// finalized (the caller passes terminalSettleWindow as window). Each arrival
// resets the window so a trailing burst is drained completely. It returns
// once the window elapses with no new arrival, progressCh closes, or deliver
// fails.
func drainTrailingProgress(progressCh <-chan StreamEvent, deliver func(StreamEvent) error, window time.Duration) error {
	settle := time.NewTimer(window)
	defer settle.Stop()
	for {
		select {
		case evt, ok := <-progressCh:
			if !ok {
				return nil
			}
			if err := deliver(evt); err != nil {
				return err
			}
			if !settle.Stop() {
				<-settle.C
			}
			settle.Reset(window)
		case <-settle.C:
			return nil
		}
	}
}

func waitForSSEReconnect(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// resumeSSEStream continues a server-closed Streamable HTTP response. The
// MCP transport specifies a GET to the same endpoint carrying Last-Event-ID;
// session, protocol, and authentication headers must match the original
// connection so the upstream can locate the pending request.
func (c *Client) resumeSSEStream(ctx context.Context, upstream Upstream, lastEventID string) (*http.Response, error) {
	endpoint := strings.TrimRight(upstream.BaseURL, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Last-Event-ID", lastEventID)
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
		return nil, fmt.Errorf("upstream %q resume failed with HTTP %d: %s", upstream.Name, resp.StatusCode, extractErrorDetail(body))
	}
	if !strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		defer resp.Body.Close()
		return nil, fmt.Errorf("upstream %q resume returned content type %q, want text/event-stream", upstream.Name, resp.Header.Get("Content-Type"))
	}
	if newSession := strings.TrimSpace(resp.Header.Get("Mcp-Session-Id")); newSession != "" {
		protocol := c.getSessionProtocol(upstream.Name)
		c.setSession(upstream.Name, newSession, protocol)
	}
	return resp, nil
}

// runSessionInitBounded runs fn (a session initialize/reinitialize step)
// under opts.HeaderTimeout. The session-init POSTs happen before the
// streaming call proper, so doHTTPToolCallStream's own header timeout never
// covers them; and in streaming mode the caller's ctx carries no deadline
// (the fixed request timeout is deliberately not applied — that's the whole
// point of the streaming timeout scheme). Without this bound, an upstream
// with no per-server timeout_seconds configured that hangs during
// initialize would block the call indefinitely.
func runSessionInitBounded(ctx context.Context, upstream Upstream, opts StreamOptions, fn func(context.Context) error) error {
	initCtx := ctx
	if opts.HeaderTimeout > 0 {
		var cancel context.CancelFunc
		initCtx, cancel = context.WithTimeout(ctx, opts.HeaderTimeout)
		defer cancel()
	}
	err := fn(initCtx)
	if err != nil && initCtx.Err() != nil && ctx.Err() == nil {
		// The bound we imposed fired (not the caller's own ctx): surface it
		// as the same typed timeout the rest of the streaming path uses.
		return fmt.Errorf("upstream %q: timed out initializing session before streaming call: %w", upstream.Name, ErrStreamTimeout)
	}
	return err
}

func (c *Client) invokeHTTPStream(ctx context.Context, upstream Upstream, tool string, input map[string]any, requestID *string, meta map[string]any, sink StreamSink, opts StreamOptions) (InvokeResult, error) {
	if err := runSessionInitBounded(ctx, upstream, opts, func(initCtx context.Context) error {
		return c.ensureHTTPSession(initCtx, upstream)
	}); err != nil {
		return InvokeResult{}, err
	}

	merged := mergeRequestMeta(meta, requestID)
	// A callSink restores the caller's progress token for messages arriving
	// from either the POST response or the standalone stream.
	effectiveSink := sink
	var progressCh chan StreamEvent
	var standalone *standaloneStream
	var standaloneDropped atomic.Int64
	var wireToken string
	if rewritten, token, original, ok := c.rewriteProgressToken(merged); ok {
		wireToken = token
		merged = rewritten
		effectiveSink = newCallSink(sink, wireToken, original)
		progressCh = make(chan StreamEvent, standaloneWaiterEventBuffer)
		standalone = c.acquireStandaloneStreamWithLimit(upstream, opts.maxMessageBytes())
		standalone.registerWaiter(wireToken, progressWaiter{events: progressCh, dropped: &standaloneDropped})
		defer func() {
			current := standalone
			current.unregisterWaiter(wireToken)
			c.releaseStandaloneStream(current)
			if statsSink, ok := effectiveSink.(StreamStatsSink); ok {
				statsSink.StreamStats(StreamStats{StandaloneEventsDropped: standaloneDropped.Load()})
			}
		}()
	}

	body, err := marshalToolCallEnvelopeWithMeta(tool, input, merged)
	if err != nil {
		return InvokeResult{}, err
	}

	outcome, err := c.doHTTPToolCallStream(ctx, upstream, body, effectiveSink, progressCh, opts)
	if err != nil {
		return InvokeResult{}, err
	}
	if outcome.missingSession {
		if outcome.eventsRelayed > 0 {
			return InvokeResult{}, fmt.Errorf("upstream %q reported a missing session after the stream had already relayed %d event(s): %w", upstream.Name, outcome.eventsRelayed, ErrStreamSessionRetryRefused)
		}
		c.debugf("upstream http tools.call stream missing session server=%s session=%q", upstream.Name, outcome.sessionID)
		if retryErr := runSessionInitBounded(ctx, upstream, opts, func(initCtx context.Context) error {
			return c.reinitializeRequiredHTTPSession(initCtx, upstream, outcome.sessionID)
		}); retryErr != nil {
			return InvokeResult{}, retryErr
		}
		if standalone != nil {
			standalone.unregisterWaiter(wireToken)
			c.releaseStandaloneStream(standalone)
			standalone = c.acquireStandaloneStreamWithLimit(upstream, opts.maxMessageBytes())
			standalone.registerWaiter(wireToken, progressWaiter{events: progressCh, dropped: &standaloneDropped})
		}
		outcome, err = c.doHTTPToolCallStream(ctx, upstream, body, effectiveSink, progressCh, opts)
		if err != nil {
			return InvokeResult{}, err
		}
		if outcome.missingSession {
			return InvokeResult{}, fmt.Errorf("upstream %q rejected session after reinitialize", upstream.Name)
		}
	}
	c.debugf("upstream http tools.call stream server=%s status=%d failed=%t events_relayed=%d", upstream.Name, outcome.invoke.StatusCode, outcome.invoke.Failed, outcome.eventsRelayed)
	return outcome.invoke, nil
}
