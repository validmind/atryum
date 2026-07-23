package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// syncFakeStreamSink is fakeStreamSink's mutex-protected counterpart. It's
// needed wherever a test can have both relaySSEToolCall's own read loop and
// routeStandaloneEvent deliver to the same sink concurrently — the plain
// fakeStreamSink above assumes single-goroutine delivery and would race.
type syncFakeStreamSink struct {
	mu      sync.Mutex
	started bool
	events  []StreamEvent
}

func newSyncFakeStreamSink() *syncFakeStreamSink {
	return &syncFakeStreamSink{}
}

func (s *syncFakeStreamSink) StreamStarted() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.started = true
}

func (s *syncFakeStreamSink) Event(evt StreamEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, evt)
	return nil
}

func (s *syncFakeStreamSink) wasStarted() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.started
}

func (s *syncFakeStreamSink) snapshotEvents() []StreamEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]StreamEvent, len(s.events))
	copy(out, s.events)
	return out
}

func TestRewriteProgressToken(t *testing.T) {
	client := NewHTTPClient()
	if _, _, _, ok := client.rewriteProgressToken(nil); ok {
		t.Fatal("expected no rewrite when meta is nil")
	}
	if _, _, _, ok := client.rewriteProgressToken(map[string]any{"atryumRequestId": "x"}); ok {
		t.Fatal("expected no rewrite when meta has no progressToken")
	}

	rewritten, wireToken, original, ok := client.rewriteProgressToken(map[string]any{"progressToken": float64(7), "atryumRequestId": "req-1"})
	if !ok {
		t.Fatal("expected a rewrite when progressToken is present")
	}
	if original != float64(7) {
		t.Fatalf("expected original token 7, got %#v", original)
	}
	if rewritten["progressToken"] != wireToken {
		t.Fatalf("expected rewritten meta to carry the wire token, got %#v", rewritten["progressToken"])
	}
	if rewritten["atryumRequestId"] != "req-1" {
		t.Fatal("expected other meta keys preserved")
	}

	_, wireToken2, _, _ := client.rewriteProgressToken(map[string]any{"progressToken": "other"})
	if wireToken2 == wireToken {
		t.Fatal("expected distinct wire tokens across calls, so concurrent callers can't collide")
	}
}

