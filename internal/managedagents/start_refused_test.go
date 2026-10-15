package managedagents

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/validmind/atryum/internal/invocation"
	"github.com/validmind/atryum/internal/invocation/policy"
	"github.com/validmind/atryum/internal/store"

	_ "modernc.org/sqlite"
)

// realGateway is Atryum's invocation service on SQLite, with transactions
// wired as pkg/atryum wires them and a decider that approves every call.
func realGateway(t *testing.T, hook invocation.EventHook) *invocation.Service {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if err := store.InitDB(db); err != nil {
		t.Fatal(err)
	}
	svc := invocation.NewService(store.NewInvocationRepo(db), store.NewEventRepo(db), nil, nil, policy.AlwaysApproveProvider{}, time.Second, nil, nil, nil, nil)
	svc.SetTx(func(ctx context.Context, fn func(context.Context, *sql.Tx) error) error {
		return store.InTx(ctx, db, fn)
	})
	svc.SetDecider(func(context.Context, invocation.ToolCall) (invocation.Decision, error) {
		return invocation.Decision{Verdict: invocation.VerdictAllow}, nil
	})
	svc.SetEventHook(hook)
	return svc
}

func results(c *fakeClient) []any {
	var out []any
	for _, s := range c.sentEvents() {
		for _, e := range s.Events {
			out = append(out, e.Fields["result"])
		}
	}
	return out
}

// An embedder that refuses the start (an expired lease) gets a deny sent to
// Claude, never an allow, and the call is recorded as cancelled, not running.
func TestRefusedStartSendsDenyNotAllow(t *testing.T) {
	svc := realGateway(t, func(_ context.Context, _ *sql.Tx, c invocation.Change) error {
		if c.Type == "invocation.executing" {
			return errors.New("lease expired")
		}
		return nil
	})
	c := &fakeClient{}
	w := newTestWatcher(svc, c, newFakeAudit())
	ctx := context.Background()

	w.handleEvent(ctx, toolUseEvent("tool_evt_1", evtAgentToolUse, "Bash", map[string]any{"command": "ls"}))
	w.mu.Lock()
	invID := w.pending["tool_evt_1"].InvocationID
	w.mu.Unlock()
	w.handleEvent(ctx, idleRequiresAction("idle_1", []string{"tool_evt_1"}))

	sent := c.sentEvents()
	if got := results(c); len(got) != 1 || got[0] != "deny" {
		t.Fatalf("results sent to Claude = %v, want exactly one deny", got)
	}
	if msg, _ := sent[0].Events[0].Fields["deny_message"].(string); !strings.Contains(msg, "lease expired") {
		t.Fatalf("deny_message = %q, want the refusal reason", msg)
	}
	if got, err := svc.Get(ctx, invID); err != nil || got.Status != invocation.StatusCancelled {
		t.Fatalf("invocation status = %s (%v), want cancelled", got.Status, err)
	}
	// A replay of the same requires_action re-sends the deny, never an allow.
	w.handleEvent(ctx, idleRequiresAction("idle_1", []string{"tool_evt_1"}))
	for _, r := range results(c) {
		if r != "deny" {
			t.Fatalf("results after replay = %v, want only denies", results(c))
		}
	}
}

// Without a hook, an approved call is recorded running before Claude is
// told to run it, and then allowed as before.
func TestApprovedCallRecordedRunningBeforeAllow(t *testing.T) {
	c := &fakeClient{}
	var sentBeforeRunning = -1
	svc := realGateway(t, func(_ context.Context, _ *sql.Tx, ch invocation.Change) error {
		if ch.Type == "invocation.executing" {
			sentBeforeRunning = len(c.sentEvents())
		}
		return nil
	})
	w := newTestWatcher(svc, c, newFakeAudit())
	ctx := context.Background()
	w.handleEvent(ctx, toolUseEvent("tool_evt_1", evtAgentToolUse, "Bash", nil))
	w.handleEvent(ctx, idleRequiresAction("idle_1", []string{"tool_evt_1"}))
	if sentBeforeRunning != 0 {
		t.Fatalf("%d events were sent before running was recorded", sentBeforeRunning)
	}
	if got := results(c); len(got) != 1 || got[0] != "allow" {
		t.Fatalf("results sent to Claude = %v, want one allow", got)
	}
}

// When the allow fails to send after running was recorded, the replay sends
// it again without recording running twice.
func TestReplayResendsAllowForRunningCall(t *testing.T) {
	var executing int
	svc := realGateway(t, func(_ context.Context, _ *sql.Tx, ch invocation.Change) error {
		if ch.Type == "invocation.executing" {
			executing++
		}
		return nil
	})
	c := &fakeClient{sendErr: errors.New("anthropic unavailable")}
	w := newTestWatcher(svc, c, newFakeAudit())
	ctx := context.Background()
	w.handleEvent(ctx, toolUseEvent("tool_evt_1", evtAgentToolUse, "Bash", nil))
	w.handleEvent(ctx, idleRequiresAction("idle_1", []string{"tool_evt_1"}))
	if len(c.sentEvents()) != 0 || executing != 1 {
		t.Fatalf("sent=%d executing=%d, want 0 and 1", len(c.sentEvents()), executing)
	}
	c.mu.Lock()
	c.sendErr = nil
	c.mu.Unlock()
	w.handleEvent(ctx, idleRequiresAction("idle_1", []string{"tool_evt_1"}))
	if got := results(c); len(got) != 1 || got[0] != "allow" || executing != 1 {
		t.Fatalf("results=%v executing=%d, want one allow and running recorded once", got, executing)
	}
}
