package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// invokeStreamTestServer builds the initialize/notifications.initialized
// scaffolding shared by the InvokeStream tests below, dispatching tools/call
// to callHandler.
func invokeStreamTestServer(t *testing.T, sessionID string, callHandler func(w http.ResponseWriter, r *http.Request, req Envelope)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			// The standalone SSE stream a progressToken-bearing call opens
			// alongside its tools/call POST. This fake upstream doesn't
			// support it — a legitimate, spec-allowed response — so tests
			// using a progressToken don't need every callHandler to be
			// GET-aware.
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		var req Envelope
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch req.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", sessionID)
			writeTestRPC(w, req.ID, map[string]any{"protocolVersion": r.Header.Get("MCP-Protocol-Version"), "capabilities": map[string]any{}}, nil)
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/call":
			callHandler(w, r, req)
		default:
			t.Fatalf("unexpected method %q", req.Method)
		}
	}))
}

func TestInvokeStreamRelaysEventsBeforeTerminalResponseExists(t *testing.T) {
	release := make(chan struct{})
	server := invokeStreamTestServer(t, "sid-incremental", func(w http.ResponseWriter, r *http.Request, req Envelope) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		writeTestSSEEventFlush(w, flusher, `{"jsonrpc":"2.0","method":"notifications/progress","params":{"progressToken":"tok-7","progress":1}}`)
		<-release // the terminal response cannot be written until the test has observed the event above
		writeTestSSEEventFlush(w, flusher, `{"jsonrpc":"2.0","id":"1","result":{"content":[{"type":"text","text":"done"}]}}`)
	})
	defer server.Close()

	client := NewHTTPClient()
	client.httpClient = server.Client()
	sink := &fakeStreamSink{onEvent: func(StreamEvent) error {
		close(release)
		return nil
	}}

	result, err := client.InvokeStream(context.Background(), Upstream{Name: "shortcut", Mode: UpstreamModeHTTP, BaseURL: server.URL}, "stories.get", map[string]any{}, nil, nil, sink, StreamOptions{})
	if err != nil {
		t.Fatalf("InvokeStream returned error: %v", err)
	}
	if !sink.started {
		t.Fatal("expected StreamStarted to fire")
	}
	if len(sink.events) != 1 {
		t.Fatalf("expected exactly one relayed event, got %d", len(sink.events))
	}
	if !strings.Contains(string(sink.events[0].Data), "notifications/progress") {
		t.Fatalf("expected progress notification relayed, got %s", sink.events[0].Data)
	}
	if !strings.Contains(string(result.Body), "done") {
		t.Fatalf("expected terminal result body, got %s", result.Body)
	}
}

func TestInvokeStreamResumesAfterUpstreamClosesSSEBeforeTerminalResponse(t *testing.T) {
	var resumeRequests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			resumeRequests++
			if got := r.Header.Get("Last-Event-ID"); got != "evt-1" {
				t.Fatalf("resume Last-Event-ID = %q, want evt-1", got)
			}
			if got := r.Header.Get("Mcp-Session-Id"); got != "sid-resume" {
				t.Fatalf("resume Mcp-Session-Id = %q, want sid-resume", got)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			flusher := w.(http.Flusher)
			_, _ = io.WriteString(w, "id: evt-2\ndata: {\"jsonrpc\":\"2.0\",\"id\":\"1\",\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"done after resume\"}]}}\n\n")
			flusher.Flush()
			return
		}

		var req Envelope
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch req.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "sid-resume")
			writeTestRPC(w, req.ID, map[string]any{"protocolVersion": r.Header.Get("MCP-Protocol-Version"), "capabilities": map[string]any{}}, nil)
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/call":
			w.Header().Set("Content-Type", "text/event-stream")
			flusher := w.(http.Flusher)
			_, _ = io.WriteString(w, "id: evt-1\nretry: 1\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{\"progress\":1}}\n\n")
			flusher.Flush()
			// End this HTTP response without the terminal JSON-RPC response.
			// A resumable MCP stream continues through a GET with Last-Event-ID.
		default:
			t.Fatalf("unexpected method %q", req.Method)
		}
	}))
	defer server.Close()

	client := NewHTTPClient()
	client.httpClient = server.Client()
	sink := &fakeStreamSink{}
	result, err := client.InvokeStream(
		context.Background(),
		Upstream{Name: "shortcut", Mode: UpstreamModeHTTP, BaseURL: server.URL},
		"stories.get", map[string]any{}, nil, nil, sink,
		StreamOptions{IdleTimeout: time.Second, MaxDuration: 5 * time.Second},
	)
	if err != nil {
		t.Fatalf("InvokeStream returned error: %v", err)
	}
	if resumeRequests != 1 {
		t.Fatalf("resume request count = %d, want 1", resumeRequests)
	}
	if len(sink.events) != 1 || !strings.Contains(string(sink.events[0].Data), "notifications/progress") {
		t.Fatalf("expected exactly the pre-disconnect progress event, got %#v", sink.events)
	}
	if !strings.Contains(string(result.Body), "done after resume") {
		t.Fatalf("expected terminal response from resumed stream, got %s", result.Body)
	}
}