func TestExtractAndRewriteProgressTokenInPayload(t *testing.T) {
	payload := []byte(`{"jsonrpc":"2.0","method":"notifications/progress","params":{"progressToken":"atryum-pt-3","progress":1,"total":3}}`)
	var msg map[string]json.RawMessage
	if err := json.Unmarshal(payload, &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	token, ok := extractProgressToken(msg)
	if !ok || token != "atryum-pt-3" {
		t.Fatalf("extractProgressToken = (%q, %v), want (atryum-pt-3, true)", token, ok)
	}

	rewritten, err := rewriteProgressTokenInPayload(payload, float64(42))
	if err != nil {
		t.Fatalf("rewriteProgressTokenInPayload: %v", err)
	}
	if !strings.Contains(string(rewritten), `"progressToken":42`) {
		t.Fatalf("expected original numeric token restored, got %s", rewritten)
	}
	if !strings.Contains(string(rewritten), `"progress":1`) {
		t.Fatalf("expected other params fields preserved, got %s", rewritten)
	}

	if _, ok := extractProgressToken(map[string]json.RawMessage{"method": json.RawMessage(`"notifications/message"`)}); ok {
		t.Fatal("expected no token when params is absent")
	}
}

// TestRouteStandaloneEventAttributesTokenlessNotificationOnlyWhenUnambiguous
// covers routeStandaloneEvent's fallback for messages with no progressToken
// (e.g. a plain logging notification): deliverable only when exactly one
// call is waiting on the stream, since guessing with several concurrent
// waiters would leak one caller's message to another.
func TestRouteStandaloneEventAttributesTokenlessNotificationOnlyWhenUnambiguous(t *testing.T) {
	client := NewHTTPClient()
	payload := []byte(`{"jsonrpc":"2.0","method":"notifications/message","params":{"level":"info","data":"hello"}}`)

	chA := make(chan StreamEvent, 1)
	lone := &standaloneStream{waiters: map[string]progressWaiter{"tok-a": {events: chA}}}
	client.routeStandaloneEvent(lone, payload)
	select {
	case <-chA:
	default:
		t.Fatal("expected the lone waiter to receive a tokenless notification")
	}

	chB, chC := make(chan StreamEvent, 1), make(chan StreamEvent, 1)
	ambiguous := &standaloneStream{waiters: map[string]progressWaiter{
		"tok-b": {events: chB},
		"tok-c": {events: chC},
	}}
	client.routeStandaloneEvent(ambiguous, payload)
	select {
	case <-chB:
		t.Fatal("expected a tokenless notification to be dropped, not guessed, with multiple concurrent waiters")
	case <-chC:
		t.Fatal("expected a tokenless notification to be dropped, not guessed, with multiple concurrent waiters")
	default:
	}
}

// TestInvokeStreamStandaloneStreamRelaysProgressNotification is the
// regression test for the real end-to-end gap this feature fixes: the
// reference MCP Python SDK's Context.report_progress sends progress
// notifications on the standalone GET stream, never on the tools/call POST
// response body, because it doesn't attribute the notification to the
// request that triggered it. Without a standalone-stream reader, Atryum
// would relay zero progress notifications for such a server even though the
// terminal response arrives correctly.
func TestInvokeStreamStandaloneStreamRelaysProgressNotification(t *testing.T) {
	tokenCh := make(chan string, 1)
	notifSent := make(chan struct{})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			if got := r.Header.Get("Last-Event-ID"); got != "" {
				t.Fatalf("standalone GET unexpectedly carried Last-Event-ID=%q (that's the resume path)", got)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			flusher := w.(http.Flusher)
			token := <-tokenCh
			writeTestSSEEventFlush(w, flusher, fmt.Sprintf(`{"jsonrpc":"2.0","method":"notifications/progress","params":{"progressToken":%q,"progress":1}}`, token))
			close(notifSent)
			<-r.Context().Done()
			return
		}

		var req Envelope
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch req.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "sid-standalone")
			writeTestRPC(w, req.ID, map[string]any{"protocolVersion": r.Header.Get("MCP-Protocol-Version"), "capabilities": map[string]any{}}, nil)
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/call":
			var params struct {
				Meta struct {
					ProgressToken string `json:"progressToken"`
				} `json:"_meta"`
			}
			_ = json.Unmarshal(req.Params, &params)
			tokenCh <- params.Meta.ProgressToken
			<-notifSent
			w.Header().Set("Content-Type", "text/event-stream")
			flusher := w.(http.Flusher)
			writeTestSSEEventFlush(w, flusher, `{"jsonrpc":"2.0","id":"1","result":{"content":[{"type":"text","text":"done"}]}}`)
		default:
			t.Fatalf("unexpected method %q", req.Method)
		}
	}))
	defer server.Close()

	client := NewHTTPClient()
	client.httpClient = server.Client()
	sink := newSyncFakeStreamSink()

	result, err := client.InvokeStream(context.Background(), Upstream{Name: "real", Mode: UpstreamModeHTTP, BaseURL: server.URL}, "slow_streaming_task", map[string]any{}, nil, map[string]any{"progressToken": "caller-token"}, sink, StreamOptions{})
	if err != nil {
		t.Fatalf("InvokeStream returned error: %v", err)
	}
	if !strings.Contains(string(result.Body), "done") {
		t.Fatalf("expected terminal result body, got %s", result.Body)
	}
	if !sink.wasStarted() {
		t.Fatal("expected StreamStarted to fire for a notification delivered only via the standalone stream")
	}
	events := sink.snapshotEvents()
	if len(events) != 1 {
		t.Fatalf("expected exactly one relayed event, got %d: %#v", len(events), events)
	}
	if !strings.Contains(string(events[0].Data), `"progressToken":"caller-token"`) {
		t.Fatalf("expected the caller's original progressToken restored, got %s", events[0].Data)
	}
}

