package invocation_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/validmind/atryum/internal/config"
	"github.com/validmind/atryum/internal/invocation"
	"github.com/validmind/atryum/internal/invocation/policy"
	"github.com/validmind/atryum/internal/mcp"
	"github.com/validmind/atryum/internal/store"
)

// A held call streams only once approved, relays the upstream's progress to
// the caller's sink, and records it under the invocation with what correlates
// it to the downstream and upstream requests.
func TestHeldStreamingCallRelaysAfterApprovalAndRecordsTheStream(t *testing.T) {
	var calls atomic.Int32
	var approved atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req mcp.Envelope
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch req.Method {
		case "initialize":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"capabilities": map[string]any{}}})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/call":
			calls.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{\"progress\":1}}\n\n"))
			_, _ = w.Write([]byte("data: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"ok\"}]}}\n\n"))
		}
	}))
	defer upstream.Close()

	db := newSQLiteTestDB(t)
	resolver := mcp.NewResolver(store.NewServerRepo(db), config.Config{
		Upstreams: []config.UpstreamConfig{{Name: "slow", Mode: "http", BaseURL: upstream.URL, Enabled: true, TimeoutSeconds: 5}},
	})
	if err := resolver.BootstrapIfEmpty(context.Background()); err != nil {
		t.Fatal(err)
	}
	svc := invocation.NewService(store.NewInvocationRepo(db), store.NewEventRepo(db), resolver, mcp.NewHTTPClient(), policy.ManualApprovalProvider{}, 5*time.Second, nil, nil, nil, nil)

	go func() {
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
			list, err := svc.List(context.Background(), invocation.InvocationListFilter{Limit: 1})
			if err != nil || len(list.Items) == 0 || list.Items[0].Status != invocation.StatusPendingApproval {
				continue
			}
			if n := calls.Load(); n != 0 {
				t.Errorf("upstream called %d times before approval", n)
			}
			if err := svc.Approve(context.Background(), list.Items[0].InvocationID, ""); err != nil {
				t.Errorf("approve: %v", err)
			}
			approved.Store(true)
			return
		}
	}()

	var relayed []mcp.StreamEvent
	ctx := mcp.WithStreamSink(context.Background(), &mcp.StreamSink{Emit: func(evt mcp.StreamEvent) { relayed = append(relayed, evt) }})
	requestID := "req-9"
	resp, err := svc.Invoke(ctx, invocation.CreateInvocationRequest{Server: "slow", Tool: "work", Input: map[string]any{}, RequestID: &requestID})
	if err != nil || resp.Status != invocation.StatusSucceeded {
		t.Fatalf("status=%s err=%v", resp.Status, err)
	}
	if !approved.Load() {
		t.Fatal("the call ran without being held for approval")
	}
	if len(relayed) != 1 || relayed[0].Method != "notifications/progress" {
		t.Fatalf("relayed = %+v", relayed)
	}

	events, err := svc.Events(context.Background(), resp.InvocationID, invocation.EventListFilter{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	var streamed *invocation.EventResponse
	for i := range events.Items {
		if events.Items[i].Type == "invocation.streamed" {
			streamed = &events.Items[i]
		}
	}
	if streamed == nil {
		t.Fatalf("no invocation.streamed event in %+v", events.Items)
	}
	var payload struct {
		RequestID         string            `json:"request_id"`
		UpstreamRequestID string            `json:"upstream_request_id"`
		Events            []mcp.StreamEvent `json:"events"`
	}
	if err := json.Unmarshal(streamed.Data, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.RequestID != "req-9" || payload.UpstreamRequestID != "1" || len(payload.Events) != 1 || payload.Events[0].Method != "notifications/progress" {
		t.Fatalf("streamed payload = %s", streamed.Data)
	}
}