func TestSSEReconnectDelayUsesBoundedExponentialBackoff(t *testing.T) {
	first := sseReconnectDelay(0, 0)
	second := sseReconnectDelay(0, 1)

	if first < minSSEReconnectDelay || first > minSSEReconnectDelay+sseReconnectJitter(minSSEReconnectDelay) {
		t.Fatalf("first reconnect delay = %s, want bounded minimum-delay jitter", first)
	}
	if second < 2*minSSEReconnectDelay || second > 2*minSSEReconnectDelay+sseReconnectJitter(2*minSSEReconnectDelay) {
		t.Fatalf("second reconnect delay = %s, want bounded exponential-delay jitter", second)
	}
	if got := sseReconnectDelay(time.Nanosecond, 0); got < minSSEReconnectDelay {
		t.Fatalf("server retry below minimum produced %s, want at least %s", got, minSSEReconnectDelay)
	}
	if got := sseReconnectDelay(24*time.Hour, 0); got > maxSSEReconnectDelay {
		t.Fatalf("server retry above maximum produced %s, want at most %s", got, maxSSEReconnectDelay)
	}
	for range 100 {
		if got := sseReconnectDelay(maxSSEReconnectDelay-time.Second, 0); got > maxSSEReconnectDelay {
			t.Fatalf("jitter pushed reconnect delay above maximum: %s", got)
		}
	}
}

func TestInvokeStreamRejectsOversizedPlainJSONResponse(t *testing.T) {
	server := invokeStreamTestServer(t, "sid-large-json", func(w http.ResponseWriter, r *http.Request, req Envelope) {
		writeTestRPC(w, req.ID, map[string]any{"content": strings.Repeat("x", 256)}, nil)
	})
	defer server.Close()

	client := NewHTTPClient()
	client.httpClient = server.Client()
	_, err := client.InvokeStream(
		context.Background(),
		Upstream{Name: "large-json", Mode: UpstreamModeHTTP, BaseURL: server.URL},
		"demo",
		map[string]any{},
		nil,
		nil,
		&fakeStreamSink{},
		StreamOptions{MaxMessageBytes: 128},
	)
	if !errors.Is(err, ErrStreamMessageTooLarge) {
		t.Fatalf("InvokeStream error = %v, want ErrStreamMessageTooLarge", err)
	}
}

