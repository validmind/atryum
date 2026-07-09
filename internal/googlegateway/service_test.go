package googlegateway

import (
	"context"
	"testing"
	"time"

	"atryum/internal/invocation"
)

// fakeGateway is an in-memory InvocationGateway for testing the decision logic
// without the store or approval engine.
type fakeGateway struct {
	submitStatus invocation.Status
	// getStatuses is returned by successive Get calls (for pending -> terminal).
	getStatuses []invocation.Status
	denyReason  string
	gets        int
	lastSubmit  invocation.ExternalSubmitRequest
}

func (f *fakeGateway) Submit(_ context.Context, r invocation.ExternalSubmitRequest) (invocation.InvocationResponse, error) {
	f.lastSubmit = r
	return f.resp(f.submitStatus), nil
}

func (f *fakeGateway) Get(_ context.Context, _ string) (invocation.InvocationResponse, error) {
	st := invocation.StatusPendingApproval
	if f.gets < len(f.getStatuses) {
		st = f.getStatuses[f.gets]
	}
	f.gets++
	return f.resp(st), nil
}

func (f *fakeGateway) resp(st invocation.Status) invocation.InvocationResponse {
	r := invocation.InvocationResponse{InvocationID: "inv_test", Status: st}
	if st == invocation.StatusDenied && f.denyReason != "" {
		reason := f.denyReason
		r.Approval = &invocation.Approval{Status: "denied", Reason: &reason}
	}
	return r
}

func fastCfg() Config {
	return Config{PollInterval: time.Millisecond, DecisionTimeout: 200 * time.Millisecond}
}

func TestDecide_AutoApprove(t *testing.T) {
	svc := NewService(&fakeGateway{submitStatus: invocation.StatusApproved}, fastCfg())
	d := svc.Decide(context.Background(), ToolCall{Tool: "bash", AgentID: "a1"})
	if !d.Allow {
		t.Fatalf("expected allow, got deny: %q", d.Message)
	}
}

func TestDecide_AutoDeny_CarriesReason(t *testing.T) {
	svc := NewService(&fakeGateway{submitStatus: invocation.StatusDenied, denyReason: "no shell in prod"}, fastCfg())
	d := svc.Decide(context.Background(), ToolCall{Tool: "bash"})
	if d.Allow {
		t.Fatal("expected deny")
	}
	if d.Message != "no shell in prod" {
		t.Fatalf("expected rule reason, got %q", d.Message)
	}
}

func TestDecide_PendingThenApproved(t *testing.T) {
	g := &fakeGateway{
		submitStatus: invocation.StatusPendingApproval,
		getStatuses:  []invocation.Status{invocation.StatusPendingApproval, invocation.StatusApproved},
	}
	d := NewService(g, fastCfg()).Decide(context.Background(), ToolCall{Tool: "write_file"})
	if !d.Allow {
		t.Fatalf("expected allow after approval, got deny: %q", d.Message)
	}
}

func TestDecide_PendingTimeout_FailsClosed(t *testing.T) {
	// Never leaves pending -> must fail closed at the decision timeout.
	g := &fakeGateway{submitStatus: invocation.StatusPendingApproval}
	d := NewService(g, Config{PollInterval: time.Millisecond, DecisionTimeout: 20 * time.Millisecond}).
		Decide(context.Background(), ToolCall{Tool: "delete_db"})
	if d.Allow {
		t.Fatal("expected fail-closed deny on timeout")
	}
}

func TestDecide_MCPServerBecomesRuleSource(t *testing.T) {
	g := &fakeGateway{submitStatus: invocation.StatusApproved}
	NewService(g, fastCfg()).Decide(context.Background(), ToolCall{Tool: "search", ServerName: "shortcut"})
	if g.lastSubmit.Source != "shortcut" {
		t.Fatalf("expected MCP server name as rule source, got %q", g.lastSubmit.Source)
	}
}

func TestDecide_IdempotencyKeyFromCallID(t *testing.T) {
	g := &fakeGateway{submitStatus: invocation.StatusApproved}
	NewService(g, fastCfg()).Decide(context.Background(), ToolCall{Tool: "x", CallID: "call-123"})
	if g.lastSubmit.IdempotencyKey == nil || *g.lastSubmit.IdempotencyKey != "call-123" {
		t.Fatalf("expected idempotency key from CallID")
	}
}
