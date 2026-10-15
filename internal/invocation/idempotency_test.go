package invocation_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/validmind/atryum/internal/auth"
	"github.com/validmind/atryum/internal/invocation"
)

func as(agent string) context.Context {
	return auth.WithIdentity(context.Background(), auth.Identity{AgentID: agent})
}

// Idempotency keys are scoped to the caller's agent and bound to the request:
// another agent's key never returns that agent's invocation, and the same
// agent reusing a key for a different request gets a conflict.
func TestIdempotencyKeyScopedToAgentAndRequest(t *testing.T) {
	upstream, calls := fakeMCP(t)
	svc := newHookedService(t, newSQLiteTestDB(t), upstream.URL)
	key := "retry-1"

	invoke := func(ctx context.Context, tool string, input map[string]any) (invocation.InvocationResponse, error) {
		return svc.Invoke(ctx, invocation.CreateInvocationRequest{Server: "bank", Tool: tool, Input: input, IdempotencyKey: &key})
	}
	submit := func(ctx context.Context, agentID, tool string, input map[string]any) (invocation.InvocationResponse, error) {
		return svc.Submit(ctx, invocation.ExternalSubmitRequest{Source: "harness", Tool: tool, Input: input, IdempotencyKey: &key, AgentID: agentID})
	}

	t.Run("invoke", func(t *testing.T) {
		first, err := invoke(as("agent-a"), "pay", map[string]any{"amount": 100, "to": "acme"})
		if err != nil || first.Status != invocation.StatusSucceeded {
			t.Fatalf("first: %+v %v", first, err)
		}
		// Same agent, same request (keys in another order): the original call.
		again, err := invoke(as("agent-a"), "pay", map[string]any{"to": "acme", "amount": 100})
		if err != nil || again.InvocationID != first.InvocationID || calls.Load() != 1 {
			t.Fatalf("retry: %s %v calls=%d, want %s and one upstream call", again.InvocationID, err, calls.Load(), first.InvocationID)
		}
		// Another agent with the same key: its own invocation, nothing of A's.
		other, err := invoke(as("agent-b"), "pay", map[string]any{"amount": 100, "to": "acme"})
		if err != nil || other.InvocationID == first.InvocationID || (other.AgentID != nil && *other.AgentID != "agent-b") {
			t.Fatalf("agent-b got %+v %v, want a separate invocation", other, err)
		}
		// Same agent, different request: a conflict that describes nothing.
		for name, call := range map[string]func() (invocation.InvocationResponse, error){
			"input": func() (invocation.InvocationResponse, error) {
				return invoke(as("agent-a"), "pay", map[string]any{"amount": 900, "to": "acme"})
			},
			"tool": func() (invocation.InvocationResponse, error) {
				return invoke(as("agent-a"), "refund", map[string]any{"amount": 100, "to": "acme"})
			},
			"path": func() (invocation.InvocationResponse, error) {
				return submit(as("agent-a"), "", "pay", map[string]any{"amount": 100, "to": "acme"})
			},
		} {
			resp, err := call()
			if !errors.Is(err, invocation.ErrIdempotencyConflict) || resp.InvocationID != "" || strings.Contains(err.Error(), first.InvocationID) {
				t.Fatalf("different %s: %+v %v, want a bare ErrIdempotencyConflict", name, resp, err)
			}
		}
	})

	t.Run("submit", func(t *testing.T) {
		key = "retry-2"
		first, err := submit(as("agent-a"), "", "Bash", map[string]any{"command": "ls"})
		if err != nil {
			t.Fatal(err)
		}
		if again, err := submit(as("agent-a"), "", "Bash", map[string]any{"command": "ls"}); err != nil || again.InvocationID != first.InvocationID {
			t.Fatalf("retry: %s %v, want %s", again.InvocationID, err, first.InvocationID)
		}
		if other, err := submit(as("agent-b"), "", "Bash", map[string]any{"command": "ls"}); err != nil || other.InvocationID == first.InvocationID {
			t.Fatalf("agent-b: %s %v, want a separate invocation", other.InvocationID, err)
		}
		if _, err := submit(as("agent-a"), "", "Bash", map[string]any{"command": "rm -rf /"}); !errors.Is(err, invocation.ErrIdempotencyConflict) {
			t.Fatalf("different input: %v, want ErrIdempotencyConflict", err)
		}
		// Without auth the declared agent_id scopes the key.
		key = "retry-3"
		anon, err := submit(context.Background(), "harness-a", "Bash", map[string]any{"command": "ls"})
		if err != nil {
			t.Fatal(err)
		}
		if other, err := submit(context.Background(), "harness-b", "Bash", map[string]any{"command": "ls"}); err != nil || other.InvocationID == anon.InvocationID {
			t.Fatalf("harness-b: %s %v, want a separate invocation", other.InvocationID, err)
		}
		if again, err := submit(context.Background(), "harness-a", "Bash", map[string]any{"command": "ls"}); err != nil || again.InvocationID != anon.InvocationID {
			t.Fatalf("harness-a retry: %s %v, want %s", again.InvocationID, err, anon.InvocationID)
		}
	})
}