// TestInvokeStreamResumeSkipsInclusivelyReplayedCursorEvent is a regression
// test for reconnect duplicate delivery: Last-Event-ID replay is exclusive
// of the cursor, but the classic server off-by-one replays the cursor event
// itself again. That event's data already reached the agent before the
// disconnect — relaying it twice would deliver a duplicate notification.
func TestInvokeStreamResumeSkipsInclusivelyReplayedCursorEvent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			if got := r.Header.Get("Last-Event-ID"); got != "evt-1" {
				t.Fatalf("resume Last-Event-ID = %q, want evt-1", got)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			flusher := w.(http.Flusher)
			// Buggy inclusive replay: evt-1 again, then genuinely new events.
			_, _ = io.WriteString(w, "id: evt-1\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{\"progress\":1}}\n\n")
			_, _ = io.WriteString(w, "id: evt-2\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{\"progress\":2}}\n\n")
			_, _ = io.WriteString(w, "id: evt-3\ndata: {\"jsonrpc\":\"2.0\",\"id\":\"1\",\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"done after resume\"}]}}\n\n")
			flusher.Flush()
			return
		}

		var req Envelope
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch req.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "sid-resume-dupe")
			writeTestRPC(w, req.ID, map[string]any{"protocolVersion": r.Header.Get("MCP-Protocol-Version"), "capabilities": map[string]any{}}, nil)
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/call":
			w.Header().Set("Content-Type", "text/event-stream")
			flusher := w.(http.Flusher)
			_, _ = io.WriteString(w, "id: evt-1\nretry: 1\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{\"progress\":1}}\n\n")
			flusher.Flush()
			// Close without the terminal response → client resumes via GET.
		default:
			t.Fatalf("unexpected method %q", req.Method)
		}
	}))
	defer server.Close()

	client := NewHTTPClient()
	client.httpClient = server.Client()
	sink := &fakeStreamSink{}
	result, err := client.InvokeStream(
		context.Background(),
		Upstream{Name: "shortcut", Mode: UpstreamModeHTTP, BaseURL: server.URL},
		"stories.get", map[string]any{}, nil, nil, sink,
		StreamOptions{IdleTimeout: time.Second, MaxDuration: 5 * time.Second},
	)
	if err != nil {
		t.Fatalf("InvokeStream returned error: %v", err)
	}
	if len(sink.events) != 2 {
		t.Fatalf("expected exactly 2 relayed notifications (evt-1 once + evt-2, cursor replay deduplicated), got %d: %#v", len(sink.events), sink.events)
	}
	if !strings.Contains(string(sink.events[0].Data), `"progress":1`) || !strings.Contains(string(sink.events[1].Data), `"progress":2`) {
		t.Fatalf("expected progress 1 then progress 2, got %#v", sink.events)
	}
	if !strings.Contains(string(result.Body), "done after resume") {
		t.Fatalf("expected terminal response from resumed stream, got %s", result.Body)
	}
}

func TestInvokeStreamRelaysNotificationAndServerRequest(t *testing.T) {
	server := invokeStreamTestServer(t, "sid-mixed", func(w http.ResponseWriter, r *http.Request, req Envelope) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		writeTestSSEEventFlush(w, flusher, `{"jsonrpc":"2.0","method":"notifications/message","params":{"level":"info","data":"halfway"}}`)
		writeTestSSEEventFlush(w, flusher, `{"jsonrpc":"2.0","id":"srv-1","method":"sampling/createMessage","params":{}}`)
		writeTestSSEEventFlush(w, flusher, `{"jsonrpc":"2.0","id":"1","result":{"content":[{"type":"text","text":"done"}]}}`)
	})
	defer server.Close()

	client := NewHTTPClient()
	client.httpClient = server.Client()
	sink := &fakeStreamSink{}

	result, err := client.InvokeStream(context.Background(), Upstream{Name: "shortcut", Mode: UpstreamModeHTTP, BaseURL: server.URL}, "stories.get", map[string]any{}, nil, nil, sink, StreamOptions{})
	if err != nil {
		t.Fatalf("InvokeStream returned error: %v", err)
	}
	if len(sink.events) != 2 {
		t.Fatalf("expected 2 relayed events (notification + server request), got %d: %#v", len(sink.events), sink.events)
	}
	if sink.events[0].ServerRequest {
		t.Fatalf("expected first event to be a notification, got %#v", sink.events[0])
	}
	if !sink.events[1].ServerRequest {
		t.Fatalf("expected second event to be flagged as a server request, got %#v", sink.events[1])
	}
	if !strings.Contains(string(result.Body), "done") {
		t.Fatalf("expected terminal result body, got %s", result.Body)
	}
}

func TestInvokeStreamJSONResponseNeverTouchesSink(t *testing.T) {
	server := invokeStreamTestServer(t, "sid-json", func(w http.ResponseWriter, r *http.Request, req Envelope) {
		writeTestRPC(w, req.ID, map[string]any{"content": []any{map[string]any{"type": "text", "text": "ok"}}}, nil)
	})
	defer server.Close()

	client := NewHTTPClient()
	client.httpClient = server.Client()
	sink := &fakeStreamSink{}

	result, err := client.InvokeStream(context.Background(), Upstream{Name: "shortcut", Mode: UpstreamModeHTTP, BaseURL: server.URL}, "stories.get", map[string]any{}, nil, nil, sink, StreamOptions{})
	if err != nil {
		t.Fatalf("InvokeStream returned error: %v", err)
	}
	if sink.started || len(sink.events) != 0 {
		t.Fatalf("expected sink to never be touched for a JSON response, got started=%t events=%#v", sink.started, sink.events)
	}
	if !strings.Contains(string(result.Body), `"text":"ok"`) {
		t.Fatalf("expected plain JSON result body, got %s", result.Body)
	}
}

