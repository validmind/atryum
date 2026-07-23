package api

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/validmind/atryum/internal/config"
	"github.com/validmind/atryum/internal/invocation"
	"github.com/validmind/atryum/internal/invocation/policy"
	"github.com/validmind/atryum/internal/mcp"
	"github.com/validmind/atryum/internal/store"
)

func TestMCPToolsCallRelaysStreamedEventsAndRewritesTerminalID(t *testing.T) {
	now := time.Now().UTC()
	svc := &stubService{invoke: invocation.InvocationResponse{
		InvocationID: "inv_123", ServerName: "demo", ToolName: "demo_tool",
		Status: invocation.StatusSucceeded, SubmittedAt: now, CompletedAt: &now,
		Result: json.RawMessage(`{"content":[{"type":"text","text":"done"}]}`),
	}}
	svc.invokeStreamingFn = func(ctx context.Context, req invocation.CreateInvocationRequest, sink mcp.StreamSink) (invocation.InvocationResponse, error) {
		sink.StreamStarted()
		if err := sink.Event(mcp.StreamEvent{Data: []byte(`{"jsonrpc":"2.0","method":"notifications/progress","params":{"progress":1}}`)}); err != nil {
			t.Fatalf("sink.Event: %v", err)
		}
		return svc.invoke, nil
	}
	h := NewHandler(svc, stubServerService{}, nil, nil, nil, nil, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/mcp/demo", strings.NewReader(`{"jsonrpc":"2.0","id":99,"method":"tools/call","params":{"name":"demo_tool","arguments":{}}}`))
	req.Header.Set("Accept", "application/json, text/event-stream")
	w := httptest.NewRecorder()

	h.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("expected text/event-stream, got %q", ct)
	}
	body := w.Body.String()
	if !strings.Contains(body, "notifications/progress") {
		t.Fatalf("expected the relayed progress notification in the body, got %q", body)
	}
	// The terminal frame must carry the agent's own id (99), not the fixed
	// upstream envelope id ("1") mcp.Client always sends on the wire.
	if !strings.Contains(body, `"id":99`) {
		t.Fatalf("expected terminal frame rewritten to the agent's id 99, got %q", body)
	}
	if !strings.Contains(body, `"text":"done"`) {
		t.Fatalf("expected terminal result body, got %q", body)
	}
}

