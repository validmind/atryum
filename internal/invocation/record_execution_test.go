package invocation_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/validmind/atryum/internal/auth"
	"github.com/validmind/atryum/internal/invocation"
	"github.com/validmind/atryum/internal/store"

	_ "modernc.org/sqlite"
)

// newRecordExecutionService builds a service wired to a fresh in-memory DB
// with no rules, resolver, or upstream client — external Submit/RecordExecution
// flows don't need them.
func newRecordExecutionService(t *testing.T) *invocation.Service {
	t.Helper()
	db := newSQLiteTestDB(t)
	return invocation.NewService(store.NewInvocationRepo(db), store.NewEventRepo(db), nil, nil, nil, 5*time.Second, nil, nil, nil, nil)
}

// submitExternal creates an external invocation (which lands in
// pending_approval since no rules are configured) and returns its ID.
func submitExternal(t *testing.T, svc *invocation.Service, agentID string) string {
	t.Helper()
	resp, err := svc.Submit(context.Background(), invocation.ExternalSubmitRequest{
		Source:  "amp",
		Tool:    "bash",
		Input:   map[string]any{"cmd": "ls"},
		AgentID: agentID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != invocation.StatusPendingApproval {
		t.Fatalf("submit status = %q, want pending_approval", resp.Status)
	}
	return resp.InvocationID
}

func getInvocation(t *testing.T, svc *invocation.Service, id string) invocation.InvocationResponse {
	t.Helper()
	resp, err := svc.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func countEvents(t *testing.T, svc *invocation.Service, id string, eventType string) int {
	t.Helper()
	events, err := svc.Events(context.Background(), id, invocation.EventListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, evt := range events.Items {
		if evt.Type == eventType {
			n++
		}
	}
	return n
}

func TestRecordExecutionRejectsUnapprovedInvocation(t *testing.T) {
	for _, execStatus := range []string{"running", "completed", "failed"} {
		t.Run(execStatus, func(t *testing.T) {
			svc := newRecordExecutionService(t)
			id := submitExternal(t, svc, "")

			_, err := svc.RecordExecution(context.Background(), id, invocation.ExternalExecutionUpdate{
				ExecutionStatus: execStatus,
				Result:          json.RawMessage(`{"forged":true}`),
			})
			if !errors.Is(err, invocation.ErrInvalidTransition) {
				t.Fatalf("err = %v, want ErrInvalidTransition", err)
			}

			got := getInvocation(t, svc, id)
			if got.Status != invocation.StatusPendingApproval {
				t.Fatalf("status = %q, want pending_approval to be preserved", got.Status)
			}
			if len(got.Result) != 0 {
				t.Fatalf("result = %s, want no result recorded", got.Result)
			}
			if n := countEvents(t, svc, id, "invocation.succeeded"); n != 0 {
				t.Fatalf("invocation.succeeded events = %d, want 0", n)
			}
			if n := countEvents(t, svc, id, "invocation.failed"); n != 0 {
				t.Fatalf("invocation.failed events = %d, want 0", n)
			}
		})
	}
}

func TestRecordExecutionRejectsOverwritingHumanDenial(t *testing.T) {
	svc := newRecordExecutionService(t)
	id := submitExternal(t, svc, "")
	if err := svc.Deny(context.Background(), id, "not on my watch", ""); err != nil {
		t.Fatal(err)
	}

	for _, execStatus := range []string{"running", "completed", "failed", "cancelled"} {
		_, err := svc.RecordExecution(context.Background(), id, invocation.ExternalExecutionUpdate{
			ExecutionStatus: execStatus,
			Result:          json.RawMessage(`{"forged":true}`),
		})
		if !errors.Is(err, invocation.ErrInvalidTransition) {
			t.Fatalf("%s after denial: err = %v, want ErrInvalidTransition", execStatus, err)
		}
	}

	got := getInvocation(t, svc, id)
	if got.Status != invocation.StatusDenied {
		t.Fatalf("status = %q, want denied to be preserved", got.Status)
	}
}

func TestRecordExecutionApprovedLifecycle(t *testing.T) {
	svc := newRecordExecutionService(t)
	id := submitExternal(t, svc, "")
	if err := svc.Approve(context.Background(), id, ""); err != nil {
		t.Fatal(err)
	}

	resp, err := svc.RecordExecution(context.Background(), id, invocation.ExternalExecutionUpdate{ExecutionStatus: "running"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != invocation.StatusExecuting {
		t.Fatalf("status after running = %q, want executing", resp.Status)
	}

	resp, err = svc.RecordExecution(context.Background(), id, invocation.ExternalExecutionUpdate{
		ExecutionStatus: "completed",
		Result:          json.RawMessage(`{"output":"ok"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != invocation.StatusSucceeded {
		t.Fatalf("status after completed = %q, want succeeded", resp.Status)
	}
	if resp.CompletedAt == nil {
		t.Fatal("completed_at not set")
	}
	if string(resp.Result) != `{"output":"ok"}` {
		t.Fatalf("result = %s", resp.Result)
	}
	if n := countEvents(t, svc, id, "invocation.succeeded"); n != 1 {
		t.Fatalf("invocation.succeeded events = %d, want 1", n)
	}
}

func TestRecordExecutionTerminalStatusesAllowedFromApproved(t *testing.T) {
	// Executors may skip the "running" step and report the outcome directly
	// from approved.
	cases := []struct {
		execStatus string
		want       invocation.Status
	}{
		{"completed", invocation.StatusSucceeded},
		{"failed", invocation.StatusFailed},
		{"cancelled", invocation.StatusCancelled},
	}
	for _, tc := range cases {
		t.Run(tc.execStatus, func(t *testing.T) {
			svc := newRecordExecutionService(t)
			id := submitExternal(t, svc, "")
			if err := svc.Approve(context.Background(), id, ""); err != nil {
				t.Fatal(err)
			}
			resp, err := svc.RecordExecution(context.Background(), id, invocation.ExternalExecutionUpdate{ExecutionStatus: tc.execStatus})
			if err != nil {
				t.Fatal(err)
			}
			if resp.Status != tc.want {
				t.Fatalf("status = %q, want %q", resp.Status, tc.want)
			}
		})
	}
}

func TestRecordExecutionCancelledAllowedFromPendingApproval(t *testing.T) {
	// Abandoning a call that never got approved is legitimate — it only makes
	// the record more conservative.
	svc := newRecordExecutionService(t)
	id := submitExternal(t, svc, "")

	resp, err := svc.RecordExecution(context.Background(), id, invocation.ExternalExecutionUpdate{
		ExecutionStatus: "cancelled",
		Message:         "user aborted the agent run",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != invocation.StatusCancelled {
		t.Fatalf("status = %q, want cancelled", resp.Status)
	}
	if n := countEvents(t, svc, id, "invocation.cancelled"); n != 1 {
		t.Fatalf("invocation.cancelled events = %d, want 1", n)
	}
}

func TestRecordExecutionTerminalRetryIsIdempotent(t *testing.T) {
	svc := newRecordExecutionService(t)
	id := submitExternal(t, svc, "")
	if err := svc.Approve(context.Background(), id, ""); err != nil {
		t.Fatal(err)
	}

	first, err := svc.RecordExecution(context.Background(), id, invocation.ExternalExecutionUpdate{
		ExecutionStatus: "completed",
		Result:          json.RawMessage(`{"attempt":1}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != invocation.StatusSucceeded {
		t.Fatalf("status = %q, want succeeded", first.Status)
	}

	// A retry of the same terminal status succeeds but must not overwrite the
	// recorded outcome or emit a second event.
	retry, err := svc.RecordExecution(context.Background(), id, invocation.ExternalExecutionUpdate{
		ExecutionStatus: "completed",
		Result:          json.RawMessage(`{"attempt":2}`),
	})
	if err != nil {
		t.Fatalf("idempotent retry: %v", err)
	}
	if string(retry.Result) != `{"attempt":1}` {
		t.Fatalf("retry result = %s, want first outcome preserved", retry.Result)
	}
	if n := countEvents(t, svc, id, "invocation.succeeded"); n != 1 {
		t.Fatalf("invocation.succeeded events = %d, want 1", n)
	}
}

func TestRecordExecutionRejectsTerminalOverwrite(t *testing.T) {
	svc := newRecordExecutionService(t)
	id := submitExternal(t, svc, "")
	if err := svc.Approve(context.Background(), id, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RecordExecution(context.Background(), id, invocation.ExternalExecutionUpdate{ExecutionStatus: "completed"}); err != nil {
		t.Fatal(err)
	}

	for _, execStatus := range []string{"running", "failed", "cancelled"} {
		_, err := svc.RecordExecution(context.Background(), id, invocation.ExternalExecutionUpdate{ExecutionStatus: execStatus})
		if !errors.Is(err, invocation.ErrInvalidTransition) {
			t.Fatalf("%s after succeeded: err = %v, want ErrInvalidTransition", execStatus, err)
		}
	}
	if got := getInvocation(t, svc, id); got.Status != invocation.StatusSucceeded {
		t.Fatalf("status = %q, want succeeded to be preserved", got.Status)
	}
}

func TestRecordExecutionEnforcesAgentOwnership(t *testing.T) {
	newApproved := func(t *testing.T, svc *invocation.Service, agentID string) string {
		t.Helper()
		id := submitExternal(t, svc, agentID)
		if err := svc.Approve(context.Background(), id, ""); err != nil {
			t.Fatal(err)
		}
		return id
	}
	update := invocation.ExternalExecutionUpdate{ExecutionStatus: "completed"}

	t.Run("different agent is rejected", func(t *testing.T) {
		svc := newRecordExecutionService(t)
		id := newApproved(t, svc, "agent-a")
		ctx := auth.WithIdentity(context.Background(), auth.Identity{AgentID: "agent-b"})
		_, err := svc.RecordExecution(ctx, id, update)
		if !errors.Is(err, invocation.ErrNotOwner) {
			t.Fatalf("err = %v, want ErrNotOwner", err)
		}
		if got := getInvocation(t, svc, id); got.Status != invocation.StatusApproved {
			t.Fatalf("status = %q, want approved to be preserved", got.Status)
		}
	})

	t.Run("owning agent is allowed", func(t *testing.T) {
		svc := newRecordExecutionService(t)
		id := newApproved(t, svc, "agent-a")
		ctx := auth.WithIdentity(context.Background(), auth.Identity{AgentID: "agent-a"})
		resp, err := svc.RecordExecution(ctx, id, update)
		if err != nil {
			t.Fatal(err)
		}
		if resp.Status != invocation.StatusSucceeded {
			t.Fatalf("status = %q, want succeeded", resp.Status)
		}
	})

	t.Run("server-internal caller without identity is exempt", func(t *testing.T) {
		svc := newRecordExecutionService(t)
		id := newApproved(t, svc, "agent-a")
		resp, err := svc.RecordExecution(context.Background(), id, update)
		if err != nil {
			t.Fatal(err)
		}
		if resp.Status != invocation.StatusSucceeded {
			t.Fatalf("status = %q, want succeeded", resp.Status)
		}
	})

	t.Run("anonymous invocation accepts any identity", func(t *testing.T) {
		svc := newRecordExecutionService(t)
		id := newApproved(t, svc, "")
		ctx := auth.WithIdentity(context.Background(), auth.Identity{AgentID: "agent-b"})
		resp, err := svc.RecordExecution(ctx, id, update)
		if err != nil {
			t.Fatal(err)
		}
		if resp.Status != invocation.StatusSucceeded {
			t.Fatalf("status = %q, want succeeded", resp.Status)
		}
	})
}