func TestInvokeStreamSSETerminalOnlyResponseStartsSink(t *testing.T) {
	server := invokeStreamTestServer(t, "sid-terminal-only-sse", func(w http.ResponseWriter, r *http.Request, req Envelope) {
		w.Header().Set("Content-Type", "text/event-stream")
		writeTestSSEEventFlush(
			w,
			w.(http.Flusher),
			`{"jsonrpc":"2.0","id":"1","result":{"content":[{"type":"text","text":"done"}]}}`,
		)
	})
	defer server.Close()

	client := NewHTTPClient()
	client.httpClient = server.Client()
	sink := &fakeStreamSink{}

	result, err := client.InvokeStream(
		context.Background(),
		Upstream{Name: "terminal-only-sse", Mode: UpstreamModeHTTP, BaseURL: server.URL},
		"demo",
		map[string]any{},
		nil,
		nil,
		sink,
		StreamOptions{},
	)
	if err != nil {
		t.Fatalf("InvokeStream returned error: %v", err)
	}
	if sink.startedCount != 1 {
		t.Fatalf("expected terminal-only HTTP SSE to start the sink exactly once, got %d", sink.startedCount)
	}
	if len(sink.events) != 0 {
		t.Fatalf("expected no intermediate events, got %d", len(sink.events))
	}
	if !strings.Contains(string(result.Body), "done") {
		t.Fatalf("expected terminal result body, got %s", result.Body)
	}
}

// TestInvokeStreamHangingJSONBodyBoundedByIdleTimeout is a regression test:
// StreamOptions' idle/max-duration bounds must apply to every body-reading
// branch of doHTTPToolCallStream, not just the SSE relay. The per-call
// http.Client.Timeout that would normally catch a hanging JSON body is
// deliberately skipped in streaming mode (see doHTTPEnvelopeRaw's streaming
// param), so without this a slow-to-complete JSON response during a
// streaming call attempt would hang forever.
func TestInvokeStreamHangingJSONBodyBoundedByIdleTimeout(t *testing.T) {
	blockUntilTestDone := make(chan struct{})
	server := invokeStreamTestServer(t, "sid-slow-json", func(w http.ResponseWriter, r *http.Request, req Envelope) {
		w.Header().Set("Content-Type", "application/json")
		flusher := w.(http.Flusher)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":"1","result":`)) // deliberately incomplete
		flusher.Flush()
		<-blockUntilTestDone // never completes the body
	})
	t.Cleanup(func() {
		close(blockUntilTestDone)
		server.Close()
	})

	client := NewHTTPClient()
	client.httpClient = server.Client()
	sink := &fakeStreamSink{}

	start := time.Now()
	_, err := client.InvokeStream(context.Background(), Upstream{Name: "shortcut", Mode: UpstreamModeHTTP, BaseURL: server.URL}, "stories.get", map[string]any{}, nil, nil, sink, StreamOptions{IdleTimeout: 50 * time.Millisecond})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected a timeout error for the hanging JSON body read")
	}
	if !errors.Is(err, ErrStreamTimeout) {
		t.Fatalf("expected errors.Is(err, ErrStreamTimeout), got %v", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("took too long to abort: %s", elapsed)
	}
}

