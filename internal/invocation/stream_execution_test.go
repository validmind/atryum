package invocation_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/validmind/atryum/internal/config"
	"github.com/validmind/atryum/internal/invocation"
	"github.com/validmind/atryum/internal/invocation/policy"
	"github.com/validmind/atryum/internal/mcp"
	"github.com/validmind/atryum/internal/store"
)

// recordingSink is a test mcp.StreamSink that records what it received.
// onEvent, when set, lets a test hook into delivery (e.g. to synchronize
// with a background approval goroutine). Safe for concurrent use: Event may
// run on the InvokeStreaming call's goroutine while a test's assertions run
// on another.
type recordingSink struct {
	mu      sync.Mutex
	started bool
	events  []mcp.StreamEvent
	onEvent func(mcp.StreamEvent) error
}

type blockingStreamEventRepo struct {
	inner   *store.EventRepo
	started chan struct{}
	once    sync.Once
}

func (r *blockingStreamEventRepo) Create(ctx context.Context, evt invocation.Event) error {
	if evt.EventType == "invocation.stream_event" {
		r.once.Do(func() { close(r.started) })
		<-ctx.Done()
		return ctx.Err()
	}
	return r.inner.Create(ctx, evt)
}

func (r *blockingStreamEventRepo) ListByInvocation(ctx context.Context, invocationID string, filter invocation.EventListFilter) ([]invocation.Event, int, error) {
	return r.inner.ListByInvocation(ctx, invocationID, filter)
}

func (s *recordingSink) StreamStarted() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.started = true
}

func (s *recordingSink) Event(evt mcp.StreamEvent) error {
	s.mu.Lock()
	s.events = append(s.events, evt)
	s.mu.Unlock()
	if s.onEvent != nil {
		return s.onEvent(evt)
	}
	return nil
}

func (s *recordingSink) touched() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.started || len(s.events) > 0
}

func (s *recordingSink) eventCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.events)
}

// sseToolCallUpstream builds an httptest.Server implementing the
// initialize/notifications.initialized handshake, dispatching tools/call to
// callHandler so a test controls exactly what SSE bytes are written.
func sseToolCallUpstream(t *testing.T, callHandler func(w http.ResponseWriter, r *http.Request, body map[string]any)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
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
			callHandler(w, r, body)
		default:
			t.Fatalf("unexpected method %q", body["method"])
		}
	}))
}

func writeSSEEvent(w http.ResponseWriter, flusher http.Flusher, data string) {
	_, _ = w.Write([]byte("event: message\ndata: " + data + "\n\n"))
	flusher.Flush()
}

func TestInvokeStreamingRelaysEventsAndAuditsThem(t *testing.T) {
	upstream := sseToolCallUpstream(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		writeSSEEvent(w, flusher, `{"jsonrpc":"2.0","method":"notifications/progress","params":{"progress":1}}`)
		writeSSEEvent(w, flusher, `{"jsonrpc":"2.0","id":"1","result":{"content":[{"type":"text","text":"done"}]}}`)
	})
	defer upstream.Close()

	service := newTestService(t, config.Config{
		Defaults:  config.DefaultsConfig{RequestTimeoutSeconds: 5},
		Upstreams: []config.UpstreamConfig{{Name: "shortcut", Mode: "http", BaseURL: upstream.URL, Enabled: true, TimeoutSeconds: 5}},
	})

	sink := &recordingSink{}
	resp, err := service.InvokeStreaming(context.Background(), invocation.CreateInvocationRequest{Server: "shortcut", Tool: "demo", Input: map[string]any{"n": 1}}, sink)
	if err != nil {
		t.Fatalf("InvokeStreaming returned error: %v", err)
	}
	if resp.Status != invocation.StatusSucceeded {
		t.Fatalf("expected succeeded, got %s: %s", resp.Status, resp.Error)
	}
	if !sink.started {
		t.Fatal("expected StreamStarted to fire")
	}
	if sink.eventCount() != 1 {
		t.Fatalf("expected exactly one relayed event, got %d", sink.eventCount())
	}
	if !jsonContains(resp.Result, "done") {
		t.Fatalf("expected terminal result body, got %s", resp.Result)
	}

	events, err := service.Events(context.Background(), resp.InvocationID, invocation.EventListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	var sawStreamEvent, sawStreamCompleted bool
	for _, evt := range events.Items {
		switch evt.Type {
		case "invocation.stream_event":
			sawStreamEvent = true
			var payload struct {
				Seq          int    `json:"seq"`
				UpstreamName string `json:"upstream_name"`
			}
			if err := json.Unmarshal(evt.Data, &payload); err != nil {
				t.Fatalf("decode invocation.stream_event payload: %v", err)
			}
			if payload.Seq != 1 {
				t.Fatalf("expected seq 1, got %d", payload.Seq)
			}
			if payload.UpstreamName != "shortcut" {
				t.Fatalf("expected upstream_name shortcut, got %q", payload.UpstreamName)
			}
		case "invocation.stream_completed":
			sawStreamCompleted = true
			var payload struct {
				EventsTotal int    `json:"events_total"`
				Terminal    string `json:"terminal"`
			}
			if err := json.Unmarshal(evt.Data, &payload); err != nil {
				t.Fatalf("decode invocation.stream_completed payload: %v", err)
			}
			if payload.EventsTotal != 1 {
				t.Fatalf("expected events_total 1, got %d", payload.EventsTotal)
			}
			if payload.Terminal != "succeeded" {
				t.Fatalf("expected terminal succeeded, got %q", payload.Terminal)
			}
		}
	}
	if !sawStreamEvent {
		t.Fatal("expected an invocation.stream_event audit row")
	}
	if !sawStreamCompleted {
		t.Fatal("expected an invocation.stream_completed audit row")
	}
}

