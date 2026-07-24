package invocation_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/validmind/atryum/internal/auth"
	"github.com/validmind/atryum/internal/config"
	"github.com/validmind/atryum/internal/invocation"
	"github.com/validmind/atryum/internal/invocation/policy"
	"github.com/validmind/atryum/internal/mcp"
	"github.com/validmind/atryum/internal/store"
)

// stubRulesStore returns a fixed rule list so the test can pin the user
// matching dimension we exercise (agent_id from auth context).
type stubRulesStore struct {
	rules []invocation.ApprovalRule
}

func (s stubRulesStore) ListApprovalRules(_ context.Context) ([]invocation.ApprovalRule, error) {
	return s.rules, nil
}

func TestInvokeUsesAuthenticatedAgentIDForRulesAndEvents(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"content": []map[string]any{{"type": "text", "text": "ok"}}}})
	}))
	defer upstream.Close()

	db := newSQLiteTestDB(t)
	cfg := config.Config{
		Defaults:  config.DefaultsConfig{RequestTimeoutSeconds: 5},
		Upstreams: []config.UpstreamConfig{{Name: "demo", Mode: "http", BaseURL: upstream.URL, Enabled: true, TimeoutSeconds: 5}},
	}
	resolver := mcp.NewResolver(store.NewServerRepo(db), cfg)
	if err := resolver.BootstrapIfEmpty(context.Background()); err != nil {
		t.Fatal(err)
	}

	rules := stubRulesStore{rules: []invocation.ApprovalRule{{
		ID: "rule_auto_approve", Action: invocation.RuleActionAutoApprove,
		ServerPatterns: []string{"*"}, ToolPatterns: []string{"*"},
		Enabled: true,
	}}}
	// The stub decides which rule matches, but the id it hands back is still
	// written to invocations.matched_rule_id, which carries a FK to
	// approval_rules(id). Auto-approved invocations now persist that id, so the
	// row has to exist for the write to succeed.
	if err := store.NewRulesRepo(db).Create(context.Background(), store.Rule{
		ID: "rule_auto_approve", Action: string(invocation.RuleActionAutoApprove),
		ServerPatterns: []string{"*"}, ToolPatterns: []string{"*"},
		Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	svc := invocation.NewService(
		store.NewInvocationRepo(db), store.NewEventRepo(db), resolver,
		mcp.NewHTTPClient(), policy.AlwaysDenyProvider{}, // would deny if the rule didn't match
		5*time.Second, rules, nil, nil, nil,
	)

	ctx := auth.WithIdentity(context.Background(), auth.Identity{AgentID: "agent-007", Issuer: "https://idp.test"})
	resp, err := svc.Invoke(ctx, invocation.CreateInvocationRequest{
		Server: "demo", Tool: "demo_tool", Input: map[string]any{"x": 1},
	})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if resp.Status != invocation.StatusSucceeded {
		t.Fatalf("expected succeeded (rule auto_approve), got %s", resp.Status)
	}
	if resp.Approval == nil || resp.Approval.Status != "auto_approved" {
		t.Fatalf("expected auto_approved approval, got %#v", resp.Approval)
	}

	events, err := svc.Events(context.Background(), resp.InvocationID, invocation.EventListFilter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	var sawAgentInReceived bool
	for _, e := range events.Items {
		if e.Type != "invocation.received" {
			continue
		}
		if strings.Contains(string(e.Data), `"agent_id":"agent-007"`) {
			sawAgentInReceived = true
		}
	}
	if !sawAgentInReceived {
		t.Fatalf("expected invocation.received event to record agent_id; events: %s", eventsAsJSON(events.Items))
	}

	// The invocations row itself should carry the agent_id (column added in
	// migration 006), and the column should be filterable.
	got, err := svc.Get(context.Background(), resp.InvocationID)
	if err != nil {
		t.Fatal(err)
	}
	if got.AgentID == nil || *got.AgentID != "agent-007" {
		t.Fatalf("expected invocation.agent_id=agent-007, got %v", got.AgentID)
	}
	list, err := svc.List(context.Background(), invocation.InvocationListFilter{AgentIDs: []string{"agent-007"}, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if list.Total != 1 || len(list.Items) != 1 || list.Items[0].InvocationID != resp.InvocationID {
		t.Fatalf("expected exactly the auth'd invocation when filtering by agent_id, got %+v", list)
	}
}

// Anonymous (no [[auth]]) callers should still surface MCP clientInfo on
// the invocation row when the API layer passes it through.
func TestInvokeWithoutAgentIDPersistsClientInfoFromRequest(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"content": []map[string]any{{"type": "text", "text": "ok"}}}})
	}))
	defer upstream.Close()

	db := newSQLiteTestDB(t)
	cfg := config.Config{
		Defaults:  config.DefaultsConfig{RequestTimeoutSeconds: 5},
		Upstreams: []config.UpstreamConfig{{Name: "demo", Mode: "http", BaseURL: upstream.URL, Enabled: true, TimeoutSeconds: 5}},
	}
	resolver := mcp.NewResolver(store.NewServerRepo(db), cfg)
	if err := resolver.BootstrapIfEmpty(context.Background()); err != nil {
		t.Fatal(err)
	}
	svc := invocation.NewService(
		store.NewInvocationRepo(db), store.NewEventRepo(db), resolver,
		mcp.NewHTTPClient(), policy.AlwaysApproveProvider{},
		5*time.Second, nil, nil, nil, nil,
	)

	resp, err := svc.Invoke(context.Background(), invocation.CreateInvocationRequest{
		Server: "demo", Tool: "demo_tool", Input: map[string]any{"x": 1},
		ClientName: "amp", ClientVersion: "0.0.1234",
	})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if resp.AgentID != nil {
		t.Fatalf("expected no agent_id, got %v", resp.AgentID)
	}
	if resp.AgentClientName == nil || *resp.AgentClientName != "amp" {
		t.Fatalf("AgentClientName = %v, want amp", resp.AgentClientName)
	}
	if resp.AgentClientVersion == nil || *resp.AgentClientVersion != "0.0.1234" {
		t.Fatalf("AgentClientVersion = %v, want 0.0.1234", resp.AgentClientVersion)
	}

	got, err := svc.Get(context.Background(), resp.InvocationID)
	if err != nil {
		t.Fatal(err)
	}
	if got.AgentClientName == nil || *got.AgentClientName != "amp" {
		t.Fatalf("Get AgentClientName = %v, want amp", got.AgentClientName)
	}
}