// TestMCPToolsCallRelaysMultiLineEventDataAsMultipleDataLines is a
// regression test: writeSSEEvent must emit one "data:" line per line of a
// multi-line payload rather than embedding raw newlines inside a single
// "data:" line, which breaks SSE framing (a continuation line with no
// field prefix is dropped by any compliant parser). Upstream SSE data can
// genuinely be multi-line — see mcp.TestListToolsDecodesMultilineSSEData
// for the receiving side of this same scenario.
func TestMCPToolsCallRelaysMultiLineEventDataAsMultipleDataLines(t *testing.T) {
	now := time.Now().UTC()
	svc := &stubService{invoke: invocation.InvocationResponse{
		InvocationID: "inv_multiline", ServerName: "demo", ToolName: "demo_tool",
		Status: invocation.StatusSucceeded, SubmittedAt: now, CompletedAt: &now,
		Result: json.RawMessage(`{"content":[{"type":"text","text":"done"}]}`),
	}}
	multiLineNotification := []byte("{\"jsonrpc\":\"2.0\",\n\"method\":\"notifications/progress\",\n\"params\":{\"progress\":1}}")
	svc.invokeStreamingFn = func(ctx context.Context, req invocation.CreateInvocationRequest, sink mcp.StreamSink) (invocation.InvocationResponse, error) {
		sink.StreamStarted()
		if err := sink.Event(mcp.StreamEvent{Data: multiLineNotification}); err != nil {
			t.Fatalf("sink.Event: %v", err)
		}
		return svc.invoke, nil
	}
	h := NewHandler(svc, stubServerService{}, nil, nil, nil, nil, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/mcp/demo", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"demo_tool","arguments":{}}}`))
	req.Header.Set("Accept", "application/json, text/event-stream")
	w := httptest.NewRecorder()

	h.Routes().ServeHTTP(w, req)

	reader := bufio.NewReader(w.Body)
	frame := readNextSSEFrame(t, reader)
	if frame.data != string(multiLineNotification) {
		t.Fatalf("expected the multi-line payload reconstructed exactly from multiple \"data:\" lines, got %q", frame.data)
	}
}

// TestMCPToolsCallErrorAfterStreamStartedWritesTerminalErrorFrame is a
// regression test: InvokeStreaming CAN return an error after the stream
// has started (e.g. persisting the result fails after the relay
// completed). A started stream must then end with a terminal JSON-RPC
// error frame carrying the agent's request id — not writeRPCError (a
// second WriteHeader) and not a bare close that would leave the request
// unanswered.
// TestSSERelaySinkHeartbeatsKeepStreamAliveWithoutCorruptingFrames covers
// the intermediary-keepalive requirement: proxies/LBs kill connections
// that carry no traffic (commonly ~60s idle), so an open relay must emit
// `: ping` comments while the upstream is silent — and, because the
// heartbeat runs on its own goroutine, its writes must never interleave
// with (corrupt) an event or terminal frame. Run under -race this also
// proves the mutex discipline.
func TestSSERelaySinkHeartbeatsKeepStreamAliveWithoutCorruptingFrames(t *testing.T) {
	w := httptest.NewRecorder()
	sink := newSSERelaySink(w, w)
	sink.heartbeatInterval = 2 * time.Millisecond

	sink.StreamStarted()
	deadline := time.Now().Add(60 * time.Millisecond)
	for time.Now().Before(deadline) {
		if err := sink.Event(mcp.StreamEvent{Data: []byte(`{"jsonrpc":"2.0","method":"notifications/progress","params":{"progress":1}}`)}); err != nil {
			t.Fatalf("Event: %v", err)
		}
	}
	if err := sink.finishStream([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`)); err != nil {
		t.Fatalf("finishStream: %v", err)
	}

	body := w.Body.String()
	if !strings.Contains(body, ": ping\n\n") {
		t.Fatalf("expected heartbeat comments in the stream, got none in %d bytes", len(body))
	}
	// Frame integrity: every line must be a well-formed SSE line — a
	// heartbeat interleaved mid-frame would produce a line that is neither.
	for _, line := range strings.Split(body, "\n") {
		if line == "" || strings.HasPrefix(line, "data: ") || strings.HasPrefix(line, ": ping") {
			continue
		}
		t.Fatalf("malformed SSE line (heartbeat interleaved mid-frame?): %q", line)
	}
	if !strings.HasSuffix(strings.TrimRight(body, "\n"), `{"jsonrpc":"2.0","id":1,"result":{}}`) {
		t.Fatalf("expected the terminal frame to be the stream's final write, got tail %q", body[max(0, len(body)-120):])
	}
}

// switchableFailingWriter is a ResponseWriter whose writes succeed until
// the test flips broken — simulating an agent whose connection died
// mid-stream. broken is atomic because the heartbeat goroutine writes
// concurrently with the test goroutine.
type switchableFailingWriter struct {
	*httptest.ResponseRecorder
	broken atomic.Bool
}

func (f *switchableFailingWriter) Write(p []byte) (int, error) {
	if f.broken.Load() {
		return 0, fmt.Errorf("connection reset by peer")
	}
	return f.ResponseRecorder.Write(p)
}

// TestSSERelaySinkHeartbeatFailureSurfacesOnNextEvent covers stalled-agent
// detection: when a heartbeat write discovers the connection is dead, the
// failure must stick and abort the relay on the next Event — the
// synchronous relay loop is otherwise blind to the downstream connection
// between events.
func TestSSERelaySinkHeartbeatFailureSurfacesOnNextEvent(t *testing.T) {
	fw := &switchableFailingWriter{ResponseRecorder: httptest.NewRecorder()}
	sink := newSSERelaySink(fw, fw.ResponseRecorder)
	sink.heartbeatInterval = 2 * time.Millisecond

	sink.StreamStarted()
	if err := sink.Event(mcp.StreamEvent{Data: []byte(`{"jsonrpc":"2.0","method":"notifications/progress","params":{"progress":1}}`)}); err != nil {
		t.Fatalf("first Event should succeed, got %v", err)
	}
	fw.broken.Store(true) // the agent's connection dies between events

	// Wait until a heartbeat has hit the dead connection.
	deadlineExceeded := time.Now().Add(2 * time.Second)
	for {
		sink.mu.Lock()
		failed := sink.writeErr != nil
		sink.mu.Unlock()
		if failed {
			break
		}
		if time.Now().After(deadlineExceeded) {
			t.Fatal("heartbeat never observed the write failure")
		}
		time.Sleep(2 * time.Millisecond)
	}

	if err := sink.Event(mcp.StreamEvent{Data: []byte(`{"jsonrpc":"2.0","method":"notifications/progress","params":{"progress":2}}`)}); err == nil {
		t.Fatal("expected the heartbeat's sticky write error to abort the next Event")
	}
	if err := sink.finishStream([]byte(`{}`)); err == nil {
		t.Fatal("expected finishStream to report the dead connection rather than pretend the terminal frame was written")
	}
}

func TestMCPToolsCallAuditsTerminalDeliveryFailure(t *testing.T) {
	now := time.Now().UTC()
	svc := &stubService{invoke: invocation.InvocationResponse{
		InvocationID: "inv_delivery", ServerName: "demo", ToolName: "demo_tool",
		Status: invocation.StatusSucceeded, SubmittedAt: now, CompletedAt: &now,
		Result: json.RawMessage(`{"content":[{"type":"text","text":"done"}]}`),
	}}
	writer := &switchableFailingWriter{ResponseRecorder: httptest.NewRecorder()}
	svc.invokeStreamingFn = func(ctx context.Context, req invocation.CreateInvocationRequest, sink mcp.StreamSink) (invocation.InvocationResponse, error) {
		sink.StreamStarted()
		if err := sink.Event(mcp.StreamEvent{Data: []byte(`{"jsonrpc":"2.0","method":"notifications/progress","params":{"progress":1}}`)}); err != nil {
			t.Fatalf("sink.Event: %v", err)
		}
		writer.broken.Store(true)
		return svc.invoke, nil
	}
	h := NewHandler(svc, stubServerService{}, nil, nil, nil, nil, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/mcp/demo", strings.NewReader(`{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"demo_tool","arguments":{}}}`))
	req.Header.Set("Accept", "text/event-stream")

	h.Routes().ServeHTTP(writer, req)

	if svc.streamDeliveryInvocationID != "inv_delivery" {
		t.Fatalf("delivery audit invocation = %q, want inv_delivery", svc.streamDeliveryInvocationID)
	}
	if svc.streamDeliveryStatus != "failed" {
		t.Fatalf("delivery audit status = %q, want failed", svc.streamDeliveryStatus)
	}
	if !strings.Contains(svc.streamDeliveryMessage, "connection reset") {
		t.Fatalf("delivery audit message = %q, want terminal write failure", svc.streamDeliveryMessage)
	}
}

func TestMCPToolsCallErrorAfterStreamStartedWritesTerminalErrorFrame(t *testing.T) {
	svc := &stubService{}
	svc.invokeStreamingFn = func(ctx context.Context, req invocation.CreateInvocationRequest, sink mcp.StreamSink) (invocation.InvocationResponse, error) {
		sink.StreamStarted()
		if err := sink.Event(mcp.StreamEvent{Data: []byte(`{"jsonrpc":"2.0","method":"notifications/progress","params":{"progress":1}}`)}); err != nil {
			t.Fatalf("sink.Event: %v", err)
		}
		return invocation.InvocationResponse{}, fmt.Errorf("persisting result failed")
	}
	h := NewHandler(svc, stubServerService{}, nil, nil, nil, nil, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/mcp/demo", strings.NewReader(`{"jsonrpc":"2.0","id":77,"method":"tools/call","params":{"name":"demo_tool","arguments":{}}}`))
	req.Header.Set("Accept", "application/json, text/event-stream")
	w := httptest.NewRecorder()

	h.Routes().ServeHTTP(w, req)

	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("expected text/event-stream (headers were already sent), got %q", ct)
	}
	body := w.Body.String()
	if !strings.Contains(body, `"id":77`) {
		t.Fatalf("expected the terminal error frame rewritten to the agent's id 77, got %q", body)
	}
	if !strings.Contains(body, `"error"`) || !strings.Contains(body, "failed to finalize invocation") {
		t.Fatalf("expected a terminal JSON-RPC error frame with the static message, got %q", body)
	}
	// The internal error text must never reach the wire: it can carry
	// SQL/driver detail. It is logged server-side instead.
	if strings.Contains(body, "persisting result failed") {
		t.Fatalf("internal error detail leaked into the terminal frame: %q", body)
	}
}

// TestMCPToolsCallNeverForwardsServerToClientRequestToAgent is a
// regression test: Atryum does not broker server-initiated requests
// (sampling, elicitation, roots) — the agent has no channel to answer one
// arriving on a tools/call response stream, so it must never be forwarded,
// even though it is still relayed to the sink's Event method for the
// service layer to audit.
func TestMCPToolsCallNeverForwardsServerToClientRequestToAgent(t *testing.T) {
	now := time.Now().UTC()
	svc := &stubService{invoke: invocation.InvocationResponse{
		InvocationID: "inv_srvreq", ServerName: "demo", ToolName: "demo_tool",
		Status: invocation.StatusSucceeded, SubmittedAt: now, CompletedAt: &now,
		Result: json.RawMessage(`{"content":[{"type":"text","text":"done"}]}`),
	}}
	svc.invokeStreamingFn = func(ctx context.Context, req invocation.CreateInvocationRequest, sink mcp.StreamSink) (invocation.InvocationResponse, error) {
		sink.StreamStarted()
		if err := sink.Event(mcp.StreamEvent{Data: []byte(`{"jsonrpc":"2.0","id":"srv-1","method":"sampling/createMessage","params":{}}`), ServerRequest: true}); err != nil {
			t.Fatalf("sink.Event(server request): %v", err)
		}
		if err := sink.Event(mcp.StreamEvent{Data: []byte(`{"jsonrpc":"2.0","method":"notifications/progress","params":{"progress":1}}`)}); err != nil {
			t.Fatalf("sink.Event(notification): %v", err)
		}
		return svc.invoke, nil
	}
	h := NewHandler(svc, stubServerService{}, nil, nil, nil, nil, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/mcp/demo", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"demo_tool","arguments":{}}}`))
	req.Header.Set("Accept", "application/json, text/event-stream")
	w := httptest.NewRecorder()

	h.Routes().ServeHTTP(w, req)

	body := w.Body.String()
	if strings.Contains(body, "sampling/createMessage") {
		t.Fatalf("expected the server-to-client request never to reach the agent, got %q", body)
	}
	if !strings.Contains(body, "notifications/progress") {
		t.Fatalf("expected the notification to still be relayed, got %q", body)
	}
}

