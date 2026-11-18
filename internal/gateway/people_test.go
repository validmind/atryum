package gateway

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/validmind/atryum/internal/invocation"
	"github.com/validmind/atryum/internal/store"

	_ "modernc.org/sqlite"
)

// These tests run the gateway against the real invocation service on SQLite:
// one gateway agent serves every person, and models reuse call IDs (call_0
// on every turn), so what one person's call ID finds must be theirs.

func newRealHandler(t *testing.T, decide invocation.Decider) (*Handler, *invocation.Service) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if err := store.InitDB(db); err != nil {
		t.Fatal(err)
	}
	svc := invocation.NewService(store.NewInvocationRepo(db), store.NewEventRepo(db), nil, nil, nil, 5*time.Second, nil, nil, nil, nil)
	svc.SetDecider(decide)
	return New(svc, Options{}), svc
}

// byUser allows the people in allow and denies everyone else.
func byUser(allow ...string) invocation.Decider {
	return func(_ context.Context, call invocation.ToolCall) (invocation.Decision, error) {
		for _, u := range allow {
			if call.Asserted != nil && call.Asserted.UserID == u {
				return invocation.Decision{Verdict: invocation.VerdictAllow, Reason: u + " may"}, nil
			}
		}
		return invocation.Decision{Verdict: invocation.VerdictDeny, Reason: "not this person"}, nil
	}
}

func callFor(user, session, tool, input string) string {
	return `{"format":"anthropic","response":{"type":"message","role":"assistant","content":[{"type":"tool_use","id":"call_0","name":"` + tool + `","input":` + input + `}],"stop_reason":"tool_use"},"user":{"id":"` + user + `"},"session_id":"` + session + `"}`
}

func calls(t *testing.T, svc *invocation.Service) []invocation.InvocationResponse {
	t.Helper()
	list, err := svc.List(context.Background(), invocation.InvocationListFilter{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	return list.Items
}

func TestAReusedCallIDIsDecidedForEachPerson(t *testing.T) {
	h, svc := newRealHandler(t, byUser("alice"))
	_, body := post(t, h, &gatewayKey, "/api/v1/gateway/llm/output", callFor("alice", "s-1", "refund_payment", `{"id":7}`))
	if c := firstCall(t, body); c["verdict"] != VerdictAllow {
		t.Fatalf("alice: %v", c)
	}
	// Bob's model proposes the identical call under the same call ID while
	// alice's is still running. It is bob's call: decided for bob, recorded.
	_, body = post(t, h, &gatewayKey, "/api/v1/gateway/llm/output", callFor("bob", "s-2", "refund_payment", `{"id":7}`))
	if c := firstCall(t, body); c["verdict"] != VerdictDeny || c["reason"] != "not this person" {
		t.Fatalf("bob got alice's decision: %v", c)
	}
	if got := calls(t, svc); len(got) != 2 {
		t.Fatalf("recorded %d calls, want 2 (one each)", len(got))
	}
}

func TestAToolResultCompletesThePersonsOwnCall(t *testing.T) {
	h, svc := newRealHandler(t, byUser("alice", "bob"))
	post(t, h, &gatewayKey, "/api/v1/gateway/llm/output", callFor("alice", "s-1", "read_file", `{"path":"a"}`))
	post(t, h, &gatewayKey, "/api/v1/gateway/llm/output", callFor("bob", "s-2", "transfer", `{"to":"x"}`))
	// Alice's agent sends her result for call_0.
	in := `{"format":"anthropic","request":{"model":"m","messages":[` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"call_0","name":"read_file","input":{"path":"a"}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_0","content":"ALICE FILE"}]}]},` +
		`"user":{"id":"alice"},"session_id":"s-1"}`
	post(t, h, &gatewayKey, "/api/v1/gateway/llm/input", in)
	for _, inv := range calls(t, svc) {
		switch inv.ToolName {
		case "read_file":
			if inv.Status != invocation.StatusSucceeded {
				t.Errorf("alice's read_file is %s, want succeeded", inv.Status)
			}
		case "transfer":
			if inv.Status != invocation.StatusExecuting {
				t.Errorf("bob's transfer is %s, want still executing (alice's result is not his)", inv.Status)
			}
		}
	}
}

func TestAnAllowAfterTheDeadlineIsNotMarkedRunning(t *testing.T) {
	var mu sync.Mutex
	decided := make(chan struct{})
	h, svc := newRealHandler(t, func(context.Context, invocation.ToolCall) (invocation.Decision, error) {
		time.Sleep(700 * time.Millisecond) // past the 500 ms the gateway waits
		mu.Lock()
		defer mu.Unlock()
		defer close(decided)
		return invocation.Decision{Verdict: invocation.VerdictAllow, Reason: "late"}, nil
	})
	_, body := post(t, h, &gatewayKey, "/api/v1/gateway/llm/output", strings.TrimSuffix(callFor("alice", "s-1", "refund_payment", `{"id":7}`), "}")+`,"deadline_ms":800}`)
	if c := firstCall(t, body); c["verdict"] != VerdictDeny || c["code"] != CodeUnavailable {
		t.Fatalf("got %v, want deny aa_unavailable", c)
	}
	<-decided
	time.Sleep(200 * time.Millisecond) // let the late decision be recorded
	got := calls(t, svc)
	if len(got) != 1 || got[0].Status == invocation.StatusExecuting {
		t.Fatalf("the call the agent was told it may not run is %v, want approved, not executing", got)
	}
}