func TestInvokeStreamStandaloneProgressResetsIdleTimeout(t *testing.T) {
	tokenCh := make(chan string, 1)
	progressComplete := make(chan struct{})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "text/event-stream")
			flusher := w.(http.Flusher)
			token := <-tokenCh
			for progress := 1; progress <= 4; progress++ {
				time.Sleep(60 * time.Millisecond)
				writeTestSSEEventFlush(w, flusher, fmt.Sprintf(
					`{"jsonrpc":"2.0","method":"notifications/progress","params":{"progressToken":%q,"progress":%d}}`,
					token,
					progress,
				))
			}
			close(progressComplete)
			<-r.Context().Done()
			return
		}

		var req Envelope
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch req.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "sid-standalone-idle")
			writeTestRPC(w, req.ID, map[string]any{"protocolVersion": r.Header.Get("MCP-Protocol-Version"), "capabilities": map[string]any{}}, nil)
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/call":
			var params struct {
				Meta struct {
					ProgressToken string `json:"progressToken"`
				} `json:"_meta"`
			}
			_ = json.Unmarshal(req.Params, &params)
			tokenCh <- params.Meta.ProgressToken

			w.Header().Set("Content-Type", "text/event-stream")
			flusher := w.(http.Flusher)
			_, _ = w.Write([]byte(": stream ready\n\n"))
			flusher.Flush()
			<-progressComplete
			writeTestSSEEventFlush(w, flusher, `{"jsonrpc":"2.0","id":"1","result":{"content":[{"type":"text","text":"done"}]}}`)
		default:
			t.Fatalf("unexpected method %q", req.Method)
		}
	}))
	defer server.Close()

	client := NewHTTPClient()
	client.httpClient = server.Client()
	sink := newSyncFakeStreamSink()

	result, err := client.InvokeStream(
		context.Background(),
		Upstream{Name: "standalone-idle", Mode: UpstreamModeHTTP, BaseURL: server.URL},
		"slow_streaming_task",
		map[string]any{},
		nil,
		map[string]any{"progressToken": "caller-token"},
		sink,
		StreamOptions{IdleTimeout: 150 * time.Millisecond},
	)
	if err != nil {
		t.Fatalf("InvokeStream returned error while standalone progress remained active: %v", err)
	}
	if !strings.Contains(string(result.Body), "done") {
		t.Fatalf("expected terminal result body, got %s", result.Body)
	}
	if events := sink.snapshotEvents(); len(events) != 4 {
		t.Fatalf("expected four relayed progress events, got %d", len(events))
	}
}

// TestInvokeStreamRestoresCallerProgressTokenOnPOSTResponseStreamToo is a
// regression test: some upstreams echo a call's progress notifications on
// the tools/call POST response itself, not the standalone stream — that's
// actually the more spec-typical case for a request-scoped notification.
// The caller's original progressToken must be restored there too, not only
// on notifications that happen to arrive via the standalone stream.
func TestInvokeStreamRestoresCallerProgressTokenOnPOSTResponseStreamToo(t *testing.T) {
	server := invokeStreamTestServer(t, "sid-post-echo", func(w http.ResponseWriter, r *http.Request, req Envelope) {
		var params struct {
			Meta struct {
				ProgressToken string `json:"progressToken"`
			} `json:"_meta"`
		}
		_ = json.Unmarshal(req.Params, &params)

		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		writeTestSSEEventFlush(w, flusher, fmt.Sprintf(`{"jsonrpc":"2.0","method":"notifications/progress","params":{"progressToken":%q,"progress":1}}`, params.Meta.ProgressToken))
		writeTestSSEEventFlush(w, flusher, `{"jsonrpc":"2.0","id":"1","result":{"content":[{"type":"text","text":"done"}]}}`)
	})
	defer server.Close()

	client := NewHTTPClient()
	client.httpClient = server.Client()
	sink := newSyncFakeStreamSink()

	_, err := client.InvokeStream(context.Background(), Upstream{Name: "shortcut", Mode: UpstreamModeHTTP, BaseURL: server.URL}, "stories.get", map[string]any{}, nil, map[string]any{"progressToken": "caller-token"}, sink, StreamOptions{})
	if err != nil {
		t.Fatalf("InvokeStream returned error: %v", err)
	}
	events := sink.snapshotEvents()
	if len(events) != 1 {
		t.Fatalf("expected exactly one relayed event, got %d: %#v", len(events), events)
	}
	if !strings.Contains(string(events[0].Data), `"progressToken":"caller-token"`) {
		t.Fatalf("expected the caller's original progressToken restored on the POST-response stream, got %s", events[0].Data)
	}
	if strings.Contains(string(events[0].Data), "atryum-pt-") {
		t.Fatalf("expected Atryum's internal wire token never to leak to the agent, got %s", events[0].Data)
	}
}

