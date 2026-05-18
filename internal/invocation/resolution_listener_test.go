package invocation_test

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"atryum/internal/invocation"
	"atryum/internal/invocation/policy"
	"atryum/internal/store"

	_ "modernc.org/sqlite"
)

// newSubmitOnlyService builds a service with no resolver/upstream client (we
// only exercise the Submit + approve/deny paths, which don't dial out).
func newSubmitOnlyService(t *testing.T) (*invocation.Service, *store.RulesRepo) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.InitDB(db); err != nil {
		t.Fatal(err)
	}
	rules := store.NewRulesRepo(db)
	svc := invocation.NewService(
		store.NewInvocationRepo(db),
		store.NewEventRepo(db),
		nil, // resolver — unused on Submit path
		nil, // upstreamClient — unused on Submit path
		policy.ManualApprovalProvider{},
		5*time.Second,
		rules,
	)
	return svc, rules
}

type listenerSpy struct {
	mu  sync.Mutex
	got []invocation.Invocation
}

func (l *listenerSpy) OnResolution(_ context.Context, inv invocation.Invocation) error {
	l.mu.Lock()
	l.got = append(l.got, inv)
	l.mu.Unlock()
	return nil
}

func (l *listenerSpy) snapshot() []invocation.Invocation {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]invocation.Invocation, len(l.got))
	copy(out, l.got)
	return out
}

func TestResolutionListener_FiresOnHumanApprove(t *testing.T) {
	svc, _ := newSubmitOnlyService(t)
	spy := &listenerSpy{}
	svc.AddResolutionListener(spy)

	resp, err := svc.Submit(context.Background(), invocation.ExternalSubmitRequest{
		Source:               "PR Reviewer",
		Tool:                 "read",
		Input:                map[string]any{"path": "/x"},
		ClaudeSessionID:      "sess_xyz",
		ClaudeToolUseEventID: "toolu_1",
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if resp.Status != invocation.StatusPendingApproval {
		t.Fatalf("expected pending_approval, got %s", resp.Status)
	}
	// No listener call yet — only fires on resolution.
	if got := spy.snapshot(); len(got) != 0 {
		t.Fatalf("listener called before resolution: %d", len(got))
	}

	if err := svc.Approve(context.Background(), resp.InvocationID); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	got := spy.snapshot()
	if len(got) != 1 {
		t.Fatalf("listener calls = %d, want 1", len(got))
	}
	if got[0].Status != invocation.StatusApproved {
		t.Errorf("listener saw status %s, want approved", got[0].Status)
	}
	if got[0].ClaudeSessionID == nil || *got[0].ClaudeSessionID != "sess_xyz" {
		t.Errorf("listener did not see claude_session_id: %+v", got[0].ClaudeSessionID)
	}
	if got[0].ClaudeToolUseEventID == nil || *got[0].ClaudeToolUseEventID != "toolu_1" {
		t.Errorf("listener did not see claude_tool_use_event_id: %+v", got[0].ClaudeToolUseEventID)
	}
}

func TestResolutionListener_FiresOnHumanDenyWithMessage(t *testing.T) {
	svc, _ := newSubmitOnlyService(t)
	spy := &listenerSpy{}
	svc.AddResolutionListener(spy)

	resp, err := svc.Submit(context.Background(), invocation.ExternalSubmitRequest{
		Source:               "PR Reviewer",
		Tool:                 "write",
		Input:                map[string]any{},
		ClaudeSessionID:      "sess_xyz",
		ClaudeToolUseEventID: "toolu_2",
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if err := svc.Deny(context.Background(), resp.InvocationID, "use staging instead"); err != nil {
		t.Fatalf("Deny: %v", err)
	}
	got := spy.snapshot()
	if len(got) != 1 {
		t.Fatalf("listener calls = %d", len(got))
	}
	if got[0].Status != invocation.StatusDenied {
		t.Errorf("status = %s", got[0].Status)
	}
	if got[0].Approval == nil || got[0].Approval.Reason == nil || *got[0].Approval.Reason != "use staging instead" {
		t.Errorf("expected reason 'use staging instead', got %+v", got[0].Approval)
	}
}

func TestResolutionListener_FiresOnAutoApproveRule(t *testing.T) {
	svc, rules := newSubmitOnlyService(t)
	spy := &listenerSpy{}
	svc.AddResolutionListener(spy)

	if err := rules.Create(context.Background(), store.Rule{
		ID:             "rule_auto",
		Action:         invocation.RuleActionAutoApprove,
		ServerPatterns: []string{"PR Reviewer"},
		ToolPatterns:   []string{"read"},
		UserPattern:    "*",
		Enabled:        true,
	}); err != nil {
		t.Fatalf("create rule: %v", err)
	}

	resp, err := svc.Submit(context.Background(), invocation.ExternalSubmitRequest{
		Source:               "PR Reviewer",
		Tool:                 "read",
		Input:                map[string]any{},
		ClaudeSessionID:      "sess_xyz",
		ClaudeToolUseEventID: "toolu_3",
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if resp.Status != invocation.StatusApproved {
		t.Fatalf("expected approved (auto), got %s", resp.Status)
	}
	got := spy.snapshot()
	if len(got) != 1 {
		t.Fatalf("listener calls = %d, want 1", len(got))
	}
	if got[0].Status != invocation.StatusApproved {
		t.Errorf("status = %s", got[0].Status)
	}
}

func TestResolutionListener_RuleScopedByAgentName(t *testing.T) {
	svc, rules := newSubmitOnlyService(t)

	// Auto-approve only for "PR Reviewer" agent.
	if err := rules.Create(context.Background(), store.Rule{
		ID:             "rule_scoped",
		Action:         invocation.RuleActionAutoApprove,
		ServerPatterns: []string{"PR Reviewer"},
		ToolPatterns:   []string{"read"},
		UserPattern:    "*",
		Enabled:        true,
	}); err != nil {
		t.Fatalf("create rule: %v", err)
	}

	prResp, err := svc.Submit(context.Background(), invocation.ExternalSubmitRequest{
		Source: "PR Reviewer", Tool: "read", Input: map[string]any{},
	})
	if err != nil {
		t.Fatalf("Submit PR: %v", err)
	}
	if prResp.Status != invocation.StatusApproved {
		t.Errorf("PR Reviewer should auto-approve, got %s", prResp.Status)
	}

	calResp, err := svc.Submit(context.Background(), invocation.ExternalSubmitRequest{
		Source: "Calendar Manager", Tool: "read", Input: map[string]any{},
	})
	if err != nil {
		t.Fatalf("Submit Calendar: %v", err)
	}
	if calResp.Status != invocation.StatusPendingApproval {
		t.Errorf("Calendar Manager should fall to manual, got %s", calResp.Status)
	}
}