func TestInvokeStreamingAuditCapsEnforcedWithoutSuppressingRelay(t *testing.T) {
	upstream := sseToolCallUpstream(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for i := 0; i < 3; i++ {
			writeSSEEvent(w, flusher, `{"jsonrpc":"2.0","method":"notifications/progress","params":{"progress":1}}`)
		}
		writeSSEEvent(w, flusher, `{"jsonrpc":"2.0","id":"1","result":{"content":[{"type":"text","text":"done"}]}}`)
	})
	defer upstream.Close()

	db := newSQLiteTestDB(t)
	serverRepo := store.NewServerRepo(db)
	cfg := config.Config{Upstreams: []config.UpstreamConfig{{Name: "shortcut", Mode: "http", BaseURL: upstream.URL, Enabled: true, TimeoutSeconds: 5}}}
	resolver := mcp.NewResolver(serverRepo, cfg)
	if err := resolver.BootstrapIfEmpty(context.Background()); err != nil {
		t.Fatal(err)
	}
	service := invocation.NewService(
		store.NewInvocationRepo(db), store.NewEventRepo(db), resolver, mcp.NewHTTPClient(),
		policy.AlwaysApproveProvider{}, 5*time.Second, nil, nil, nil, nil,
	)
	service.SetStreamOptions(mcp.StreamOptions{}, invocation.StreamAuditLimits{MaxEvents: 1})

	sink := &recordingSink{}
	resp, err := service.InvokeStreaming(context.Background(), invocation.CreateInvocationRequest{Server: "shortcut", Tool: "demo", Input: map[string]any{}}, sink)
	if err != nil {
		t.Fatalf("InvokeStreaming returned error: %v", err)
	}
	if resp.Status != invocation.StatusSucceeded {
		t.Fatalf("expected succeeded, got %s: %s", resp.Status, resp.Error)
	}
	// The cap bounds what's persisted, not what's relayed: the agent-facing
	// sink must still see every event even once the audit log stops
	// recording them individually.
	if sink.eventCount() != 3 {
		t.Fatalf("expected all 3 events relayed to the sink despite the audit cap, got %d", sink.eventCount())
	}

	events, err := service.Events(context.Background(), resp.InvocationID, invocation.EventListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	streamEventRows := 0
	for _, evt := range events.Items {
		if evt.Type == "invocation.stream_event" {
			streamEventRows++
		}
		if evt.Type == "invocation.stream_completed" {
			var payload struct {
				EventsTotal int `json:"events_total"`
			}
			if err := json.Unmarshal(evt.Data, &payload); err != nil {
				t.Fatalf("decode invocation.stream_completed payload: %v", err)
			}
			if payload.EventsTotal != 3 {
				t.Fatalf("expected events_total to reflect the true count (3) even though only 1 was persisted, got %d", payload.EventsTotal)
			}
		}
	}
	if streamEventRows != 1 {
		t.Fatalf("expected exactly 1 persisted invocation.stream_event row (MaxEvents cap), got %d", streamEventRows)
	}
}

