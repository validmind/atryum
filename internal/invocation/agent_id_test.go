package invocation_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"atryum/internal/auth"
	"atryum/internal/config"
	"atryum/internal/invocation"
	"atryum/internal/invocation/policy"
	"atryum/internal/mcp"
	"atryum/internal/store"

	_ "modernc.org/sqlite"
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

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := store.InitDB(db); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		Defaults:  config.DefaultsConfig{RequestTimeoutSeconds: 5},
		Upstreams: []config.UpstreamConfig{{Name: "demo", Mode: "http", BaseURL: upstream.URL, Enabled: true, TimeoutSeconds: 5}},
	}
	resolver := mcp.NewResolver(store.NewServerRepo(db), cfg)
	if err := resolver.BootstrapIfEmpty(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Rule that auto-approves only the authenticated agent_id; if the
	// service incorrectly matched on request_id (legacy behavior) the rule
	// would NOT match and the call would fall back to manual approval.
	rules := stubRulesStore{rules: []invocation.ApprovalRule{{
		ID: "rule_agent_007", Action: invocation.RuleActionAutoApprove,
		ServerPatterns: []string{"*"}, ToolPatterns: []string{"*"},
		AgentIDPattern: "agent-007", Enabled: true,
	}}}

	svc := invocation.NewService(
		store.NewInvocationRepo(db), store.NewEventRepo(db), resolver,
		mcp.NewHTTPClient(), policy.AlwaysDenyProvider{}, // would deny if the rule didn't match
		5*time.Second, rules,
	)

	ctx := auth.WithIdentity(context.Background(), auth.Identity{AgentID: "agent-007", Issuer: "https://idp.test"})
	resp, err := svc.Invoke(ctx, invocation.CreateInvocationRequest{
		Server: "demo", Tool: "demo_tool", Input: map[string]any{"x": 1},
	})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if resp.Status != invocation.StatusSucceeded {
		t.Fatalf("expected succeeded (rule auto_approve matched on agent_id), got %s", resp.Status)
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
	list, err := svc.List(context.Background(), invocation.InvocationListFilter{AgentID: "agent-007", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if list.Total != 1 || len(list.Items) != 1 || list.Items[0].InvocationID != resp.InvocationID {
		t.Fatalf("expected exactly the auth'd invocation when filtering by agent_id, got %+v", list)
	}
}

func TestInvokeWithoutAuthFallsBackToRequestIDForRules(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"content": []map[string]any{{"type": "text", "text": "ok"}}}})
	}))
	defer upstream.Close()

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := store.InitDB(db); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		Defaults:  config.DefaultsConfig{RequestTimeoutSeconds: 5},
		Upstreams: []config.UpstreamConfig{{Name: "demo", Mode: "http", BaseURL: upstream.URL, Enabled: true, TimeoutSeconds: 5}},
	}
	resolver := mcp.NewResolver(store.NewServerRepo(db), cfg)
	if err := resolver.BootstrapIfEmpty(context.Background()); err != nil {
		t.Fatal(err)
	}

	rules := stubRulesStore{rules: []invocation.ApprovalRule{{
		ID: "rule_legacy", Action: invocation.RuleActionAutoApprove,
		ServerPatterns: []string{"*"}, ToolPatterns: []string{"*"},
		AgentIDPattern: "legacy-request-id", Enabled: true,
	}}}
	svc := invocation.NewService(
		store.NewInvocationRepo(db), store.NewEventRepo(db), resolver,
		mcp.NewHTTPClient(), policy.AlwaysDenyProvider{},
		5*time.Second, rules,
	)

	rid := "legacy-request-id"
	resp, err := svc.Invoke(context.Background(), invocation.CreateInvocationRequest{
		Server: "demo", Tool: "demo_tool", Input: map[string]any{"x": 1}, RequestID: &rid,
	})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if resp.Status != invocation.StatusSucceeded {
		t.Fatalf("expected succeeded via request_id fallback, got %s", resp.Status)
	}
}

func eventsAsJSON(items []invocation.EventResponse) string {
	b, _ := json.Marshal(items)
	return string(b)
}