// TestInvokeStreamStandaloneStreamAvoidsProgressTokenCollisionAcrossCalls
// proves two concurrent callers who happen to pick the same progressToken
// don't cross-deliver: Atryum multiplexes every caller of an upstream onto
// one shared session, so the standalone stream is shared too, and the only
// thing preventing a collision is the per-call wire-token rewrite.
func TestInvokeStreamStandaloneStreamAvoidsProgressTokenCollisionAcrossCalls(t *testing.T) {
	var mu sync.Mutex
	tokenFor := map[string]string{}
	postCount := 0
	getConnected := make(chan struct{})
	gotBothTokens := make(chan struct{})
	notifsDone := make(chan struct{})
	var closeGetConnectedOnce, closeGotBothTokensOnce sync.Once

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			closeGetConnectedOnce.Do(func() { close(getConnected) })
			w.Header().Set("Content-Type", "text/event-stream")
			flusher := w.(http.Flusher)
			<-gotBothTokens
			mu.Lock()
			tokA, tokB := tokenFor["tool-a"], tokenFor["tool-b"]
			mu.Unlock()
			writeTestSSEEventFlush(w, flusher, fmt.Sprintf(`{"jsonrpc":"2.0","method":"notifications/progress","params":{"progressToken":%q,"progress":1}}`, tokA))
			writeTestSSEEventFlush(w, flusher, fmt.Sprintf(`{"jsonrpc":"2.0","method":"notifications/progress","params":{"progressToken":%q,"progress":2}}`, tokB))
			close(notifsDone)
			<-r.Context().Done()
			return
		}

		var req Envelope
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch req.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "sid-collision")
			writeTestRPC(w, req.ID, map[string]any{"protocolVersion": r.Header.Get("MCP-Protocol-Version"), "capabilities": map[string]any{}}, nil)
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/call":
			var params struct {
				Name string `json:"name"`
				Meta struct {
					ProgressToken string `json:"progressToken"`
				} `json:"_meta"`
			}
			_ = json.Unmarshal(req.Params, &params)
			<-getConnected
			mu.Lock()
			tokenFor[params.Name] = params.Meta.ProgressToken
			postCount++
			ready := postCount == 2
			mu.Unlock()
			if ready {
				closeGotBothTokensOnce.Do(func() { close(gotBothTokens) })
			}
			<-notifsDone
			w.Header().Set("Content-Type", "text/event-stream")
			flusher := w.(http.Flusher)
			writeTestSSEEventFlush(w, flusher, fmt.Sprintf(`{"jsonrpc":"2.0","id":"1","result":{"content":[{"type":"text","text":"done-%s"}]}}`, params.Name))
		default:
			t.Fatalf("unexpected method %q", req.Method)
		}
	}))
	defer server.Close()

	client := NewHTTPClient()
	client.httpClient = server.Client()
	upstream := Upstream{Name: "real", Mode: UpstreamModeHTTP, BaseURL: server.URL}

	sinkA := newSyncFakeStreamSink()
	sinkB := newSyncFakeStreamSink()

	var wg sync.WaitGroup
	var errA, errB error
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, errA = client.InvokeStream(context.Background(), upstream, "tool-a", map[string]any{}, nil, map[string]any{"progressToken": float64(1)}, sinkA, StreamOptions{})
	}()
	go func() {
		defer wg.Done()
		_, errB = client.InvokeStream(context.Background(), upstream, "tool-b", map[string]any{}, nil, map[string]any{"progressToken": float64(1)}, sinkB, StreamOptions{})
	}()
	wg.Wait()

	if errA != nil {
		t.Fatalf("call A error: %v", errA)
	}
	if errB != nil {
		t.Fatalf("call B error: %v", errB)
	}

	eventsA, eventsB := sinkA.snapshotEvents(), sinkB.snapshotEvents()
	if len(eventsA) != 1 {
		t.Fatalf("call A: expected exactly 1 relayed event, got %d: %#v", len(eventsA), eventsA)
	}
	if len(eventsB) != 1 {
		t.Fatalf("call B: expected exactly 1 relayed event, got %d: %#v", len(eventsB), eventsB)
	}
	if !strings.Contains(string(eventsA[0].Data), `"progress":1`) || !strings.Contains(string(eventsA[0].Data), `"progressToken":1`) {
		t.Fatalf("call A got the wrong notification or token, want its own progress=1/token=1, got %s", eventsA[0].Data)
	}
	if !strings.Contains(string(eventsB[0].Data), `"progress":2`) || !strings.Contains(string(eventsB[0].Data), `"progressToken":1`) {
		t.Fatalf("call B got the wrong notification or token, want its own progress=2/token=1, got %s", eventsB[0].Data)
	}
}