func TestInvokeStreamingBlockedAuditWriteDoesNotDelayRelay(t *testing.T) {
	upstream := sseToolCallUpstream(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		writeSSEEvent(w, flusher, `{"jsonrpc":"2.0","method":"notifications/progress","params":{"progress":1}}`)
		writeSSEEvent(w, flusher, `{"jsonrpc":"2.0","id":"1","result":{"content":[{"type":"text","text":"done"}]}}`)
	})
	defer upstream.Close()

	db := newSQLiteTestDB(t)
	serverRepo := store.NewServerRepo(db)
	resolver := mcp.NewResolver(serverRepo, config.Config{Upstreams: []config.UpstreamConfig{{Name: "shortcut", Mode: "http", BaseURL: upstream.URL, Enabled: true, TimeoutSeconds: 5}}})
	if err := resolver.BootstrapIfEmpty(context.Background()); err != nil {
		t.Fatal(err)
	}
	events := &blockingStreamEventRepo{inner: store.NewEventRepo(db), started: make(chan struct{})}
	service := invocation.NewService(
		store.NewInvocationRepo(db), events, resolver, mcp.NewHTTPClient(),
		policy.AlwaysApproveProvider{}, 5*time.Second, nil, nil, nil, nil,
	)

	delivered := make(chan struct{})
	var deliveredOnce sync.Once
	sink := &recordingSink{onEvent: func(mcp.StreamEvent) error {
		deliveredOnce.Do(func() { close(delivered) })
		return nil
	}}
	done := make(chan error, 1)
	go func() {
		resp, err := service.InvokeStreaming(context.Background(), invocation.CreateInvocationRequest{Server: "shortcut", Tool: "demo", Input: map[string]any{}}, sink)
		if err == nil && resp.Status != invocation.StatusSucceeded {
			err = fmt.Errorf("status = %s, want succeeded", resp.Status)
		}
		done <- err
	}()

	select {
	case <-events.started:
	case <-time.After(time.Second):
		t.Fatal("audit write did not start")
	}
	select {
	case <-delivered:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("relay waited for blocked audit storage")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stream did not finish after bounded audit write timed out")
	}
}

func TestInvokeStreamingSinkAbortMarksFailedAsDownstreamAborted(t *testing.T) {
	upstream := sseToolCallUpstream(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		writeSSEEvent(w, flusher, `{"jsonrpc":"2.0","method":"notifications/progress","params":{"progress":1}}`)
		writeSSEEvent(w, flusher, `{"jsonrpc":"2.0","method":"notifications/progress","params":{"progress":2}}`)
	})
	defer upstream.Close()

	service := newTestService(t, config.Config{
		Defaults:  config.DefaultsConfig{RequestTimeoutSeconds: 5},
		Upstreams: []config.UpstreamConfig{{Name: "shortcut", Mode: "http", BaseURL: upstream.URL, Enabled: true, TimeoutSeconds: 5}},
	})

	sink := &recordingSink{onEvent: func(mcp.StreamEvent) error {
		return errors.New("downstream connection closed")
	}}
	resp, err := service.InvokeStreaming(context.Background(), invocation.CreateInvocationRequest{Server: "shortcut", Tool: "demo", Input: map[string]any{}}, sink)
	if err != nil {
		t.Fatalf("InvokeStreaming returned error: %v", err)
	}
	if resp.Status != invocation.StatusFailed {
		t.Fatalf("expected failed status, got %s", resp.Status)
	}

	events, err := service.Events(context.Background(), resp.InvocationID, invocation.EventListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, evt := range events.Items {
		if evt.Type == "invocation.failed" && jsonContains(evt.Data, "stream_aborted_downstream") {
			found = true
		}
	}
	if !found {
		t.Fatal("expected an invocation.failed event with reason stream_aborted_downstream")
	}
}