// TestInvokeStreamHangingSessionInitBoundedByHeaderTimeout is a regression
// test: the session-initialize POST happens before doHTTPToolCallStream's
// own header timeout is armed, and in streaming mode neither the per-call
// http.Client timeout (deliberately skipped) nor the caller's ctx (no
// deadline) bounds it. An upstream with no per-server timeout configured
// that hangs on initialize would block the call forever without
// runSessionInitBounded.
func TestInvokeStreamHangingSessionInitBoundedByHeaderTimeout(t *testing.T) {
	blockUntilTestDone := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req Envelope
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.Method != "initialize" {
			t.Fatalf("unexpected method %q before initialize completed", req.Method)
		}
		<-blockUntilTestDone // hang the initialize response forever
	}))
	t.Cleanup(func() {
		close(blockUntilTestDone)
		server.Close()
	})

	client := NewHTTPClient()
	client.httpClient = server.Client()
	sink := &fakeStreamSink{}

	start := time.Now()
	// upstream.Timeout deliberately zero: no per-server bound to fall back on.
	_, err := client.InvokeStream(context.Background(), Upstream{Name: "shortcut", Mode: UpstreamModeHTTP, BaseURL: server.URL}, "stories.get", map[string]any{}, nil, nil, sink, StreamOptions{HeaderTimeout: 50 * time.Millisecond})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected a session-init timeout error")
	}
	if !errors.Is(err, ErrStreamTimeout) {
		t.Fatalf("expected errors.Is(err, ErrStreamTimeout) for the hung session init, got %v", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("session-init timeout took too long to abort: %s", elapsed)
	}
	if sink.started || len(sink.events) != 0 {
		t.Fatalf("expected the sink never to be touched during a failed session init, got started=%t events=%d", sink.started, len(sink.events))
	}
}

func TestInvokeStreamMapsTerminalRPCErrorAfterRelayedEvents(t *testing.T) {
	server := invokeStreamTestServer(t, "sid-error", func(w http.ResponseWriter, r *http.Request, req Envelope) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		writeTestSSEEventFlush(w, flusher, `{"jsonrpc":"2.0","method":"notifications/progress","params":{"progress":1}}`)
		writeTestSSEEventFlush(w, flusher, `{"jsonrpc":"2.0","id":"1","error":{"code":-32000,"message":"tool exploded"}}`)
	})
	defer server.Close()

	client := NewHTTPClient()
	client.httpClient = server.Client()
	sink := &fakeStreamSink{}

	result, err := client.InvokeStream(context.Background(), Upstream{Name: "shortcut", Mode: UpstreamModeHTTP, BaseURL: server.URL}, "stories.get", map[string]any{}, nil, nil, sink, StreamOptions{})
	if err != nil {
		t.Fatalf("InvokeStream returned error: %v", err)
	}
	if !result.Failed {
		t.Fatalf("expected Failed result, got %#v", result)
	}
	if !strings.Contains(string(result.Body), "tool exploded") {
		t.Fatalf("expected error body, got %s", result.Body)
	}
	if len(sink.events) != 1 {
		t.Fatalf("expected the progress notification to have been relayed before the terminal error, got %d", len(sink.events))
	}
}

func TestInvokeStreamRetriesOnceWhenMissingSessionBeforeAnyEventRelayed(t *testing.T) {
	var sessions []string
	var toolsCallCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req Envelope
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch req.Method {
		case "initialize":
			sessionID := "sid-1"
			if len(sessions) > 0 {
				sessionID = "sid-2"
			}
			sessions = append(sessions, sessionID)
			w.Header().Set("Mcp-Session-Id", sessionID)
			writeTestRPC(w, req.ID, map[string]any{"protocolVersion": r.Header.Get("MCP-Protocol-Version"), "capabilities": map[string]any{}}, nil)
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/call":
			toolsCallCount++
			w.Header().Set("Content-Type", "text/event-stream")
			flusher := w.(http.Flusher)
			if toolsCallCount == 1 {
				writeTestSSEEventFlush(w, flusher, `{"jsonrpc":"2.0","id":"1","error":{"code":-32000,"message":"No session ID provided for non-initialization request"}}`)
				return
			}
			if got := r.Header.Get("Mcp-Session-Id"); got != "sid-2" {
				t.Fatalf("retry tools/call used session %q, want sid-2", got)
			}
			writeTestSSEEventFlush(w, flusher, `{"jsonrpc":"2.0","id":"1","result":{"content":[{"type":"text","text":"done"}]}}`)
		default:
			t.Fatalf("unexpected method %q", req.Method)
		}
	}))
	defer server.Close()

	client := NewHTTPClient()
	client.httpClient = server.Client()
	sink := &fakeStreamSink{}

	result, err := client.InvokeStream(context.Background(), Upstream{Name: "shortcut", Mode: UpstreamModeHTTP, BaseURL: server.URL}, "stories.get", map[string]any{}, nil, nil, sink, StreamOptions{})
	if err != nil {
		t.Fatalf("InvokeStream returned error: %v", err)
	}
	if toolsCallCount != 2 {
		t.Fatalf("tools/call count = %d, want 2", toolsCallCount)
	}
	if len(sessions) != 2 {
		t.Fatalf("initialize sessions = %#v, want two sessions", sessions)
	}
	if !strings.Contains(string(result.Body), "done") {
		t.Fatalf("expected terminal result body after retry, got %s", result.Body)
	}
	if sink.startedCount != 1 {
		t.Fatalf("expected StreamStarted to fire exactly once (for the successful retry, not the discarded missing-session attempt), got %d", sink.startedCount)
	}
}