func TestStandaloneStreamRefcountsSharedConnection(t *testing.T) {
	var connections int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("unexpected method %q", r.Method)
		}
		atomic.AddInt32(&connections, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()

	client := NewHTTPClient()
	client.httpClient = server.Client()
	upstream := Upstream{Name: "real", Mode: UpstreamModeHTTP, BaseURL: server.URL}

	s1 := client.acquireStandaloneStream(upstream)
	s2 := client.acquireStandaloneStream(upstream)
	if s1 != s2 {
		t.Fatal("expected the second acquire to reuse the same standaloneStream while the first is still active")
	}

	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(&connections) < 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := atomic.LoadInt32(&connections); got != 1 {
		t.Fatalf("expected exactly 1 standalone connection while both waiters are active, got %d", got)
	}

	client.releaseStandaloneStream(upstream, s1)
	if got := atomic.LoadInt32(&connections); got != 1 {
		t.Fatalf("releasing one of two references should not close the connection yet, got %d", got)
	}
	client.releaseStandaloneStream(upstream, s2)

	s3 := client.acquireStandaloneStream(upstream)
	if s3 == s1 {
		t.Fatal("expected a fresh standaloneStream after the previous one was fully released")
	}
	deadline = time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(&connections) < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := atomic.LoadInt32(&connections); got != 2 {
		t.Fatalf("expected a new connection after full release + reacquire, got %d", got)
	}
	client.releaseStandaloneStream(upstream, s3)
}

// TestInvokeStreamStandaloneStreamUnsupportedDoesNotFailCall covers an
// upstream that returns a plain error (e.g. 404/405, which some servers
// legitimately return for this endpoint per spec) for the standalone GET:
// the tools/call itself must still succeed normally via its own POST
// response stream.
func TestInvokeStreamStandaloneStreamUnsupportedDoesNotFailCall(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		var req Envelope
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch req.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "sid-unsupported")
			writeTestRPC(w, req.ID, map[string]any{"protocolVersion": r.Header.Get("MCP-Protocol-Version"), "capabilities": map[string]any{}}, nil)
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/call":
			w.Header().Set("Content-Type", "text/event-stream")
			flusher := w.(http.Flusher)
			writeTestSSEEventFlush(w, flusher, `{"jsonrpc":"2.0","id":"1","result":{"content":[{"type":"text","text":"done"}]}}`)
		default:
			t.Fatalf("unexpected method %q", req.Method)
		}
	}))
	defer server.Close()

	client := NewHTTPClient()
	client.httpClient = server.Client()
	sink := newSyncFakeStreamSink()

	result, err := client.InvokeStream(context.Background(), Upstream{Name: "real", Mode: UpstreamModeHTTP, BaseURL: server.URL}, "tool", map[string]any{}, nil, map[string]any{"progressToken": "tok"}, sink, StreamOptions{})
	if err != nil {
		t.Fatalf("InvokeStream returned error: %v", err)
	}
	if !strings.Contains(string(result.Body), "done") {
		t.Fatalf("expected terminal result body, got %s", result.Body)
	}
	if len(sink.snapshotEvents()) != 0 {
		t.Fatalf("expected no relayed events when the standalone stream is unsupported, got %#v", sink.snapshotEvents())
	}
}

// TestInvokeStreamStandaloneStreamWrongContentTypeDoesNotFailCall covers the
// other shape of "unsupported": a 200 response that isn't actually SSE
// (some servers, on a bare GET, just serve something unrelated rather than
// the expected 404/405). openStandaloneGET must reject it the same way it
// rejects an outright error status, without affecting the call itself.
func TestInvokeStreamStandaloneStreamWrongContentTypeDoesNotFailCall(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<html>not an SSE stream</html>"))
			return
		}
		var req Envelope
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch req.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "sid-wrong-content-type")
			writeTestRPC(w, req.ID, map[string]any{"protocolVersion": r.Header.Get("MCP-Protocol-Version"), "capabilities": map[string]any{}}, nil)
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/call":
			w.Header().Set("Content-Type", "text/event-stream")
			flusher := w.(http.Flusher)
			writeTestSSEEventFlush(w, flusher, `{"jsonrpc":"2.0","id":"1","result":{"content":[{"type":"text","text":"done"}]}}`)
		default:
			t.Fatalf("unexpected method %q", req.Method)
		}
	}))
	defer server.Close()

	client := NewHTTPClient()
	client.httpClient = server.Client()
	sink := newSyncFakeStreamSink()

	result, err := client.InvokeStream(context.Background(), Upstream{Name: "real", Mode: UpstreamModeHTTP, BaseURL: server.URL}, "tool", map[string]any{}, nil, map[string]any{"progressToken": "tok"}, sink, StreamOptions{})
	if err != nil {
		t.Fatalf("InvokeStream returned error: %v", err)
	}
	if !strings.Contains(string(result.Body), "done") {
		t.Fatalf("expected terminal result body, got %s", result.Body)
	}
	if len(sink.snapshotEvents()) != 0 {
		t.Fatalf("expected no relayed events when the standalone stream has the wrong content type, got %#v", sink.snapshotEvents())
	}
}