func TestMCPToolsCallStreamedFailureWritesTerminalErrorFrame(t *testing.T) {
	now := time.Now().UTC()
	svc := &stubService{invoke: invocation.InvocationResponse{
		InvocationID: "inv_failed", ServerName: "demo", ToolName: "demo_tool",
		Status: invocation.StatusFailed, SubmittedAt: now, CompletedAt: &now,
		Error: json.RawMessage(`{"content":[{"type":"text","text":"upstream exploded"}],"isError":true}`),
	}}
	svc.invokeStreamingFn = func(ctx context.Context, req invocation.CreateInvocationRequest, sink mcp.StreamSink) (invocation.InvocationResponse, error) {
		sink.StreamStarted()
		if err := sink.Event(mcp.StreamEvent{Data: []byte(`{"jsonrpc":"2.0","method":"notifications/progress","params":{"progress":1}}`)}); err != nil {
			t.Fatalf("sink.Event: %v", err)
		}
		return svc.invoke, nil
	}
	h := NewHandler(svc, stubServerService{}, nil, nil, nil, nil, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/mcp/demo", strings.NewReader(`{"jsonrpc":"2.0","id":55,"method":"tools/call","params":{"name":"demo_tool","arguments":{}}}`))
	req.Header.Set("Accept", "application/json, text/event-stream")
	w := httptest.NewRecorder()

	h.Routes().ServeHTTP(w, req)

	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("expected text/event-stream, got %q", ct)
	}
	body := w.Body.String()
	if !strings.Contains(body, "upstream exploded") {
		t.Fatalf("expected the terminal error content, got %q", body)
	}
	if !strings.Contains(body, `"id":55`) {
		t.Fatalf("expected terminal frame rewritten to the agent's id 55, got %q", body)
	}
}