func TestInvokeStreamingSinkAbortPersistsFailureAfterRequestContextCancellation(t *testing.T) {
	upstream := sseToolCallUpstream(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		writeSSEEvent(w, flusher, `{"jsonrpc":"2.0","method":"notifications/progress","params":{"progress":1}}`)
	})
	defer upstream.Close()

	service := newTestService(t, config.Config{
		Defaults:  config.DefaultsConfig{RequestTimeoutSeconds: 5},
		Upstreams: []config.UpstreamConfig{{Name: "shortcut", Mode: "http", BaseURL: upstream.URL, Enabled: true, TimeoutSeconds: 5}},
	})

	ctx, cancel := context.WithCancel(context.Background())
	sink := &recordingSink{onEvent: func(mcp.StreamEvent) error {
		cancel() // net/http cancels the request context when the agent disconnects.
		return errors.New("downstream connection closed")
	}}
	resp, err := service.InvokeStreaming(ctx, invocation.CreateInvocationRequest{Server: "shortcut", Tool: "demo", Input: map[string]any{}}, sink)
	if err != nil {
		t.Fatalf("InvokeStreaming returned error: %v", err)
	}

	persisted, err := service.Get(context.Background(), resp.InvocationID)
	if err != nil {
		t.Fatalf("read persisted invocation: %v", err)
	}
	if persisted.Status != invocation.StatusFailed {
		t.Fatalf("persisted status = %s, want failed after downstream disconnect", persisted.Status)
	}

	events, err := service.Events(context.Background(), resp.InvocationID, invocation.EventListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, evt := range events.Items {
		if evt.Type == "invocation.failed" && jsonContains(evt.Data, "stream_aborted_downstream") {
			return
		}
	}
	t.Fatal("expected persisted invocation.failed event with reason stream_aborted_downstream")
}

func TestInvokeStreamingIdleTimeoutMarksFailedAsStreamTimeout(t *testing.T) {
	blockUntilTestDone := make(chan struct{})
	upstream := sseToolCallUpstream(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		writeSSEEvent(w, flusher, `{"jsonrpc":"2.0","method":"notifications/progress","params":{"progress":1}}`)
		<-blockUntilTestDone // never send the terminal event
	})
	t.Cleanup(func() {
		close(blockUntilTestDone)
		upstream.Close()
	})

	db := newSQLiteTestDB(t)
	serverRepo := store.NewServerRepo(db)
	cfg := config.Config{Upstreams: []config.UpstreamConfig{{Name: "shortcut", Mode: "http", BaseURL: upstream.URL, Enabled: true, TimeoutSeconds: 5}}}
	resolver := mcp.NewResolver(serverRepo, cfg)
	if err := resolver.BootstrapIfEmpty(context.Background()); err != nil {
		t.Fatal(err)
	}
	service := invocation.NewService(
		store.NewInvocationRepo(db), store.NewEventRepo(db), resolver, mcp.NewHTTPClient(),
		policy.AlwaysApproveProvider{}, 5*time.Second, nil, nil, nil, nil,
	)
	service.SetStreamOptions(mcp.StreamOptions{IdleTimeout: 50 * time.Millisecond}, invocation.StreamAuditLimits{})

	sink := &recordingSink{}
	resp, err := service.InvokeStreaming(context.Background(), invocation.CreateInvocationRequest{Server: "shortcut", Tool: "demo", Input: map[string]any{}}, sink)
	if err != nil {
		t.Fatalf("InvokeStreaming returned error: %v", err)
	}
	if resp.Status != invocation.StatusFailed {
		t.Fatalf("expected failed status, got %s", resp.Status)
	}

	events, err := service.Events(context.Background(), resp.InvocationID, invocation.EventListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, evt := range events.Items {
		if evt.Type == "invocation.failed" && jsonContains(evt.Data, "stream_timeout") {
			found = true
		}
	}
	if !found {
		t.Fatal("expected an invocation.failed event with reason stream_timeout")
	}
}

func TestInvokeStreamingMidStreamSessionRetryRefusalMarksFailedWithDistinctReason(t *testing.T) {
	var toolsCallCount int
	upstream := sseToolCallUpstream(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		toolsCallCount++
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		writeSSEEvent(w, flusher, `{"jsonrpc":"2.0","method":"notifications/progress","params":{"progress":1}}`)
		writeSSEEvent(w, flusher, `{"jsonrpc":"2.0","id":"1","error":{"code":-32000,"message":"No session ID provided for non-initialization request"}}`)
	})
	defer upstream.Close()

	service := newTestService(t, config.Config{
		Defaults:  config.DefaultsConfig{RequestTimeoutSeconds: 5},
		Upstreams: []config.UpstreamConfig{{Name: "shortcut", Mode: "http", BaseURL: upstream.URL, Enabled: true, TimeoutSeconds: 5}},
	})

	sink := &recordingSink{}
	resp, err := service.InvokeStreaming(context.Background(), invocation.CreateInvocationRequest{Server: "shortcut", Tool: "demo", Input: map[string]any{}}, sink)
	if err != nil {
		t.Fatalf("InvokeStreaming returned error: %v", err)
	}
	if resp.Status != invocation.StatusFailed {
		t.Fatalf("expected failed status, got %s", resp.Status)
	}
	if toolsCallCount != 1 {
		t.Fatalf("tools/call count = %d, want 1 (no retry once events were relayed mid-stream)", toolsCallCount)
	}

	events, err := service.Events(context.Background(), resp.InvocationID, invocation.EventListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, evt := range events.Items {
		if evt.Type == "invocation.failed" && jsonContains(evt.Data, "stream_session_retry_refused") {
			found = true
		}
	}
	if !found {
		t.Fatal("expected an invocation.failed event with reason stream_session_retry_refused, distinguishable from a generic transport_error")
	}
}