func TestInvokeStreamRefusesRetryAfterEventsAlreadyRelayed(t *testing.T) {
	var initializeCount int
	var toolsCallCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req Envelope
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch req.Method {
		case "initialize":
			initializeCount++
			w.Header().Set("Mcp-Session-Id", "sid-1")
			writeTestRPC(w, req.ID, map[string]any{"protocolVersion": r.Header.Get("MCP-Protocol-Version"), "capabilities": map[string]any{}}, nil)
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/call":
			toolsCallCount++
			w.Header().Set("Content-Type", "text/event-stream")
			flusher := w.(http.Flusher)
			writeTestSSEEventFlush(w, flusher, `{"jsonrpc":"2.0","method":"notifications/progress","params":{"progress":1}}`)
			writeTestSSEEventFlush(w, flusher, `{"jsonrpc":"2.0","id":"1","error":{"code":-32000,"message":"No session ID provided for non-initialization request"}}`)
		default:
			t.Fatalf("unexpected method %q", req.Method)
		}
	}))
	defer server.Close()

	client := NewHTTPClient()
	client.httpClient = server.Client()
	sink := &fakeStreamSink{}

	_, err := client.InvokeStream(context.Background(), Upstream{Name: "shortcut", Mode: UpstreamModeHTTP, BaseURL: server.URL}, "stories.get", map[string]any{}, nil, nil, sink, StreamOptions{})
	if err == nil {
		t.Fatal("expected an error refusing to retry mid-stream")
	}
	if !strings.Contains(err.Error(), "already relayed") {
		t.Fatalf("expected a mid-stream retry refusal error, got %v", err)
	}
	if !errors.Is(err, ErrStreamSessionRetryRefused) {
		t.Fatalf("expected errors.Is(err, ErrStreamSessionRetryRefused) to hold, got %v", err)
	}
	if toolsCallCount != 1 {
		t.Fatalf("tools/call count = %d, want 1 (no retry once events were relayed)", toolsCallCount)
	}
	if initializeCount != 1 {
		t.Fatalf("initialize count = %d, want 1 (no reinitialize attempt)", initializeCount)
	}
	if len(sink.events) != 1 {
		t.Fatalf("expected the one notification before the terminal error to have been relayed, got %d", len(sink.events))
	}
}

func TestInvokeStreamIdleTimeoutAbortsRead(t *testing.T) {
	blockUntilTestDone := make(chan struct{})
	server := invokeStreamTestServer(t, "sid-idle", func(w http.ResponseWriter, r *http.Request, req Envelope) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		writeTestSSEEventFlush(w, flusher, `{"jsonrpc":"2.0","method":"notifications/progress","params":{"progress":1}}`)
		<-blockUntilTestDone // never send the terminal event
	})
	t.Cleanup(func() {
		close(blockUntilTestDone)
		server.Close()
	})

	client := NewHTTPClient()
	client.httpClient = server.Client()
	sink := &fakeStreamSink{}

	start := time.Now()
	_, err := client.InvokeStream(context.Background(), Upstream{Name: "shortcut", Mode: UpstreamModeHTTP, BaseURL: server.URL}, "stories.get", map[string]any{}, nil, nil, sink, StreamOptions{IdleTimeout: 50 * time.Millisecond})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected an idle timeout error")
	}
	if !strings.Contains(err.Error(), "idle timeout") {
		t.Fatalf("expected an idle timeout error, got %v", err)
	}
	if !errors.Is(err, ErrStreamTimeout) {
		t.Fatalf("expected errors.Is(err, ErrStreamTimeout) to hold so callers can distinguish it from other failures, got %v", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("idle timeout took too long to abort: %s", elapsed)
	}
	if !sink.started {
		t.Fatal("expected StreamStarted before the timeout")
	}
	if len(sink.events) != 1 {
		t.Fatalf("expected exactly one relayed event before the timeout, got %d", len(sink.events))
	}
}
