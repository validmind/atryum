package invocation_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/validmind/atryum/internal/config"
	"github.com/validmind/atryum/internal/invocation"
	"github.com/validmind/atryum/internal/store"
)

// A catch-all auto_approve rule must not let an agent change its own hook:
// self-protection settles the call before rules are consulted.
func newSelfProtectionService(t *testing.T) *invocation.Service {
	t.Helper()
	db := newSQLiteTestDB(t)
	rulesRepo := store.NewRulesRepo(db)
	if err := rulesRepo.Create(context.Background(), store.Rule{
		ID:             "approve-everything",
		Action:         invocation.RuleActionAutoApprove,
		ServerPatterns: []string{"*"},
		ToolPatterns:   []string{"*"},
		Enabled:        true,
	}); err != nil {
		t.Fatal(err)
	}
	return invocation.NewService(store.NewInvocationRepo(db), store.NewEventRepo(db), nil, nil, nil, 5*time.Second, rulesRepo, nil, nil, nil)
}

func TestSelfProtectionDeniesHookChangesBeforeRules(t *testing.T) {
	service := newSelfProtectionService(t)

	resp, err := service.Submit(context.Background(), invocation.ExternalSubmitRequest{
		Source: "claude-code",
		Tool:   "Bash",
		Input:  map[string]any{"command": "sed -i '' '/atryum/d' ~/.claude/settings.json"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != invocation.StatusDenied {
		t.Fatalf("status = %q, want denied despite the auto_approve rule", resp.Status)
	}
	if resp.Approval == nil || resp.Approval.Status != "self_protection_denied" {
		t.Fatalf("approval = %+v, want self_protection_denied", resp.Approval)
	}
	if resp.MatchedRuleID != nil {
		t.Fatalf("matched rule = %q, want none (rules are never consulted)", *resp.MatchedRuleID)
	}

	ok, err := service.Submit(context.Background(), invocation.ExternalSubmitRequest{
		Source: "claude-code",
		Tool:   "Bash",
		Input:  map[string]any{"command": "go test ./..."},
	})
	if err != nil {
		t.Fatal(err)
	}
	if ok.Status != invocation.StatusApproved {
		t.Fatalf("ordinary call status = %q, want approved by the rule", ok.Status)
	}
}

func TestSelfProtectionCanHoldForAPerson(t *testing.T) {
	service := newSelfProtectionService(t)
	service.SetSelfProtection(invocation.SelfProtection{Enabled: true, Action: invocation.SelfProtectionHuman})

	resp, err := service.Submit(context.Background(), invocation.ExternalSubmitRequest{
		Source: "claude-code",
		Tool:   "Write",
		Input:  map[string]any{"file_path": "/repo/.claude/settings.local.json", "content": `{"disableAllHooks": true}`},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != invocation.StatusPendingApproval {
		t.Fatalf("status = %q, want pending_approval", resp.Status)
	}
	if resp.Approval == nil || resp.Approval.Status != "self_protection_escalated" {
		t.Fatalf("approval = %+v, want self_protection_escalated", resp.Approval)
	}
}

func TestSelfProtectionCanBeTurnedOff(t *testing.T) {
	service := newSelfProtectionService(t)
	service.SetSelfProtection(invocation.SelfProtection{})

	resp, err := service.Submit(context.Background(), invocation.ExternalSubmitRequest{
		Source: "claude-code",
		Tool:   "Bash",
		Input:  map[string]any{"command": "rm -rf ~/.atryum/hooks"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != invocation.StatusApproved {
		t.Fatalf("status = %q, want approved by the rule when self-protection is off", resp.Status)
	}
}

// The MCP proxy path is checked too: a filesystem-style MCP server must not
// become a way around the hook, even under an always-approve global policy.
func TestSelfProtectionDeniesProxyCallsBeforeTheUpstream(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"ok"}]}}`))
	}))
	defer upstream.Close()

	service := newTestService(t, config.Config{
		Upstreams: []config.UpstreamConfig{{Name: "fs", Mode: "http", BaseURL: upstream.URL, Enabled: true, TimeoutSeconds: 5}},
	})
	resp, err := service.Invoke(context.Background(), invocation.CreateInvocationRequest{
		Server: "fs",
		Tool:   "write_file",
		Input:  map[string]any{"path": "/home/u/.claude/settings.json", "content": "{}"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != invocation.StatusDenied {
		t.Fatalf("status = %q, want denied", resp.Status)
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("upstream was called %d times; a denied call must never reach it", n)
	}
}