func TestInvokeStreamingApprovalGateDoesNotTouchSinkBeforeApproval(t *testing.T) {
	upstream := sseToolCallUpstream(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		writeSSEEvent(w, flusher, `{"jsonrpc":"2.0","method":"notifications/progress","params":{"progress":1}}`)
		writeSSEEvent(w, flusher, `{"jsonrpc":"2.0","id":"1","result":{"content":[{"type":"text","text":"done"}]}}`)
	})
	defer upstream.Close()

	db := newSQLiteTestDB(t)
	serverRepo := store.NewServerRepo(db)
	cfg := config.Config{Upstreams: []config.UpstreamConfig{{Name: "shortcut", Mode: "http", BaseURL: upstream.URL, Enabled: true, TimeoutSeconds: 5}}}
	resolver := mcp.NewResolver(serverRepo, cfg)
	if err := resolver.BootstrapIfEmpty(context.Background()); err != nil {
		t.Fatal(err)
	}
	service := invocation.NewService(
		store.NewInvocationRepo(db), store.NewEventRepo(db), resolver, mcp.NewHTTPClient(),
		policy.ManualApprovalProvider{}, 5*time.Second, nil, nil, nil, nil,
	)

	sink := &recordingSink{}
	go func() {
		time.Sleep(50 * time.Millisecond)
		if sink.touched() {
			t.Errorf("sink touched before approval — approval gating must precede any relay")
		}
		list, err := service.List(context.Background(), invocation.InvocationListFilter{Limit: 10})
		if err != nil || len(list.Items) == 0 {
			t.Errorf("expected a pending invocation to approve")
			return
		}
		if err := service.Approve(context.Background(), list.Items[0].InvocationID, ""); err != nil {
			t.Errorf("approve: %v", err)
		}
	}()

	resp, err := service.InvokeStreaming(context.Background(), invocation.CreateInvocationRequest{Server: "shortcut", Tool: "demo", Input: map[string]any{}}, sink)
	if err != nil {
		t.Fatalf("InvokeStreaming returned error: %v", err)
	}
	if resp.Status != invocation.StatusSucceeded {
		t.Fatalf("expected succeeded, got %s: %s", resp.Status, resp.Error)
	}
	if !sink.touched() {
		t.Fatal("expected the sink to have been touched after approval unblocked execution")
	}
}

func TestInvokeStreamingNilSinkMatchesInvoke(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		switch body["method"] {
		case "initialize":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": body["id"], "result": map[string]any{"serverInfo": map[string]any{"name": "fake", "version": "0.1.0"}, "capabilities": map[string]any{}}})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/call":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"content": []map[string]any{{"type": "text", "text": "ok"}}}})
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer upstream.Close()

	service := newTestService(t, config.Config{
		Defaults:  config.DefaultsConfig{RequestTimeoutSeconds: 5},
		Upstreams: []config.UpstreamConfig{{Name: "shortcut", Mode: "http", BaseURL: upstream.URL, Enabled: true, TimeoutSeconds: 5}},
	})

	viaInvoke, err := service.Invoke(context.Background(), invocation.CreateInvocationRequest{Server: "shortcut", Tool: "demo", Input: map[string]any{}})
	if err != nil {
		t.Fatalf("Invoke returned error: %v", err)
	}
	viaStreaming, err := service.InvokeStreaming(context.Background(), invocation.CreateInvocationRequest{Server: "shortcut", Tool: "demo", Input: map[string]any{}}, nil)
	if err != nil {
		t.Fatalf("InvokeStreaming returned error: %v", err)
	}
	if viaInvoke.Status != viaStreaming.Status {
		t.Fatalf("status mismatch: Invoke=%s InvokeStreaming(nil)=%s", viaInvoke.Status, viaStreaming.Status)
	}
	if string(viaInvoke.Result) != string(viaStreaming.Result) {
		t.Fatalf("result mismatch: Invoke=%s InvokeStreaming(nil)=%s", viaInvoke.Result, viaStreaming.Result)
	}
}
