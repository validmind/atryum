package invocation_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
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

// Integers beyond float64 precision stay distinct: reusing a key with one
// that differs only past 2^53 is a conflict, not a replay of the old call.
func TestIdempotencyComparesLargeIntegersExactly(t *testing.T) {
	upstream, _ := fakeMCP(t)
	svc := newHookedService(t, newSQLiteTestDB(t), upstream.URL)
	key := "big-int"
	submit := func(n string) (invocation.InvocationResponse, error) {
		return svc.Submit(as("agent-a"), invocation.ExternalSubmitRequest{Source: "ledger", Tool: "post", Input: map[string]any{"account": json.Number(n)}, IdempotencyKey: &key})
	}
	first, err := submit("9007199254740993") // 2^53 + 1
	if err != nil {
		t.Fatal(err)
	}
	if again, err := submit("9007199254740993"); err != nil || again.InvocationID != first.InvocationID {
		t.Fatalf("identical retry: %s %v, want %s", again.InvocationID, err, first.InvocationID)
	}
	if _, err := submit("9007199254740992"); !errors.Is(err, invocation.ErrIdempotencyConflict) {
		t.Fatalf("2^53 after 2^53+1: %v, want ErrIdempotencyConflict", err)
	}
}

// Concurrent identical submits by one agent make one invocation, whichever
// insert wins: the losers get the winner's invocation, not an error.
func TestConcurrentIdenticalSubmits(t *testing.T) {
	upstream, _ := fakeMCP(t)
	db := newSQLiteTestDB(t)
	checkConcurrentIdenticalSubmits(t, newHookedService(t, db, upstream.URL), newHookedService(t, db, upstream.URL))
}

func checkConcurrentIdenticalSubmits(t *testing.T, replicas ...*invocation.Service) {
	t.Helper()
	key := "burst"
	const n = 16
	start := make(chan struct{})
	ids := make(chan string, n)
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(svc *invocation.Service) {
			defer wg.Done()
			<-start
			resp, err := svc.Submit(as("agent-a"), invocation.ExternalSubmitRequest{Source: "harness", Tool: "Bash", Input: map[string]any{"command": "make deploy"}, IdempotencyKey: &key})
			if err != nil {
				errs <- err
				return
			}
			ids <- resp.InvocationID
		}(replicas[i%len(replicas)])
	}
	close(start)
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Errorf("concurrent submit: %v", err)
	}
	seen := map[string]bool{}
	for id := range ids {
		seen[id] = true
	}
	list, err := replicas[0].List(context.Background(), invocation.InvocationListFilter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || list.Total != 1 {
		t.Fatalf("%d distinct invocation IDs returned and %d stored, want 1 and 1", len(seen), list.Total)
	}
}