func TestMCPToolsCallWithoutStreamAcceptGetsPlainJSONEvenIfUpstreamWouldStream(t *testing.T) {
	now := time.Now().UTC()
	svc := &stubService{invoke: invocation.InvocationResponse{
		InvocationID: "inv_1", ServerName: "demo", ToolName: "demo_tool",
		Status: invocation.StatusSucceeded, SubmittedAt: now, CompletedAt: &now,
		Result: json.RawMessage(`{"content":[{"type":"text","text":"ok"}]}`),
	}}
	svc.invokeStreamingFn = func(context.Context, invocation.CreateInvocationRequest, mcp.StreamSink) (invocation.InvocationResponse, error) {
		t.Fatal("InvokeStreaming should never be called when the agent did not send Accept: text/event-stream")
		return invocation.InvocationResponse{}, nil
	}
	h := NewHandler(svc, stubServerService{}, nil, nil, nil, nil, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/mcp/demo", strings.NewReader(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"demo_tool","arguments":{}}}`))
	w := httptest.NewRecorder()

	h.Routes().ServeHTTP(w, req)

	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("expected application/json, got %q", ct)
	}
	if !strings.Contains(w.Body.String(), `"text":"ok"`) {
		t.Fatalf("expected buffered JSON result, got %s", w.Body.String())
	}
}

