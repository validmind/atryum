package invocation_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

func TestInvokeAndInspectHTTP(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		switch body["method"] {
		case "initialize":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      body["id"],
				"result":  map[string]any{"serverInfo": map[string]any{"name": "fake-github", "version": "0.1.0"}, "capabilities": map[string]any{}},
			})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/call":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"content": []map[string]any{{"type": "text", "text": "ok"}}}})
		default:
			t.Errorf("unexpected method: %v", body["method"])
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer upstream.Close()

	service := newTestService(t, config.Config{
		Defaults:  config.DefaultsConfig{RequestTimeoutSeconds: 5},
		Upstreams: []config.UpstreamConfig{{Name: "github", Mode: "http", BaseURL: upstream.URL, Enabled: true, TimeoutSeconds: 5}},
	})
	go approveNextInvocation(t, service, 50*time.Millisecond)
	assertInvokeLifecycle(t, service, "github", "github.issue.get")
}

func TestInvokeAndInspectStdio(t *testing.T) {
	script := filepath.Join(t.TempDir(), "fake-mcp.sh")
	content := `#!/usr/bin/env bash
set -euo pipefail
while IFS= read -r line; do
  if [[ -z "$line" ]]; then
    continue
  fi
  if [[ "$line" == *'"method":"initialize"'* ]]; then
    printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"serverInfo":{"name":"fake-shortcut","version":"0.1.0"},"capabilities":{}}}'
  elif [[ "$line" == *'"method":"notifications/initialized"'* ]]; then
    continue
  elif [[ "$line" == *'"method":"tools/call"'* ]]; then
    printf '%s\n' '{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"ok"}]}}'
    exit 0
  fi
done
`
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}

	service := newTestService(t, config.Config{
		Defaults:  config.DefaultsConfig{RequestTimeoutSeconds: 5},
		Upstreams: []config.UpstreamConfig{{Name: "shortcut", Mode: "stdio", Command: script, Enabled: true, TimeoutSeconds: 5}},
	})
	go approveNextInvocation(t, service, 50*time.Millisecond)
	assertInvokeLifecycle(t, service, "shortcut", "stories-search")
}