// External (non-MCP) callers can still self-declare via `agent_id` when
// Atryum is running without auth. When OAuth middleware attaches a verified
// identity to the context, that identity wins over the request body.
func TestExternalSubmitUsesSelfDeclaredAgentIDWhenNoAuthIdentity(t *testing.T) {
	db := newSQLiteTestDB(t)
	svc := invocation.NewService(
		store.NewInvocationRepo(db), store.NewEventRepo(db), nil,
		nil, policy.AlwaysApproveProvider{},
		5*time.Second, nil, nil, nil, nil,
	)

	// No auth identity in context — self-declared agent_id should be used.
	resp, err := svc.Submit(context.Background(), invocation.ExternalSubmitRequest{
		Source:  "amp",
		Tool:    "bash",
		Input:   map[string]any{"cmd": "ls"},
		AgentID: "amp-local",
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if resp.AgentID == nil || *resp.AgentID != "amp-local" {
		t.Fatalf("expected agent_id=amp-local from body, got %v", resp.AgentID)
	}

	// Verified OAuth identity in context wins over the body, so a hostile
	// caller can't claim someone else's agent_id when auth is enforced.
	ctx := auth.WithIdentity(context.Background(), auth.Identity{
		AgentID: "verified-007", Issuer: "https://idp.test",
	})
	resp2, err := svc.Submit(ctx, invocation.ExternalSubmitRequest{
		Source:  "amp",
		Tool:    "bash",
		Input:   map[string]any{"cmd": "ls"},
		AgentID: "spoofed-id",
	})
	if err != nil {
		t.Fatalf("Submit with auth: %v", err)
	}
	if resp2.AgentID == nil || *resp2.AgentID != "verified-007" {
		t.Fatalf("expected verified agent_id to win over body; got %v", resp2.AgentID)
	}
}

func eventsAsJSON(items []invocation.EventResponse) string {
	b, _ := json.Marshal(items)
	return string(b)
}