func TestMCPToolsCallDenialWithStreamCapableAgentStaysJSON(t *testing.T) {
	now := time.Now().UTC()
	rules := &stubRulesRepo{rules: []store.Rule{
		{ID: "bash-deny", Action: invocation.RuleActionAutoDeny, ServerPatterns: []string{"demo"}, ToolPatterns: []string{"Bash"}, Enabled: true, Order: 0},
	}}
	svc := &stubService{invoke: invocation.InvocationResponse{
		InvocationID: "inv_denied", ServerName: "demo", ToolName: "Bash",
		Status:      invocation.StatusDenied,
		SubmittedAt: now, CompletedAt: &now,
		Error: json.RawMessage(`{"content":[{"type":"text","text":"Tool call denied by approval rule (auto_deny)."}],"isError":true}`),
	}}
	svc.invokeStreamingFn = func(context.Context, invocation.CreateInvocationRequest, mcp.StreamSink) (invocation.InvocationResponse, error) {
		// A denial is decided before any upstream execution — the sink must
		// never be touched, so this returns the same stubbed response
		// Invoke would, without ever calling StreamStarted/Event.
		return svc.invoke, nil
	}
	h := NewHandler(svc, stubServerService{}, nil, rules, nil, nil, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/mcp/demo", strings.NewReader(`{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"Bash","arguments":{"cmd":"ls"}}}`))
	req.Header.Set("Accept", "application/json, text/event-stream")
	w := httptest.NewRecorder()

	h.Routes().ServeHTTP(w, req)

	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("expected application/json (stream never starts on denial), got %q", ct)
	}
	var rpcResp struct {
		Result struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &rpcResp); err != nil {
		t.Fatal(err)
	}
	if !rpcResp.Result.IsError {
		t.Fatalf("expected isError=true, got %#v", rpcResp.Result)
	}
	if len(rpcResp.Result.Content) < 2 {
		t.Fatalf("expected denial text plus rules context, got %#v", rpcResp.Result.Content)
	}
}

// sseEventFrame is one decoded "event:"/"data:" SSE frame read off a live
// HTTP response body.
type sseEventFrame struct {
	event string
	data  string
}

// readNextSSEFrame blocks on reader until one full SSE frame (up to the
// blank line that terminates it) has arrived, then returns it. Used to
// prove live, incremental delivery: unlike parsing a fully-buffered body,
// this only returns once that specific frame has actually been read off
// the wire.
func readNextSSEFrame(t *testing.T, reader *bufio.Reader) sseEventFrame {
	t.Helper()
	var frame sseEventFrame
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read SSE frame: %v (partial line=%q)", err, line)
		}
		line = strings.TrimRight(line, "\n")
		if line == "" {
			return frame
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, ok := strings.Cut(line, ": ")
		if !ok {
			field, value = line, ""
		}
		switch field {
		case "event":
			frame.event = value
		case "data":
			if frame.data != "" {
				frame.data += "\n"
			}
			frame.data += value
		}
	}
}

