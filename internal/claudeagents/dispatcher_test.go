package claudeagents

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"atryum/internal/invocation"
)

type capturedConfirmation struct {
	path string
	body toolConfirmationEvent
}

func newCapturingClient(t *testing.T, sink *[]capturedConfirmation, mu *sync.Mutex) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var env sendEventsRequest
		_ = json.NewDecoder(r.Body).Decode(&env)
		var body toolConfirmationEvent
		if len(env.Events) > 0 {
			body = env.Events[0]
		}
		mu.Lock()
		*sink = append(*sink, capturedConfirmation{path: r.URL.Path, body: body})
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":"evt_ack"}`)
	}))
	t.Cleanup(srv.Close)
	c, err := New(Options{APIKey: "k", BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func sptr(s string) *string { return &s }

func TestDispatcher_IgnoresInvocationsWithoutClaudeLinkage(t *testing.T) {
	var got []capturedConfirmation
	var mu sync.Mutex
	d := NewDispatcher(newCapturingClient(t, &got, &mu))

	if err := d.OnResolution(context.Background(), invocation.Invocation{
		InvocationID: "inv_1", Status: invocation.StatusApproved,
	}); err != nil {
		t.Fatalf("OnResolution: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("dispatcher posted %d confirmations for unrelated invocation", len(got))
	}
}

func TestDispatcher_PostsAllowOnApprove(t *testing.T) {
	var got []capturedConfirmation
	var mu sync.Mutex
	d := NewDispatcher(newCapturingClient(t, &got, &mu))

	if err := d.OnResolution(context.Background(), invocation.Invocation{
		InvocationID:         "inv_1",
		Status:               invocation.StatusApproved,
		ClaudeSessionID:      sptr("sess_xyz"),
		ClaudeToolUseEventID: sptr("toolu_1"),
	}); err != nil {
		t.Fatalf("OnResolution: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d confirmations, want 1", len(got))
	}
	if got[0].path != "/v1/sessions/sess_xyz/events" {
		t.Errorf("path = %q", got[0].path)
	}
	if got[0].body.Type != "user.tool_confirmation" || got[0].body.Result != ResultAllow {
		t.Errorf("body = %+v", got[0].body)
	}
	if got[0].body.DenyMessage != "" {
		t.Errorf("approve must not carry deny_message: %q", got[0].body.DenyMessage)
	}
}

func TestDispatcher_PostsDenyWithMessage(t *testing.T) {
	var got []capturedConfirmation
	var mu sync.Mutex
	d := NewDispatcher(newCapturingClient(t, &got, &mu))

	if err := d.OnResolution(context.Background(), invocation.Invocation{
		InvocationID:         "inv_1",
		Status:               invocation.StatusDenied,
		Approval:             &invocation.Approval{Status: "denied", Reason: sptr("don't touch prod")},
		ClaudeSessionID:      sptr("sess_xyz"),
		ClaudeToolUseEventID: sptr("toolu_1"),
	}); err != nil {
		t.Fatalf("OnResolution: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d confirmations", len(got))
	}
	if got[0].body.Result != ResultDeny || got[0].body.DenyMessage != "don't touch prod" {
		t.Errorf("body = %+v", got[0].body)
	}
}

func TestDispatcher_HumanSentinelReasonStripped(t *testing.T) {
	// When the human denies without supplying a custom reason, we set
	// Reason="human" internally; the dispatcher must not leak that to the API.
	var got []capturedConfirmation
	var mu sync.Mutex
	d := NewDispatcher(newCapturingClient(t, &got, &mu))

	if err := d.OnResolution(context.Background(), invocation.Invocation{
		InvocationID:         "inv_1",
		Status:               invocation.StatusDenied,
		Approval:             &invocation.Approval{Status: "denied", Reason: sptr("human")},
		ClaudeSessionID:      sptr("sess_xyz"),
		ClaudeToolUseEventID: sptr("toolu_1"),
	}); err != nil {
		t.Fatalf("OnResolution: %v", err)
	}
	if got[0].body.DenyMessage != "" {
		t.Errorf("expected empty deny_message for human sentinel, got %q", got[0].body.DenyMessage)
	}
}

func TestDispatcher_NonTerminalStatusDoesNothing(t *testing.T) {
	var got []capturedConfirmation
	var mu sync.Mutex
	d := NewDispatcher(newCapturingClient(t, &got, &mu))

	if err := d.OnResolution(context.Background(), invocation.Invocation{
		InvocationID:         "inv_1",
		Status:               invocation.StatusPendingApproval,
		ClaudeSessionID:      sptr("sess_xyz"),
		ClaudeToolUseEventID: sptr("toolu_1"),
	}); err != nil {
		t.Fatalf("OnResolution: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("posted %d confirmations for non-terminal status", len(got))
	}
}