func TestBusinessToolErrorMarksInvocationFailed(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"result": map[string]any{
				"content": []map[string]any{{"type": "text", "text": "Tool stories-does-not-exist not found"}},
			},
		})
	}))
	defer upstream.Close()

	service := newTestService(t, config.Config{
		Defaults:  config.DefaultsConfig{RequestTimeoutSeconds: 5},
		Upstreams: []config.UpstreamConfig{{Name: "shortcut", Mode: "http", BaseURL: upstream.URL, Enabled: true, TimeoutSeconds: 5}},
	})
	go approveNextInvocation(t, service, 50*time.Millisecond)
	resp, err := service.Invoke(context.Background(), invocation.CreateInvocationRequest{Server: "shortcut", Tool: "stories-does-not-exist", Input: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != invocation.StatusFailed {
		t.Fatalf("expected failed status, got %s", resp.Status)
	}
	if len(resp.Error) == 0 {
		t.Fatal("expected error payload")
	}
}

func TestResolverBootstrapsServersFromConfigWhenDBEmpty(t *testing.T) {
	db := newSQLiteTestDB(t)
	repo := store.NewServerRepo(db)
	resolver := mcp.NewResolver(repo, config.Config{Upstreams: []config.UpstreamConfig{{Name: "bootstrap", Mode: "http", BaseURL: "http://example.com", Enabled: true, TimeoutSeconds: 7}}})
	if err := resolver.BootstrapIfEmpty(context.Background()); err != nil {
		t.Fatal(err)
	}
	upstream, err := repo.GetServer(context.Background(), "bootstrap")
	if err != nil {
		t.Fatal(err)
	}
	if upstream.BaseURL != "http://example.com" {
		t.Fatalf("unexpected base url %q", upstream.BaseURL)
	}
	if upstream.Status.ConnectionStatus != mcp.ConnectionStatusUnknown {
		t.Fatalf("unexpected initial connection status %q", upstream.Status.ConnectionStatus)
	}
}

func TestServerStatusPersistsAfterUpdate(t *testing.T) {
	db := newSQLiteTestDB(t)
	repo := store.NewServerRepo(db)
	upstream := mcp.FromConfig(config.UpstreamConfig{Name: "status-demo", Mode: "http", BaseURL: "http://example.com", Enabled: true, TimeoutSeconds: 5})
	if err := repo.CreateServer(context.Background(), upstream); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	errSummary := "unauthorized"
	action := "refresh credentials"
	status := mcp.ServerStatus{
		AuthType:         mcp.AuthTypeBearer,
		ConnectionStatus: mcp.ConnectionStatusNeedsAttention,
		AuthStatus:       mcp.AuthStatusInvalid,
		ReauthNeeded:     true,
		LastCheckedAt:    &now,
		LastCheckOK:      false,
		LastErrorSummary: &errSummary,
		ActionRequired:   &action,
	}
	if err := repo.UpdateServerStatus(context.Background(), "status-demo", status); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.GetServerAny(context.Background(), "status-demo")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status.AuthStatus != mcp.AuthStatusInvalid {
		t.Fatalf("unexpected auth status %q", stored.Status.AuthStatus)
	}
	if !stored.Status.ReauthNeeded {
		t.Fatal("expected reauth needed")
	}
	if stored.Status.LastErrorSummary == nil || *stored.Status.LastErrorSummary != errSummary {
		t.Fatal("expected last error summary")
	}
	if stored.Status.ActionRequired == nil || *stored.Status.ActionRequired != action {
		t.Fatal("expected action required")
	}
}

func TestSetSummaryPersistsSummaryAndRecordsEvent(t *testing.T) {
	db := newSQLiteTestDB(t)
	invRepo := store.NewInvocationRepo(db)
	eventRepo := store.NewEventRepo(db)
	service := invocation.NewService(invRepo, eventRepo, nil, nil, policy.AlwaysApproveProvider{}, 5*time.Second, nil, nil, nil, nil)
	now := time.Now().UTC()
	inv := invocation.Invocation{
		InvocationID: "inv-summary",
		Tool:         "read_file",
		Upstream:     "demo",
		Status:       invocation.StatusSucceeded,
		Input:        []byte(`{"path":"/tmp/a"}`),
		SubmittedAt:  now,
		CompletedAt:  &now,
	}
	if err := invRepo.Create(context.Background(), inv); err != nil {
		t.Fatal(err)
	}

	resp, err := service.SetSummary(context.Background(), "inv-summary", "Read /tmp/a.")
	if err != nil {
		t.Fatal(err)
	}

	if resp.InvocationID != "inv-summary" || resp.Summary != "Read /tmp/a." {
		t.Fatalf("unexpected response: %+v", resp)
	}
	stored, err := invRepo.Get(context.Background(), "inv-summary")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Summary == nil || *stored.Summary != "Read /tmp/a." {
		t.Fatalf("stored summary = %#v", stored.Summary)
	}
	events, err := service.Events(context.Background(), "inv-summary", invocation.EventListFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(events.Items) != 1 {
		t.Fatalf("expected one event, got %d", len(events.Items))
	}
	if events.Items[0].Type != "invocation.summarized" {
		t.Fatalf("event type = %q", events.Items[0].Type)
	}
	if !jsonContains(events.Items[0].Data, `"summary":"Read /tmp/a."`) {
		t.Fatalf("event payload = %s", string(events.Items[0].Data))
	}
}

func TestSubmitPendingApprovalAutomaticallySummarizesInvocation(t *testing.T) {
	db := newSQLiteTestDB(t)
	invRepo := store.NewInvocationRepo(db)
	eventRepo := store.NewEventRepo(db)
	service := invocation.NewService(invRepo, eventRepo, nil, nil, nil, 5*time.Second, nil, nil, nil, summarySettingsStub{
		orgCUID:         " org_123 ",
		modelConfigCUID: " model_123 ",
	})
	summarizer := &summaryClientStub{summary: "Run shell command ls."}
	service.SetInvocationSummarizer(summarizer)

	resp, err := service.Submit(context.Background(), invocation.ExternalSubmitRequest{
		Source: "amp",
		Tool:   "bash",
		Input:  map[string]any{"cmd": "ls"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != invocation.StatusPendingApproval {
		t.Fatalf("expected pending approval, got %s", resp.Status)
	}

	var got invocation.InvocationResponse
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got, err = service.Get(context.Background(), resp.InvocationID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Summary == "Run shell command ls." {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got.Summary != "Run shell command ls." {
		t.Fatalf("expected automatic summary, got %q", got.Summary)
	}
	summaryReq := summarizer.request()
	if summaryReq.ModelConfigCUID != "model_123" {
		t.Fatalf("model_config_cuid = %q", summaryReq.ModelConfigCUID)
	}
	if summaryReq.OrgCUID != "org_123" {
		t.Fatalf("org_cuid = %q", summaryReq.OrgCUID)
	}
	if summaryReq.Invocation["invocation_id"] != resp.InvocationID {
		t.Fatalf("backend invocation payload = %#v", summaryReq.Invocation)
	}
	input, ok := summaryReq.Invocation["input"].(map[string]any)
	if !ok || input["cmd"] != "ls" {
		t.Fatalf("backend invocation input = %#v", summaryReq.Invocation["input"])
	}
}

func TestSubmitMatchesApprovalRuleByExternalToolName(t *testing.T) {
	db := newSQLiteTestDB(t)
	invRepo := store.NewInvocationRepo(db)
	eventRepo := store.NewEventRepo(db)
	rulesRepo := store.NewRulesRepo(db)
	if err := rulesRepo.Create(context.Background(), store.Rule{
		ID:             "rule-managed-bash",
		Action:         invocation.RuleActionAutoApprove,
		ServerPatterns: []string{"claude-managed-agents"},
		ToolPatterns:   []string{"Bash"},
		Enabled:        true,
	}); err != nil {
		t.Fatal(err)
	}
	service := invocation.NewService(
		invRepo,
		eventRepo,
		nil,
		nil,
		nil,
		5*time.Second,
		rulesRepo,
		nil,
		nil,
		nil,
	)

	resp, err := service.Submit(context.Background(), invocation.ExternalSubmitRequest{
		Source: "claude-managed-agents",
		Tool:   "Bash",
		Input:  map[string]any{"command": "pwd"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != invocation.StatusApproved {
		t.Fatalf("status = %q, want approved", resp.Status)
	}
	if resp.MatchedRuleID == nil || *resp.MatchedRuleID != "rule-managed-bash" {
		t.Fatalf("matched rule id = %v, want rule-managed-bash", resp.MatchedRuleID)
	}
}

func TestSubmitAIEvaluationUsesDefaultAgentRecordForUnmappedAgentID(t *testing.T) {
	db := newSQLiteTestDB(t)
	invRepo := store.NewInvocationRepo(db)
	eventRepo := store.NewEventRepo(db)
	evaluator := &evaluateClientStub{
		resp: invocation.EvaluateResponse{Verdict: "approved", Reason: "default charter allows this"},
	}
	defaultAgent := invocation.AgentRecord{
		ID:                 "agent-local-default",
		VMCUID:             "agent-vm-default",
		VMOrganizationCUID: "org-default",
	}
	service := invocation.NewService(
		invRepo,
		eventRepo,
		nil,
		nil,
		nil,
		5*time.Second,
		rulesStoreStub{rules: []invocation.ApprovalRule{{
			Action:          invocation.RuleActionAIEvaluation,
			ToolPatterns:    []string{"bash"},
			ModelConfigCUID: "model-ai",
			AgentCUIDs:      []string{defaultAgent.ID},
			Enabled:         true,
		}}},
		agentLookupStub{byVMCUID: map[string]invocation.AgentRecord{defaultAgent.VMCUID: defaultAgent}},
		evaluator,
		summarySettingsStub{
			charterFieldKey:    "charter",
			defaultAgentVMCUID: defaultAgent.VMCUID,
		},
	)

	ctx := auth.WithIdentity(context.Background(), auth.Identity{AgentID: "hunners-codex"})
	resp, err := service.Submit(ctx, invocation.ExternalSubmitRequest{
		Source: "github",
		Tool:   "bash",
		Input:  map[string]any{"cmd": "pwd"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != invocation.StatusApproved {
		t.Fatalf("status = %q", resp.Status)
	}
	req := evaluator.request()
	if req.AgentVMCUID != defaultAgent.VMCUID {
		t.Fatalf("AgentVMCUID = %q", req.AgentVMCUID)
	}
	if req.OrgCUID != defaultAgent.VMOrganizationCUID {
		t.Fatalf("OrgCUID = %q", req.OrgCUID)
	}
	if req.CharterFieldKey != "charter" {
		t.Fatalf("CharterFieldKey = %q", req.CharterFieldKey)
	}
	if req.ModelConfigCUID != "model-ai" {
		t.Fatalf("ModelConfigCUID = %q", req.ModelConfigCUID)
	}
}

func TestInvokeAIEvaluationIncludesToolDescriptionInContext(t *testing.T) {
	toolDescription := "Attach an external URL reference to a Shortcut story. Non-destructive."
	toolSchema := `{"type":"object","properties":{"story_public_id":{"type":"integer"},"url":{"type":"string"}}}`

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		switch body["method"] {
		case "initialize":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": body["id"],
				"result": map[string]any{
					"serverInfo":   map[string]any{"name": "fake-shortcut", "version": "0.1.0"},
					"capabilities": map[string]any{},
				},
			})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": body["id"],
				"result": map[string]any{"tools": []map[string]any{{
					"name":        "stories-add-external-link",
					"description": toolDescription,
					"inputSchema": json.RawMessage(toolSchema),
				}}},
			})
		case "tools/call":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": body["id"], "result": map[string]any{"content": []map[string]any{{"type": "text", "text": "ok"}}}})
		default:
			t.Errorf("unexpected method: %v", body["method"])
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer upstream.Close()

	db := newSQLiteTestDB(t)
	serverRepo := store.NewServerRepo(db)
	resolver := mcp.NewResolver(serverRepo, config.Config{
		Upstreams: []config.UpstreamConfig{{Name: "shortcut", Mode: "http", BaseURL: upstream.URL, Enabled: true, TimeoutSeconds: 5}},
	})
	if err := resolver.BootstrapIfEmpty(context.Background()); err != nil {
		t.Fatal(err)
	}

	evaluator := &evaluateClientStub{
		resp: invocation.EvaluateResponse{Verdict: "approved", Reason: "charter allows non-destructive link operations"},
	}
	defaultAgent := invocation.AgentRecord{
		ID:                 "agent-local-default",
		VMCUID:             "agent-vm-default",
		VMOrganizationCUID: "org-default",
		Charter:            "Deny destructive tool calls, otherwise approve.",
	}

	service := invocation.NewService(
		store.NewInvocationRepo(db),
		store.NewEventRepo(db),
		resolver,
		mcp.NewHTTPClient(),
		nil,
		5*time.Second,
		rulesStoreStub{rules: []invocation.ApprovalRule{{
			Action:          invocation.RuleActionAIEvaluation,
			ServerPatterns:  []string{"shortcut"},
			ToolPatterns:    []string{"stories-add-external-link"},
			ModelConfigCUID: "model-ai",
			Enabled:         true,
		}}},
		agentLookupStub{byVMCUID: map[string]invocation.AgentRecord{defaultAgent.VMCUID: defaultAgent}},
		evaluator,
		summarySettingsStub{
			charterFieldKey:    "constitution",
			defaultAgentVMCUID: defaultAgent.VMCUID,
		},
	)

	resp, err := service.Invoke(context.Background(), invocation.CreateInvocationRequest{
		Server: "shortcut",
		Tool:   "stories-add-external-link",
		Input:  map[string]any{"story_public_id": 16782, "url": "https://example.com"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != invocation.StatusSucceeded {
		t.Fatalf("status = %q, want succeeded", resp.Status)
	}

	req := evaluator.request()
	if req.Context == "" {
		t.Fatalf("EvaluateRequest.Context is empty; expected tool description")
	}
	if !strings.Contains(req.Context, toolDescription) {
		t.Fatalf("EvaluateRequest.Context missing tool description; got %q", req.Context)
	}
	if !strings.Contains(req.Context, "story_public_id") {
		t.Fatalf("EvaluateRequest.Context missing input schema; got %q", req.Context)
	}
}

func TestInvokeAIEvaluationToleratesMissingToolDescription(t *testing.T) {
	// When tools/list fails or omits the tool, evaluation must still run —
	// just without the context enrichment.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		switch body["method"] {
		case "initialize":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": body["id"], "result": map[string]any{"serverInfo": map[string]any{"name": "fake", "version": "0.1.0"}, "capabilities": map[string]any{}}})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			w.WriteHeader(http.StatusInternalServerError)
		case "tools/call":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": body["id"], "result": map[string]any{"content": []map[string]any{{"type": "text", "text": "ok"}}}})
		}
	}))
	defer upstream.Close()

	db := newSQLiteTestDB(t)
	serverRepo := store.NewServerRepo(db)
	resolver := mcp.NewResolver(serverRepo, config.Config{
		Upstreams: []config.UpstreamConfig{{Name: "shortcut", Mode: "http", BaseURL: upstream.URL, Enabled: true, TimeoutSeconds: 5}},
	})
	if err := resolver.BootstrapIfEmpty(context.Background()); err != nil {
		t.Fatal(err)
	}

	evaluator := &evaluateClientStub{resp: invocation.EvaluateResponse{Verdict: "approved", Reason: "ok"}}
	defaultAgent := invocation.AgentRecord{ID: "a", VMCUID: "vm-a", VMOrganizationCUID: "org", Charter: "c"}
	service := invocation.NewService(
		store.NewInvocationRepo(db),
		store.NewEventRepo(db),
		resolver,
		mcp.NewHTTPClient(),
		nil,
		5*time.Second,
		rulesStoreStub{rules: []invocation.ApprovalRule{{Action: invocation.RuleActionAIEvaluation, ServerPatterns: []string{"shortcut"}, ToolPatterns: []string{"stories-search"}, ModelConfigCUID: "m", Enabled: true}}},
		agentLookupStub{byVMCUID: map[string]invocation.AgentRecord{defaultAgent.VMCUID: defaultAgent}},
		evaluator,
		summarySettingsStub{charterFieldKey: "constitution", defaultAgentVMCUID: defaultAgent.VMCUID},
	)

	resp, err := service.Invoke(context.Background(), invocation.CreateInvocationRequest{Server: "shortcut", Tool: "stories-search", Input: map[string]any{"q": "x"}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != invocation.StatusSucceeded {
		t.Fatalf("status = %q", resp.Status)
	}
	if evaluator.request().Context != "" {
		t.Fatalf("expected empty Context when tools/list fails, got %q", evaluator.request().Context)
	}
}

func newTestService(t *testing.T, cfg config.Config) *invocation.Service {
	t.Helper()
	db := newSQLiteTestDB(t)
	serverRepo := store.NewServerRepo(db)
	resolver := mcp.NewResolver(serverRepo, cfg)
	if err := resolver.BootstrapIfEmpty(context.Background()); err != nil {
		t.Fatal(err)
	}
	return invocation.NewService(store.NewInvocationRepo(db), store.NewEventRepo(db), resolver, mcp.NewHTTPClient(), policy.AlwaysApproveProvider{}, 5*time.Second, nil, nil, nil, nil)
}

type summaryClientStub struct {
	mu      sync.Mutex
	summary string
	req     invocation.SummaryRequest
}

func (s *summaryClientStub) SummarizeInvocation(_ context.Context, req invocation.SummaryRequest) (invocation.SummaryResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.req = req
	return invocation.SummaryResponse{Summary: s.summary}, nil
}

func (s *summaryClientStub) request() invocation.SummaryRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.req
}

type summarySettingsStub struct {
	orgCUID            string
	modelConfigCUID    string
	charterFieldKey    string
	defaultAgentVMCUID string
}

func (s summarySettingsStub) CharterFieldKey(context.Context) string {
	return s.charterFieldKey
}

func (s summarySettingsStub) DefaultAgentVMCUID(context.Context) string {
	return s.defaultAgentVMCUID
}

func (s summarySettingsStub) SummarySettings(context.Context) (string, string) {
	return s.orgCUID, s.modelConfigCUID
}

type rulesStoreStub struct {
	rules []invocation.ApprovalRule
}

func (s rulesStoreStub) ListApprovalRules(context.Context) ([]invocation.ApprovalRule, error) {
	return s.rules, nil
}

type agentLookupStub struct {
	byAgentID map[string]invocation.AgentRecord
	byVMCUID  map[string]invocation.AgentRecord
}

func (s agentLookupStub) GetByAgentID(_ context.Context, agentID string) (invocation.AgentRecord, error) {
	if s.byAgentID != nil {
		if rec, ok := s.byAgentID[agentID]; ok {
			return rec, nil
		}
	}
	return invocation.AgentRecord{}, sql.ErrNoRows
}

func (s agentLookupStub) GetByVMCUID(_ context.Context, vmCUID string) (invocation.AgentRecord, error) {
	if s.byVMCUID != nil {
		if rec, ok := s.byVMCUID[vmCUID]; ok {
			return rec, nil
		}
	}
	return invocation.AgentRecord{}, sql.ErrNoRows
}

type evaluateClientStub struct {
	mu   sync.Mutex
	req  invocation.EvaluateRequest
	resp invocation.EvaluateResponse
}

func (s *evaluateClientStub) EvaluateToolCall(_ context.Context, req invocation.EvaluateRequest) (invocation.EvaluateResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.req = req
	return s.resp, nil
}

func (s *evaluateClientStub) request() invocation.EvaluateRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.req
}

func approveNextInvocation(t *testing.T, service *invocation.Service, delay time.Duration) {
	time.Sleep(delay)
	list, err := service.List(context.Background(), invocation.InvocationListFilter{Limit: 10})
	// Return silently when there are no pending invocations — this is expected
	// when AlwaysApproveProvider completes the invocation before this goroutine
	// runs. Calling t.Errorf after the test has completed causes a panic.
	if err != nil || len(list.Items) == 0 {
		return
	}
	pending := list.Items[0]
	if pending.Status != invocation.StatusPendingApproval {
		return
	}
	if err := service.Approve(context.Background(), pending.InvocationID); err != nil {
		t.Errorf("approve invocation: %v", err)
	}
}

func jsonContains(raw json.RawMessage, needle string) bool {
	return string(raw) != "" && strings.Contains(string(raw), needle)
}

func assertInvokeLifecycle(t *testing.T, service *invocation.Service, server, tool string) {
	t.Helper()
	key := "demo-key-" + server + "-" + tool
	resp, err := service.Invoke(context.Background(), invocation.CreateInvocationRequest{Server: server, Tool: tool, Input: map[string]any{"n": 1}, IdempotencyKey: &key})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != invocation.StatusSucceeded {
		t.Fatalf("got status %s", resp.Status)
	}
	if resp.InvocationID == "" {
		t.Fatal("expected invocation id")
	}
	if resp.ServerName != server {
		t.Fatalf("expected server name %q, got %q", server, resp.ServerName)
	}
	if resp.ToolName != tool {
		t.Fatalf("expected tool name %q, got %q", tool, resp.ToolName)
	}
	if resp.Approval == nil {
		t.Fatal("expected approval to be set")
	}
	if resp.Approval.Status != "auto_approved" {
		t.Fatalf("expected auto_approved status, got %q", resp.Approval.Status)
	}
	if string(resp.Input) != `{"n":1}` {
		t.Fatalf("expected input to be surfaced, got %s", string(resp.Input))
	}
	if resp.SubmittedAt.IsZero() {
		t.Fatal("expected submitted_at")
	}
	if resp.CompletedAt == nil {
		t.Fatal("expected completed_at")
	}
	if len(resp.Result) == 0 {
		t.Fatal("expected result")
	}

	again, err := service.Invoke(context.Background(), invocation.CreateInvocationRequest{Server: server, Tool: tool, Input: map[string]any{"n": 1}, IdempotencyKey: &key})
	if err != nil {
		t.Fatal(err)
	}
	if again.InvocationID != resp.InvocationID {
		t.Fatal("expected idempotent replay to return same invocation")
	}

	events, err := service.Events(context.Background(), resp.InvocationID, invocation.EventListFilter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(events.Items) < 2 {
		t.Fatalf("expected events, got %d", len(events.Items))
	}
	if !jsonContains(events.Items[0].Data, `"input":{"n":1}`) || !jsonContains(events.Items[0].Data, `"arguments":{"n":1}`) {
		t.Fatalf("expected surfaced input/arguments in event payload, got %s", string(events.Items[0].Data))
	}
	if events.Items[0].Type == "" {
		t.Fatal("expected event type")
	}
	if events.Items[0].Timestamp.IsZero() {
		t.Fatal("expected event timestamp")
	}
}