// TestMCPToolsCallEndToEndRelaysLiveBeforeTerminalResponseExists is the
// primary acceptance test for the streaming relay: a real Handler, real
// invocation.Service, and real mcp.Client are wired to a fake upstream MCP
// server that flushes one progress notification and then blocks — on a
// channel this test controls — before it is even able to write its
// terminal response. The agent (a real HTTP client reading the response
// incrementally) must observe the notification before that channel is
// released, which proves the intermediate event was relayed live rather
// than after the fact from a buffered body: the terminal response cannot
// exist yet at the point the test asserts the notification arrived.
func TestMCPToolsCallEndToEndRelaysLiveBeforeTerminalResponseExists(t *testing.T) {
	releaseTerminal := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			// The standalone SSE stream Atryum opens alongside a
			// progressToken-bearing tools/call. This fake upstream doesn't
			// support it (a legitimate, spec-allowed response); the agent's
			// progress notification arrives via the tools/call POST
			// response itself below, exercised independently of this.
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode upstream request: %v", err)
			return
		}
		switch body["method"] {
		case "initialize":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": body["id"],
				"result": map[string]any{"serverInfo": map[string]any{"name": "fake", "version": "0.1.0"}, "capabilities": map[string]any{}},
			})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/call":
			w.Header().Set("Content-Type", "text/event-stream")
			flusher := w.(http.Flusher)

			// Echo the agent's own progressToken back — this only works if
			// _meta forwarded all the way from the agent's request through
			// to the upstream tools/call envelope (Phase 0).
			params, _ := body["params"].(map[string]any)
			meta, _ := params["_meta"].(map[string]any)
			token, _ := meta["progressToken"].(string)
			progress, _ := json.Marshal(map[string]any{
				"jsonrpc": "2.0", "method": "notifications/progress",
				"params": map[string]any{"progressToken": token, "progress": 1},
			})
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", progress)
			flusher.Flush()

			<-releaseTerminal // the terminal response cannot exist until the test releases this

			result, _ := json.Marshal(map[string]any{
				"jsonrpc": "2.0", "id": "1",
				"result": map[string]any{"content": []map[string]any{{"type": "text", "text": "all done"}}},
			})
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", result)
			flusher.Flush()
		default:
			t.Errorf("unexpected upstream method %v", body["method"])
		}
	}))
	defer upstream.Close()

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	if err := store.InitDB(db); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	serverRepo := store.NewServerRepo(db)
	resolver := mcp.NewResolver(serverRepo, config.Config{
		Upstreams: []config.UpstreamConfig{{Name: "demo", Mode: "http", BaseURL: upstream.URL, Enabled: true, TimeoutSeconds: 5}},
	})
	if err := resolver.BootstrapIfEmpty(context.Background()); err != nil {
		t.Fatal(err)
	}
	svc := invocation.NewService(
		store.NewInvocationRepo(db), store.NewEventRepo(db), resolver, mcp.NewHTTPClient(),
		policy.AlwaysApproveProvider{}, 5*time.Second, nil, nil, nil, nil,
	)

	h := NewHandler(svc, stubServerService{}, nil, nil, nil, nil, nil, nil, nil, nil)
	agentServer := httptest.NewServer(h.Routes())
	defer agentServer.Close()

	reqBody := `{"jsonrpc":"2.0","id":42,"method":"tools/call","params":{"name":"demo_tool","arguments":{},"_meta":{"progressToken":"tok-live"}}}`
	req, err := http.NewRequest(http.MethodPost, agentServer.URL+"/mcp/demo", strings.NewReader(reqBody))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("agent request: %v", err)
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("expected text/event-stream, got %q", ct)
	}

	reader := bufio.NewReader(resp.Body)
	first := readNextSSEFrame(t, reader)
	if !strings.Contains(first.data, "notifications/progress") || !strings.Contains(first.data, "tok-live") {
		t.Fatalf("expected the progress notification (echoing the agent's progressToken) first, got %q", first.data)
	}

	// Only now — after the agent has actually received the intermediate
	// event over the wire — does the fake upstream get to write its
	// terminal response.
	close(releaseTerminal)

	terminal := readNextSSEFrame(t, reader)
	if !strings.Contains(terminal.data, `"id":42`) {
		t.Fatalf("expected the terminal frame rewritten to the agent's id 42, got %q", terminal.data)
	}
	if !strings.Contains(terminal.data, "all done") {
		t.Fatalf("expected the terminal result body, got %q", terminal.data)
	}
}

// TestWriteSSEEventNormalizesCarriageReturns pins that no raw CR ever
// reaches the wire inside a data field: CR is an SSE line terminator, so an
// upstream that embeds one (legal JSON inter-token whitespace) would
// otherwise have its frame split mid-message by a compliant agent parser.
func TestWriteSSEEventNormalizesCarriageReturns(t *testing.T) {
	rec := httptest.NewRecorder()
	payload := []byte("{\"a\":\r\r1,\r\n\"b\":2}")
	if err := writeSSEEvent(rec, rec, "", payload); err != nil {
		t.Fatalf("writeSSEEvent returned error: %v", err)
	}
	body := rec.Body.String()
	if strings.Contains(body, "\r") {
		t.Fatalf("raw CR reached the wire: %q", body)
	}
	want := "data: {\"a\":\ndata: \ndata: 1,\ndata: \"b\":2}\n\n"
	if body != want {
		t.Fatalf("framed body = %q, want %q", body, want)
	}
}
