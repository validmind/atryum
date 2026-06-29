package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"atryum/internal/auth"
	backendclient "atryum/internal/backend"
	"atryum/internal/config"
	"atryum/internal/invocation"
	"atryum/internal/managedagents"
	"atryum/internal/mcp"
	"atryum/internal/store"

	_ "modernc.org/sqlite"
)

type stubService struct {
	tools    []mcp.Tool
	invoke   invocation.InvocationResponse
	invErr   error
	getErr   error
	setResp  invocation.InvocationResponse
	setID    string
	setText  string
	listErr  error
	upstream mcp.Upstream
	forward  mcp.ForwardResult
	fwdErr   error

	invokedReq *invocation.CreateInvocationRequest
	invokedCtx context.Context
	submitReq  *invocation.ExternalSubmitRequest
	submitCtx  context.Context
	getCtx     context.Context
	recordID   string
	recordReq  *invocation.ExternalExecutionUpdate
	recordCtx  context.Context
}

func (s *stubService) Invoke(ctx context.Context, req invocation.CreateInvocationRequest) (invocation.InvocationResponse, error) {
	s.invokedReq = &req
	s.invokedCtx = ctx
	return s.invoke, s.invErr
}
func (s *stubService) ListTools(context.Context, string) ([]mcp.Tool, error) {
	return s.tools, s.listErr
}
func (s *stubService) Get(ctx context.Context, _ string) (invocation.InvocationResponse, error) {
	s.getCtx = ctx
	return s.invoke, s.getErr
}
func (s *stubService) List(context.Context, invocation.InvocationListFilter) (invocation.InvocationListResponse, error) {
	return invocation.InvocationListResponse{Items: []invocation.InvocationResponse{s.invoke}, Total: 1, Limit: 50}, nil
}
func (s *stubService) ListAgentIDs(context.Context) ([]string, error) {
	return nil, nil
}
func (s *stubService) Events(context.Context, string, invocation.EventListFilter) (invocation.EventListResponse, error) {
	return invocation.EventListResponse{}, nil
}
func (s *stubService) Approve(context.Context, string) error      { return nil }
func (s *stubService) Deny(context.Context, string, string) error { return nil }
func (s *stubService) Submit(ctx context.Context, req invocation.ExternalSubmitRequest) (invocation.InvocationResponse, error) {
	s.submitReq = &req
	s.submitCtx = ctx
	if s.invoke.InvocationID != "" {
		return s.invoke, s.invErr
	}
	return invocation.InvocationResponse{InvocationID: "inv_submit", ToolName: req.Tool, Status: invocation.StatusPendingApproval}, s.invErr
}
func (s *stubService) SetSummary(_ context.Context, id string, summary string) (invocation.InvocationResponse, error) {
	s.setID = id
	s.setText = summary
	if s.setResp.InvocationID != "" {
		return s.setResp, nil
	}
	resp := s.invoke
	resp.Summary = summary
	return resp, nil
}
func (s *stubService) RecordExecution(ctx context.Context, id string, req invocation.ExternalExecutionUpdate) (invocation.InvocationResponse, error) {
	s.recordID = id
	s.recordReq = &req
	s.recordCtx = ctx
	if s.invoke.InvocationID != "" {
		return s.invoke, s.invErr
	}
	return invocation.InvocationResponse{InvocationID: id, Status: invocation.StatusSucceeded}, s.invErr
}
func (s *stubService) ForwardEnvelope(context.Context, mcp.Upstream, mcp.Envelope, string) (mcp.ForwardResult, error) {
	return s.forward, s.fwdErr
}
func (s *stubService) ResolveContext(context.Context, string) (mcp.Upstream, error) {
	return s.upstream, nil
}

type stubServerService struct{}

func (stubServerService) List(context.Context, mcp.ServerFilter) (ServerListResponse, error) {
	return ServerListResponse{}, nil
}
func (stubServerService) Get(context.Context, string) (AdminServer, error) { return AdminServer{}, nil }
func (stubServerService) Upsert(context.Context, string, AdminServerUpsertRequest) (AdminServer, error) {
	return AdminServer{}, nil
}
func (stubServerService) Delete(context.Context, string, bool) error { return nil }
func (stubServerService) Test(context.Context, string) (ServerTestResponse, error) {
	return ServerTestResponse{}, nil
}
func (stubServerService) StartConnect(context.Context, string, string) (OAuthConnectStartResponse, error) {
	return OAuthConnectStartResponse{}, nil
}
func (stubServerService) GetConnectStatus(context.Context, string) (OAuthConnectStatusResponse, error) {
	return OAuthConnectStatusResponse{}, nil
}
func (stubServerService) CompleteConnect(context.Context, string, string, string) (OAuthConnectStatusResponse, error) {
	return OAuthConnectStatusResponse{}, nil
}

type callbackServerService struct {
	stubServerService
	state     string
	code      string
	errorText string
}

func (s *callbackServerService) CompleteConnect(_ context.Context, state string, code string, errorText string) (OAuthConnectStatusResponse, error) {
	s.state = state
	s.code = code
	s.errorText = errorText
	message := "connected"
	return OAuthConnectStatusResponse{Status: "succeeded", Message: &message}, nil
}

type stubRulesRepo struct {
	rules []store.Rule
	err   error
}

func (s *stubRulesRepo) Create(context.Context, store.Rule) error { return nil }
func (s *stubRulesRepo) Get(_ context.Context, id string) (store.Rule, error) {
	for _, rule := range s.rules {
		if rule.ID == id {
			return rule, nil
		}
	}
	return store.Rule{}, nil
}
func (s *stubRulesRepo) List(context.Context) ([]store.Rule, error) {
	return s.rules, s.err
}
func (s *stubRulesRepo) NextOrder(context.Context) (int, error)   { return len(s.rules), nil }
func (s *stubRulesRepo) Update(context.Context, store.Rule) error { return nil }
func (s *stubRulesRepo) Delete(context.Context, string) error     { return nil }
func (s *stubRulesRepo) Move(context.Context, string, string) ([]store.Rule, error) {
	return s.rules, nil
}
func (s *stubRulesRepo) InsertBefore(context.Context, string, store.Rule) error { return nil }

type stubSummarizer struct {
	req  backendclient.SummarizeInvocationRequest
	resp backendclient.SummarizeInvocationResponse
	err  error
}

func (s *stubSummarizer) SummarizeInvocation(_ context.Context, req backendclient.SummarizeInvocationRequest) (backendclient.SummarizeInvocationResponse, error) {
	s.req = req
	return s.resp, s.err
}

type stubManagedAgentsAdmin struct {
	err      error
	req      managedagents.RegisterSessionRequest
	agents   []managedagents.AgentInfo
	sessions []managedagents.SessionRegistration
	deleted  string
	cleared  bool
}

func (s *stubManagedAgentsAdmin) RegisterSession(_ context.Context, req managedagents.RegisterSessionRequest) (managedagents.SessionRegistration, error) {
	s.req = req
	if s.err != nil {
		return managedagents.SessionRegistration{}, s.err
	}
	return managedagents.SessionRegistration{SessionID: req.SessionID, Account: req.Account, AgentID: req.AgentID}, nil
}

func (s *stubManagedAgentsAdmin) ListSessions(context.Context) ([]managedagents.SessionRegistration, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.sessions, nil
}

func (s *stubManagedAgentsAdmin) DeleteSession(_ context.Context, sessionID string) error {
	s.deleted = sessionID
	return s.err
}

func (s *stubManagedAgentsAdmin) ClearSessions(context.Context) (int, error) {
	s.cleared = true
	if s.err != nil {
		return 0, s.err
	}
	return len(s.sessions), nil
}

func (s *stubManagedAgentsAdmin) Accounts() []managedagents.AccountInfo {
	return []managedagents.AccountInfo{{Name: managedagents.DefaultAccountName}}
}

func (s *stubManagedAgentsAdmin) ListAgents(context.Context, managedagents.ListAgentsRequest) ([]managedagents.AgentInfo, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.agents, nil
}

func (s *stubManagedAgentsAdmin) ClaimAgent(_ context.Context, req managedagents.AgentClaimRequest) (managedagents.AgentInfo, error) {
	if s.err != nil {
		return managedagents.AgentInfo{}, s.err
	}
	return managedagents.AgentInfo{ID: req.ClaudeAgentID, Version: 1}, nil
}

func (s *stubManagedAgentsAdmin) ReleaseAgent(context.Context, managedagents.AgentClaimRequest) error {
	return nil
}

type stubAgentSyncSettingsRepo struct {
	settings store.AgentSyncSettings
}

func (s *stubAgentSyncSettingsRepo) Get(context.Context) (store.AgentSyncSettings, error) {
	return s.settings, nil
}

func (s *stubAgentSyncSettingsRepo) Save(_ context.Context, settings store.AgentSyncSettings) error {
	s.settings = settings
	return nil
}

func TestUpstreamMCPOAuthCallbackRouteIsPublicMCPPath(t *testing.T) {
	t.Run("new path serves callback", func(t *testing.T) {
		serverSvc := &callbackServerService{}
		h := NewHandler(&stubService{}, serverSvc, nil, nil, nil, nil, nil, nil, nil, nil)
		req := httptest.NewRequest(http.MethodGet, upstreamMCPOAuthCallbackPath+"?state=state-123&code=code-456", nil)
		w := httptest.NewRecorder()

		h.Routes().ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
		}
		if serverSvc.state != "state-123" || serverSvc.code != "code-456" || serverSvc.errorText != "" {
			t.Fatalf("callback args = state %q code %q error %q", serverSvc.state, serverSvc.code, serverSvc.errorText)
		}
	})

	t.Run("old path is removed", func(t *testing.T) {
		h := NewHandler(&stubService{}, &callbackServerService{}, nil, nil, nil, nil, nil, nil, nil, nil)
		req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/oauth/callback?state=old&code=old", nil)
		w := httptest.NewRecorder()

		h.Routes().ServeHTTP(w, req)

		if w.Code == http.StatusOK {
			t.Fatalf("expected old admin callback path not to succeed")
		}
	})
}

func TestStartConnectUsesUpstreamMCPOAuthCallbackRedirectURI(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	if err := store.InitDB(db); err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	ctx := context.Background()
	serverRepo := store.NewServerRepo(db)
	oauthRepo := store.NewOAuthRepo(db)
	if err := serverRepo.UpsertServer(ctx, mcp.Upstream{
		Name:              "shortcut",
		Mode:              mcp.UpstreamModeHTTP,
		BaseURL:           "https://mcp.example.test",
		Enabled:           true,
		OAuthAuthorizeURL: "https://idp.example.test/authorize",
		OAuthTokenURL:     "https://idp.example.test/token",
		OAuthClientID:     "client-123",
		OAuthScopes:       "read write",
	}); err != nil {
		t.Fatalf("UpsertServer: %v", err)
	}

	svc := NewServerAdminService(serverRepo, oauthRepo, nil, 5*time.Second, "")
	resp, err := svc.StartConnect(ctx, "shortcut", "http://localhost:8080/")
	if err != nil {
		t.Fatalf("StartConnect: %v", err)
	}

	parsed, err := url.Parse(resp.ConnectURL)
	if err != nil {
		t.Fatalf("parse connect url: %v", err)
	}
	wantRedirectURI := "http://localhost:8080" + upstreamMCPOAuthCallbackPath
	if got := parsed.Query().Get("redirect_uri"); got != wantRedirectURI {
		t.Fatalf("redirect_uri = %q, want %q", got, wantRedirectURI)
	}

	session, err := oauthRepo.GetConnectSession(ctx, resp.State)
	if err != nil {
		t.Fatalf("GetConnectSession: %v", err)
	}
	if session.RedirectURI != wantRedirectURI {
		t.Fatalf("saved redirect_uri = %q, want %q", session.RedirectURI, wantRedirectURI)
	}
}

func TestServerAdminServiceSurfacesEndpointURLForSluggedServerName(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	if err := store.InitDB(db); err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	ctx := context.Background()
	serverRepo := store.NewServerRepo(db)
	oauthRepo := store.NewOAuthRepo(db)
	svc := NewServerAdminService(serverRepo, oauthRepo, nil, 5*time.Second, "https://atryum.example")

	server, err := svc.Upsert(ctx, "", AdminServerUpsertRequest{
		Name:           "Slack Local",
		Mode:           string(mcp.UpstreamModeHTTP),
		BaseURL:        "https://mcp.slack.test/mcp",
		TimeoutSeconds: 10,
	})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if server.Name != "Slack Local" {
		t.Fatalf("name = %q, want Slack Local", server.Name)
	}
	if server.EndpointSlug != "slack-local" {
		t.Fatalf("endpoint_slug = %q, want slack-local", server.EndpointSlug)
	}
	if server.EndpointURL != "https://atryum.example/mcp/slack-local" {
		t.Fatalf("endpoint_url = %q", server.EndpointURL)
	}

	resolver := mcp.NewResolver(serverRepo, config.Config{})
	upstream, err := resolver.ResolveContext(ctx, "slack-local")
	if err != nil {
		t.Fatalf("ResolveContext: %v", err)
	}
	if upstream.Name != "Slack Local" {
		t.Fatalf("resolved name = %q, want Slack Local", upstream.Name)
	}
}

func TestServerAdminServiceRejectsDuplicateEndpointSlug(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	if err := store.InitDB(db); err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	ctx := context.Background()
	serverRepo := store.NewServerRepo(db)
	oauthRepo := store.NewOAuthRepo(db)
	svc := NewServerAdminService(serverRepo, oauthRepo, nil, 5*time.Second, "")

	_, err = svc.Upsert(ctx, "", AdminServerUpsertRequest{
		Name:    "Slack Local",
		Mode:    string(mcp.UpstreamModeHTTP),
		BaseURL: "https://mcp.slack.test/mcp",
	})
	if err != nil {
		t.Fatalf("Upsert first server: %v", err)
	}
	_, err = svc.Upsert(ctx, "", AdminServerUpsertRequest{
		Name:    "slack-local",
		Mode:    string(mcp.UpstreamModeHTTP),
		BaseURL: "https://other.example/mcp",
	})
	if err == nil || !strings.Contains(err.Error(), `endpoint slug "slack-local"`) {
		t.Fatalf("expected duplicate endpoint slug error, got %v", err)
	}
}

func TestServerAdminServiceUsesStoredEndpointSlug(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	if err := store.InitDB(db); err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	ctx := context.Background()
	serverRepo := store.NewServerRepo(db)
	oauthRepo := store.NewOAuthRepo(db)
	if err := serverRepo.UpsertServer(ctx, mcp.Upstream{
		Name:         "Slack Local",
		EndpointSlug: "slack-local-2",
		Mode:         mcp.UpstreamModeHTTP,
		BaseURL:      "https://mcp.slack.test/mcp",
		Enabled:      true,
	}); err != nil {
		t.Fatalf("UpsertServer: %v", err)
	}

	svc := NewServerAdminService(serverRepo, oauthRepo, nil, 5*time.Second, "https://atryum.example")
	server, err := svc.Get(ctx, "Slack Local")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if server.EndpointSlug != "slack-local-2" {
		t.Fatalf("endpoint_slug = %q, want slack-local-2", server.EndpointSlug)
	}
	if server.EndpointURL != "https://atryum.example/mcp/slack-local-2" {
		t.Fatalf("endpoint_url = %q", server.EndpointURL)
	}
}

func TestAdminServerTestDebugLogsRequestContext(t *testing.T) {
	t.Setenv("ATRYUM_MCP_DEBUG", "1")

	var logs bytes.Buffer
	prevWriter := log.Writer()
	prevFlags := log.Flags()
	log.SetOutput(&logs)
	log.SetFlags(0)
	defer func() {
		log.SetOutput(prevWriter)
		log.SetFlags(prevFlags)
	}()

	h := NewHandler(&stubService{}, stubServerService{}, nil, nil, nil, nil, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/servers/shortcut/test", nil)
	req.Header.Set("Origin", "http://localhost:8080")
	req.Header.Set("Referer", "http://localhost:8080/ui/")
	req.Header.Set("User-Agent", "debug-test")
	req.Header.Set("Cookie", "authToken=secret-token")
	w := httptest.NewRecorder()

	h.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	got := logs.String()
	for _, want := range []string{
		"admin server test request method=POST path=/api/v1/admin/servers/shortcut/test server=shortcut",
		"origin=\"http://localhost:8080\"",
		"referer=\"http://localhost:8080/ui/\"",
		"user_agent=\"debug-test\"",
		"admin server test response server=shortcut",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected logs to contain %q, got:\n%s", want, got)
		}
	}
	if strings.Contains(got, "secret-token") || strings.Contains(got, "Cookie") {
		t.Fatalf("expected logs to omit cookie values, got:\n%s", got)
	}
}

func TestManagedAgentSessionRegistrationDebugLogsFailure(t *testing.T) {
	t.Setenv("ATRYUM_MCP_DEBUG", "1")

	var logs bytes.Buffer
	prevWriter := log.Writer()
	prevFlags := log.Flags()
	log.SetOutput(&logs)
	log.SetFlags(0)
	defer func() {
		log.SetOutput(prevWriter)
		log.SetFlags(prevFlags)
	}()

	h := NewHandler(&stubService{}, stubServerService{}, nil, nil, nil, nil, nil, nil, nil, nil)
	h.SetManagedAgents(&stubManagedAgentsAdmin{err: fmt.Errorf("unknown managed-agent account %q", "default")})
	body := strings.NewReader(`{
		"session_id": "sesn_01K1L93ktjwd7CgFs7L5ZDXs",
		"account": "default",
		"agent_id": "agent_013popGjeyhH8qPYqziHxdLk"
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/managed-agents/sessions", body)
	req.Header.Set("content-type", "application/json")
	w := httptest.NewRecorder()

	h.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", w.Code, w.Body.String())
	}
	got := logs.String()
	for _, want := range []string{
		"managed-agents session registration request",
		"session_id=\"sesn_01K1L93ktjwd7CgFs7L5ZDXs\"",
		"account=\"default\"",
		"agent_id=\"agent_013popGjeyhH8qPYqziHxdLk\"",
		"managed-agents session registration failed",
		"unknown managed-agent account \"default\"",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected logs to contain %q, got:\n%s", want, got)
		}
	}
}

type stubAgentsRepo struct {
	records     []store.AgentRecord
	err         error
	byVMCUID    map[string]store.AgentRecord
	byVMCUIDErr map[string]error
}

func (s *stubAgentsRepo) List(context.Context) ([]store.AgentRecord, error) {
	return s.records, s.err
}
func (s *stubAgentsRepo) ListEnabled(context.Context) ([]store.AgentRecord, error) {
	var out []store.AgentRecord
	for _, r := range s.records {
		if r.Enabled {
			out = append(out, r)
		}
	}
	return out, s.err
}
func (s *stubAgentsRepo) Get(context.Context, string) (store.AgentRecord, error) {
	return store.AgentRecord{}, nil
}
func (s *stubAgentsRepo) GetByAgentID(_ context.Context, agentID string) (store.AgentRecord, error) {
	for _, r := range s.records {
		for _, id := range parseAgentIDs(r.AgentIDs) {
			if id == agentID {
				return r, nil
			}
		}
	}
	return store.AgentRecord{}, fmt.Errorf("sql: no rows in result set")
}
func (s *stubAgentsRepo) GetByVMCUID(_ context.Context, vmCUID string) (store.AgentRecord, error) {
	if s.byVMCUIDErr != nil {
		if err, ok := s.byVMCUIDErr[vmCUID]; ok {
			return store.AgentRecord{}, err
		}
	}
	if s.byVMCUID != nil {
		if rec, ok := s.byVMCUID[vmCUID]; ok {
			return rec, nil
		}
	}
	return store.AgentRecord{}, fmt.Errorf("sql: no rows in result set")
}
func (s *stubAgentsRepo) UpdateEnabled(context.Context, string, bool) error { return nil }
func (s *stubAgentsRepo) UpdateAgentIDs(context.Context, string, string) error {
	return nil
}
func (s *stubAgentsRepo) UpdateMeta(context.Context, string, string, string, string) error {
	return nil
}
func (s *stubAgentsRepo) CheckAgentIDConflict(_ context.Context, excludeID string, agentIDs []string) (string, string, error) {
	for _, id := range agentIDs {
		for _, r := range s.records {
			if r.ID == excludeID {
				continue
			}
			for _, rid := range parseAgentIDs(r.AgentIDs) {
				if rid == id {
					return id, r.VMName, nil
				}
			}
		}
	}
	return "", "", nil
}
func (s *stubAgentsRepo) Create(_ context.Context, _ store.AgentRecord) error { return nil }
func (s *stubAgentsRepo) Delete(context.Context, string) error                { return nil }
func (s *stubAgentsRepo) DeleteSynced(context.Context) error                  { return nil }
func (s *stubAgentsRepo) DeleteAll(context.Context) error {
	return nil
}

func TestMCPInitializeNegotiatesProtocolVersion(t *testing.T) {
	h := NewHandler(&stubService{}, stubServerService{}, nil, nil, nil, nil, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/mcp/demo", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`))

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("MCP-Protocol-Version", "2025-11-25")
	w := httptest.NewRecorder()

	h.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if got := w.Header().Get("MCP-Protocol-Version"); got != "2025-06-18" {
		t.Fatalf("expected negotiated header, got %q", got)
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	result := resp["result"].(map[string]any)
	if result["protocolVersion"] != "2025-06-18" {
		t.Fatalf("expected protocol version 2025-06-18, got %#v", result["protocolVersion"])
	}
	instructions, ok := result["instructions"].(string)
	if !ok || !strings.Contains(instructions, "gated by the Atryum harness") || !strings.Contains(instructions, "rules may change between conversation turns") {
		t.Fatalf("expected initialize instructions to describe Atryum gating and changing rules, got %#v", result["instructions"])
	}
}

func TestAgentIDsUsesAgentsTable(t *testing.T) {
	agents := &stubAgentsRepo{records: []store.AgentRecord{
		{ID: "agent_a", AgentIDs: `["worker-1","worker-2","worker-1",""]`, Enabled: true},
		{ID: "agent_b", AgentIDs: `["disabled-worker"]`, Enabled: false},
		{ID: "agent_c", AgentIDs: `[" worker-3 "]`, Enabled: true},
	}}
	h := NewHandler(&stubService{}, stubServerService{}, nil, nil, agents, nil, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/agent_ids", nil)
	w := httptest.NewRecorder()

	h.agentIDs(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Items []string `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	want := []string{"worker-1", "worker-2", "worker-3"}
	if len(resp.Items) != len(want) {
		t.Fatalf("expected %v, got %v", want, resp.Items)
	}
	for i := range want {
		if resp.Items[i] != want[i] {
			t.Fatalf("expected %v, got %v", want, resp.Items)
		}
	}
}

func TestSummarizeInvocationPersistsBackendSummary(t *testing.T) {
	now := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	svc := &stubService{invoke: invocation.InvocationResponse{
		InvocationID: "inv_123",
		ServerName:   "demo",
		ToolName:     "read_file",
		Status:       invocation.StatusSucceeded,
		Input:        json.RawMessage(`{"path":"/tmp/a"}`),
		Result:       json.RawMessage(`{"content":"hello"}`),
		SubmittedAt:  now,
		CompletedAt:  &now,
	}}
	summarizer := &stubSummarizer{resp: backendclient.SummarizeInvocationResponse{Summary: "Read /tmp/a and returned hello."}}
	h := NewHandler(svc, stubServerService{}, nil, nil, nil, nil, nil, nil, nil, nil)
	h.summarizeClient = summarizer
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/invocations/inv_123/summarize", strings.NewReader(`{"model_config_cuid":" model_abc "}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if summarizer.req.ModelConfigCUID != "model_abc" {
		t.Fatalf("model_config_cuid = %q", summarizer.req.ModelConfigCUID)
	}
	if summarizer.req.Invocation["invocation_id"] != "inv_123" {
		t.Fatalf("backend invocation payload = %#v", summarizer.req.Invocation)
	}
	input, ok := summarizer.req.Invocation["input"].(map[string]any)
	if !ok || input["path"] != "/tmp/a" {
		t.Fatalf("backend invocation input = %#v", summarizer.req.Invocation["input"])
	}
	if svc.setID != "inv_123" || svc.setText != "Read /tmp/a and returned hello." {
		t.Fatalf("SetSummary called with id=%q summary=%q", svc.setID, svc.setText)
	}
	var resp SummarizeInvocationResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.InvocationID != "inv_123" || resp.Summary != "Read /tmp/a and returned hello." {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestSummarizeInvocationUsesSettingsModelConfigWhenRequestBodyEmpty(t *testing.T) {
	now := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	svc := &stubService{invoke: invocation.InvocationResponse{
		InvocationID: "inv_123",
		ServerName:   "demo",
		ToolName:     "read_file",
		Status:       invocation.StatusSucceeded,
		Input:        json.RawMessage(`{"path":"/tmp/a"}`),
		Result:       json.RawMessage(`{"content":"hello"}`),
		SubmittedAt:  now,
		CompletedAt:  &now,
	}}
	settings := &stubAgentSyncSettingsRepo{settings: store.AgentSyncSettings{
		OrgCUID:                "org_abc",
		SummaryModelConfigCUID: " model_from_settings ",
	}}
	summarizer := &stubSummarizer{resp: backendclient.SummarizeInvocationResponse{Summary: "Read /tmp/a."}}
	h := NewHandler(svc, stubServerService{}, nil, nil, nil, settings, nil, nil, nil, nil)
	h.summarizeClient = summarizer
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/invocations/inv_123/summarize", nil)
	w := httptest.NewRecorder()

	h.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if summarizer.req.ModelConfigCUID != "model_from_settings" {
		t.Fatalf("model_config_cuid = %q", summarizer.req.ModelConfigCUID)
	}
	if summarizer.req.OrgCUID != "org_abc" {
		t.Fatalf("org_cuid = %q", summarizer.req.OrgCUID)
	}
	if svc.setID != "inv_123" || svc.setText != "Read /tmp/a." {
		t.Fatalf("SetSummary called with id=%q summary=%q", svc.setID, svc.setText)
	}
}

func TestInvocationSignatureChangesWhenSummaryChanges(t *testing.T) {
	now := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	base := []invocation.InvocationResponse{{
		InvocationID: "inv_123",
		Status:       invocation.StatusSucceeded,
		CompletedAt:  &now,
	}}
	withSummary := []invocation.InvocationResponse{{
		InvocationID: "inv_123",
		Status:       invocation.StatusSucceeded,
		CompletedAt:  &now,
		Summary:      "Read /tmp/a.",
	}}

	if invocationSignature(base) == invocationSignature(withSummary) {
		t.Fatal("expected summary-only update to change invocation signature")
	}
}

func TestMCPInitializedNotificationReturnsAccepted(t *testing.T) {
	h := NewHandler(&stubService{}, stubServerService{}, nil, nil, nil, nil, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/mcp/demo", strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`))
	w := httptest.NewRecorder()

	h.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", w.Code)
	}
}

func TestMCPPingPassThrough(t *testing.T) {
	svc := &stubService{upstream: mcp.Upstream{Name: "demo", Mode: mcp.UpstreamModeHTTP}, forward: mcp.ForwardResult{StatusCode: http.StatusOK, Body: []byte(`{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`), ContentType: "application/json", ProtocolVersion: "2025-11-25"}}
	h := NewHandler(svc, stubServerService{}, nil, nil, nil, nil, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/mcp/demo", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping","params":{}}`))
	w := httptest.NewRecorder()

	h.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"ok":true`) {
		t.Fatalf("expected forwarded body, got %s", w.Body.String())
	}
}

func TestMCPPingCollapsesUpstreamSSEWhenDownstreamDoesNotAcceptEventStream(t *testing.T) {
	sseBody := []byte("event: message\n" +
		"data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{\"progress\":0.5}}\n\n" +
		"event: message\n" +
		"data: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"ok\":true}}\n\n")
	svc := &stubService{
		upstream: mcp.Upstream{Name: "demo", Mode: mcp.UpstreamModeHTTP},
		forward:  mcp.ForwardResult{StatusCode: http.StatusOK, Body: sseBody, ContentType: "text/event-stream", ProtocolVersion: "2025-11-25"},
	}
	h := NewHandler(svc, stubServerService{}, nil, nil, nil, nil, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/mcp/demo", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping","params":{}}`))
	req.Header.Set("Accept", "application/json")
	w := httptest.NewRecorder()

	h.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("expected application/json, got %q", ct)
	}
	if strings.Contains(w.Body.String(), "event: message") {
		t.Fatalf("expected collapsed JSON body, got %q", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"ok":true`) {
		t.Fatalf("expected forwarded JSON-RPC response, got %s", w.Body.String())
	}
}

func TestMCPPingPreservesUpstreamSSEWhenDownstreamAcceptsEventStream(t *testing.T) {
	sseBody := []byte("event: message\n" +
		"data: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"ok\":true}}\n\n")
	svc := &stubService{
		upstream: mcp.Upstream{Name: "demo", Mode: mcp.UpstreamModeHTTP},
		forward:  mcp.ForwardResult{StatusCode: http.StatusOK, Body: sseBody, ContentType: "text/event-stream", ProtocolVersion: "2025-11-25"},
	}
	h := NewHandler(svc, stubServerService{}, nil, nil, nil, nil, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/mcp/demo", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping","params":{}}`))
	req.Header.Set("Accept", "text/event-stream")
	w := httptest.NewRecorder()

	h.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("expected text/event-stream, got %q", ct)
	}
	if !strings.Contains(w.Body.String(), "event: message") {
		t.Fatalf("expected raw SSE body, got %q", w.Body.String())
	}
}

func TestMCPPingCollapsesUpstreamSSEWhenEventStreamHasZeroQuality(t *testing.T) {
	sseBody := []byte("event: message\n" +
		"data: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"ok\":true}}\n\n")
	svc := &stubService{
		upstream: mcp.Upstream{Name: "demo", Mode: mcp.UpstreamModeHTTP},
		forward:  mcp.ForwardResult{StatusCode: http.StatusOK, Body: sseBody, ContentType: "text/event-stream", ProtocolVersion: "2025-11-25"},
	}
	h := NewHandler(svc, stubServerService{}, nil, nil, nil, nil, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/mcp/demo", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping","params":{}}`))
	req.Header.Set("Accept", "application/json, text/event-stream;q=0")
	w := httptest.NewRecorder()

	h.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if strings.Contains(w.Body.String(), "event: message") {
		t.Fatalf("expected collapsed JSON body, got %q", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"ok":true`) {
		t.Fatalf("expected forwarded JSON-RPC response, got %s", w.Body.String())
	}
}

func TestMCPPingSSEDecodeErrorPreservesUpstreamStatus(t *testing.T) {
	svc := &stubService{
		upstream: mcp.Upstream{Name: "demo", Mode: mcp.UpstreamModeHTTP},
		forward:  mcp.ForwardResult{StatusCode: http.StatusBadGateway, Body: []byte(": keepalive\n\n"), ContentType: "text/event-stream", ProtocolVersion: "2025-11-25"},
	}
	h := NewHandler(svc, stubServerService{}, nil, nil, nil, nil, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/mcp/demo", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping","params":{}}`))
	req.Header.Set("Accept", "application/json")
	w := httptest.NewRecorder()

	h.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "no JSON-RPC response in SSE stream") {
		t.Fatalf("expected decode error body, got %s", w.Body.String())
	}
}

func TestMCPForwardedNotificationReturnsAcceptedWithoutSSEBody(t *testing.T) {
	sseBody := []byte("event: message\n" +
		"data: {\"jsonrpc\":\"2.0\",\"id\":5,\"result\":{\"unrelated\":true}}\n\n")
	svc := &stubService{
		upstream: mcp.Upstream{Name: "demo", Mode: mcp.UpstreamModeHTTP},
		forward:  mcp.ForwardResult{StatusCode: http.StatusOK, Body: sseBody, ContentType: "text/event-stream", ProtocolVersion: "2025-11-25"},
	}
	h := NewHandler(svc, stubServerService{}, nil, nil, nil, nil, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/mcp/demo", strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/custom","params":{"x":1}}`))
	req.Header.Set("Accept", "application/json")
	w := httptest.NewRecorder()

	h.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", w.Code)
	}
	if body := strings.TrimSpace(w.Body.String()); body != "" {
		t.Fatalf("expected empty notification response body, got %q", body)
	}
}

func TestMCPUnknownNotificationPassThroughFallbackAccepted(t *testing.T) {
	svc := &stubService{fwdErr: context.Canceled}
	h := NewHandler(svc, stubServerService{}, nil, nil, nil, nil, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/mcp/demo", strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/custom","params":{"x":1}}`))
	w := httptest.NewRecorder()

	h.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", w.Code)
	}
}

func TestMCPMalformedJSONReturnsParseError(t *testing.T) {
	h := NewHandler(&stubService{}, stubServerService{}, nil, nil, nil, nil, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/mcp/demo", strings.NewReader(`{"jsonrpc":`))
	w := httptest.NewRecorder()

	h.Routes().ServeHTTP(w, req)

	if !strings.Contains(w.Body.String(), `"code":-32700`) {
		t.Fatalf("expected parse error, got %s", w.Body.String())
	}
}

func TestMCPGetOpenSSEStream(t *testing.T) {
	h := NewHandler(&stubService{}, stubServerService{}, nil, nil, nil, nil, nil, nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/mcp/demo", nil).WithContext(ctx)
	req.Header.Set("MCP-Protocol-Version", "2025-06-18")
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		h.Routes().ServeHTTP(w, req)
		close(done)
	}()
	// Cancel immediately — the SSE handler should exit once the context is done
	cancel()
	<-done
	if w.Code != http.StatusOK {
		t.Fatalf("GET /mcp expected 200 SSE, got %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("expected text/event-stream, got %s", ct)
	}
	if got := w.Header().Get("MCP-Protocol-Version"); got != "2025-06-18" {
		t.Fatalf("expected protocol header, got %q", got)
	}
	if !strings.Contains(w.Body.String(), ": atryum mcp ready\n\n") {
		t.Fatalf("expected initial SSE ready comment, got %q", w.Body.String())
	}
}

func TestMCPDeleteReturn405(t *testing.T) {
	h := NewHandler(&stubService{}, stubServerService{}, nil, nil, nil, nil, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodDelete, "/mcp/demo", nil)
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("DELETE expected 405, got %d", w.Code)
	}
}

func TestMCPRootRequiresServer(t *testing.T) {
	h := NewHandler(&stubService{tools: []mcp.Tool{{Name: "demo_tool"}}}, stubServerService{}, nil, nil, nil, nil, nil, nil, nil, nil)
	tests := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{name: "bare path", method: http.MethodPost, path: "/mcp", body: `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`},
		{name: "trailing slash", method: http.MethodPost, path: "/mcp/", body: `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`},
		{name: "root sse", method: http.MethodGet, path: "/mcp/"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
			w := httptest.NewRecorder()
			h.Routes().ServeHTTP(w, req)
			if w.Code != http.StatusNotFound {
				t.Fatalf("%s %s expected 404, got %d body=%s", tt.method, tt.path, w.Code, w.Body.String())
			}
			if strings.Contains(w.Body.String(), "demo_tool") {
				t.Fatalf("root MCP endpoint should not list tools, got %s", w.Body.String())
			}
		})
	}
}

func TestMCPToolsList(t *testing.T) {
	h := NewHandler(&stubService{tools: []mcp.Tool{{Name: "demo_tool"}}}, stubServerService{}, nil, nil, nil, nil, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/mcp/demo", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
	w := httptest.NewRecorder()

	h.Routes().ServeHTTP(w, req)

	if !strings.Contains(w.Body.String(), `"demo_tool"`) {
		t.Fatalf("expected tools list, got %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), `"atryum.rules.get"`) {
		t.Fatalf("did not expect synthetic rules tool, got %s", w.Body.String())
	}
}

func TestMCPToolsCallInterceptsInvocation(t *testing.T) {
	now := time.Now().UTC()
	svc := &stubService{invoke: invocation.InvocationResponse{InvocationID: "inv_123", ServerName: "demo", ToolName: "demo_tool", Status: invocation.StatusSucceeded, Input: json.RawMessage(`{"a":1}`), SubmittedAt: now, CompletedAt: &now, Result: json.RawMessage(`{"content":[{"type":"text","text":"ok"}]}`)}}
	h := NewHandler(svc, stubServerService{}, nil, nil, nil, nil, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/mcp/demo", strings.NewReader(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"demo_tool","arguments":{"a":1}}}`))
	w := httptest.NewRecorder()

	h.Routes().ServeHTTP(w, req)

	if svc.invokedReq == nil {
		t.Fatal("expected invocation request")
	}
	if svc.invokedReq.Tool != "demo_tool" {
		t.Fatalf("expected tool demo_tool, got %s", svc.invokedReq.Tool)
	}
	if svc.invokedReq.RequestID == nil || *svc.invokedReq.RequestID != "7" {
		t.Fatalf("expected request id 7, got %#v", svc.invokedReq.RequestID)
	}
	if !strings.Contains(w.Body.String(), `"text":"ok"`) {
		t.Fatalf("expected tool result, got %s", w.Body.String())
	}
}

func TestMCPNoAuthAgentIDQueryHintSetsIdentity(t *testing.T) {
	now := time.Now().UTC()
	svc := &stubService{invoke: invocation.InvocationResponse{InvocationID: "inv_123", ServerName: "demo", ToolName: "demo_tool", Status: invocation.StatusSucceeded, SubmittedAt: now, CompletedAt: &now, Result: json.RawMessage(`{"content":[{"type":"text","text":"ok"}]}`)}}
	h := NewHandler(svc, stubServerService{}, nil, nil, nil, nil, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/mcp/demo?agent_id=hunners-codex", strings.NewReader(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"demo_tool","arguments":{}}}`))
	w := httptest.NewRecorder()

	h.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if svc.invokedCtx == nil {
		t.Fatal("expected invocation context")
	}
	if got := auth.AgentIDFromContext(svc.invokedCtx); got != "hunners-codex" {
		t.Fatalf("agent id = %q", got)
	}
}

func TestMCPNoAuthAgentIDQueryHintRejectsInvalidCharacters(t *testing.T) {
	now := time.Now().UTC()
	svc := &stubService{invoke: invocation.InvocationResponse{InvocationID: "inv_123", ServerName: "demo", ToolName: "demo_tool", Status: invocation.StatusSucceeded, SubmittedAt: now, CompletedAt: &now, Result: json.RawMessage(`{"content":[{"type":"text","text":"ok"}]}`)}}
	h := NewHandler(svc, stubServerService{}, nil, nil, nil, nil, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/mcp/demo?agent_id=bad/agent", strings.NewReader(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"demo_tool","arguments":{}}}`))
	w := httptest.NewRecorder()

	h.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if svc.invokedCtx == nil {
		t.Fatal("expected invocation context")
	}
	if got := auth.AgentIDFromContext(svc.invokedCtx); got != "" {
		t.Fatalf("agent id = %q", got)
	}
}

func TestMCPAgentIDQueryHintIgnoredWhenAuthConfigured(t *testing.T) {
	now := time.Now().UTC()
	svc := &stubService{invoke: invocation.InvocationResponse{InvocationID: "inv_123", ServerName: "demo", ToolName: "demo_tool", Status: invocation.StatusSucceeded, SubmittedAt: now, CompletedAt: &now, Result: json.RawMessage(`{"content":[{"type":"text","text":"ok"}]}`)}}
	h := NewHandler(svc, stubServerService{}, nil, nil, nil, nil, nil, nil, nil, nil)
	h.SetAuthValidator(&auth.Validator{})
	h.SetAuthDebugSkipVerify(true)
	req := httptest.NewRequest(http.MethodPost, "/mcp/demo?agent_id=hunners-codex", strings.NewReader(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"demo_tool","arguments":{}}}`))
	w := httptest.NewRecorder()

	h.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if svc.invokedCtx == nil {
		t.Fatal("expected invocation context")
	}
	if got := auth.AgentIDFromContext(svc.invokedCtx); got != "" {
		t.Fatalf("agent id = %q", got)
	}
}

func TestAgentRulesListsApplicableRulesAndDisposition(t *testing.T) {
	rules := &stubRulesRepo{rules: []store.Rule{
		{ID: "bash-deny", Action: invocation.RuleActionAutoDeny, ServerPatterns: []string{"amp"}, ToolPatterns: []string{"Bash"}, Enabled: true, Order: 0},
		{ID: "read-auto", Action: invocation.RuleActionAutoApprove, ServerPatterns: []string{"amp"}, ToolPatterns: []string{"Read"}, Description: "Read is safe", Enabled: true, Order: 1},
		{ID: "fallback-human", Action: invocation.RuleActionHumanApproval, ServerPatterns: []string{"*"}, ToolPatterns: []string{"*"}, Enabled: true, Order: 2},
		{ID: "disabled", Action: invocation.RuleActionAutoApprove, ServerPatterns: []string{"amp"}, ToolPatterns: []string{"Read"}, Enabled: false, Order: 3},
	}}
	h := NewHandler(&stubService{}, stubServerService{}, nil, rules, nil, nil, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/agent/rules?agent_id=agent-007&source=amp&tool=Read", nil)
	w := httptest.NewRecorder()

	h.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	var resp AgentRulesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.AgentID != "agent-007" {
		t.Fatalf("expected agent_id agent-007, got %q", resp.AgentID)
	}
	if resp.Action != invocation.RuleActionAutoApprove {
		t.Fatalf("expected auto approve disposition, got %q", resp.Action)
	}
	if resp.MatchedRuleID == nil || *resp.MatchedRuleID != "read-auto" {
		t.Fatalf("expected matched rule read-auto, got %#v", resp.MatchedRuleID)
	}
	if len(resp.Items) != 3 {
		t.Fatalf("expected three applicable rules, got %#v", resp.Items)
	}
	if resp.Items[0].ID != "bash-deny" || resp.Items[1].ID != "read-auto" || resp.Items[2].ID != "fallback-human" {
		t.Fatalf("unexpected applicable rules order: %#v", resp.Items)
	}
}

func TestAgentRulesUsesDefaultAgentRecordForAgentScopedRules(t *testing.T) {
	defaultAgent := store.AgentRecord{ID: "agent-default", VMCUID: "vm-default", AgentIDs: "[]"}
	rules := &stubRulesRepo{rules: []store.Rule{
		{ID: "other-agent", Action: invocation.RuleActionAutoDeny, ServerPatterns: []string{"amp"}, ToolPatterns: []string{"Read"}, AgentCUIDs: []string{"agent-other"}, Enabled: true, Order: 0},
		{ID: "default-agent", Action: invocation.RuleActionAutoApprove, ServerPatterns: []string{"amp"}, ToolPatterns: []string{"Read"}, AgentCUIDs: []string{defaultAgent.ID}, Enabled: true, Order: 1},
		{ID: "fallback-human", Action: invocation.RuleActionHumanApproval, ServerPatterns: []string{"amp"}, ToolPatterns: []string{"Read"}, Enabled: true, Order: 2},
	}}
	agents := &stubAgentsRepo{byVMCUID: map[string]store.AgentRecord{defaultAgent.VMCUID: defaultAgent}}
	settings := &stubAgentSyncSettingsRepo{settings: store.AgentSyncSettings{DefaultAgentVMCUID: defaultAgent.VMCUID}}
	h := NewHandler(&stubService{}, stubServerService{}, nil, rules, agents, settings, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/agent/rules?source=amp&tool=Read", nil)
	w := httptest.NewRecorder()

	h.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	var resp AgentRulesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Action != invocation.RuleActionAutoApprove {
		t.Fatalf("expected default-agent auto approve disposition, got %q", resp.Action)
	}
	if resp.MatchedRuleID == nil || *resp.MatchedRuleID != "default-agent" {
		t.Fatalf("expected matched rule default-agent, got %#v", resp.MatchedRuleID)
	}
	if len(resp.Items) != 2 {
		t.Fatalf("expected default-scoped and fallback rules, got %#v", resp.Items)
	}
	if resp.Items[0].ID != "default-agent" || resp.Items[1].ID != "fallback-human" {
		t.Fatalf("unexpected applicable rules order: %#v", resp.Items)
	}
}

func TestMCPToolsListAnnotatesEffectiveAction(t *testing.T) {
	rules := &stubRulesRepo{rules: []store.Rule{
		{ID: "read-auto", Action: invocation.RuleActionAutoApprove, ServerPatterns: []string{"demo"}, ToolPatterns: []string{"Read"}, Enabled: true, Order: 0},
		{ID: "bash-deny", Action: invocation.RuleActionAutoDeny, ServerPatterns: []string{"demo"}, ToolPatterns: []string{"Bash"}, Enabled: true, Order: 1},
	}}
	svc := &stubService{tools: []mcp.Tool{{Name: "Read", Description: "read a file"}, {Name: "Bash", Description: "run a shell command"}, {Name: "Other"}}}
	h := NewHandler(svc, stubServerService{}, nil, rules, nil, nil, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/mcp/demo", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
	w := httptest.NewRecorder()

	h.Routes().ServeHTTP(w, req)

	var rpcResp struct {
		Result struct {
			Tools []struct {
				Name        string `json:"name"`
				Description string `json:"description"`
				Annotations *struct {
					Atryum struct {
						EffectiveAction string `json:"effective_action"`
						MatchedRuleID   string `json:"matched_rule_id"`
					} `json:"atryum"`
				} `json:"annotations"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &rpcResp); err != nil {
		t.Fatal(err)
	}
	byName := map[string]int{}
	for i, t := range rpcResp.Result.Tools {
		byName[t.Name] = i
	}
	readTool := rpcResp.Result.Tools[byName["Read"]]
	if readTool.Annotations == nil || readTool.Annotations.Atryum.EffectiveAction != invocation.RuleActionAutoApprove {
		t.Fatalf("Read tool annotations: %#v", readTool.Annotations)
	}
	if readTool.Annotations.Atryum.MatchedRuleID != "read-auto" {
		t.Fatalf("Read matched rule: %q", readTool.Annotations.Atryum.MatchedRuleID)
	}
	if !strings.HasPrefix(readTool.Description, "[atryum policy: auto_approve]") {
		t.Fatalf("Read description prefix: %q", readTool.Description)
	}
	bashTool := rpcResp.Result.Tools[byName["Bash"]]
	if bashTool.Annotations == nil || bashTool.Annotations.Atryum.EffectiveAction != invocation.RuleActionAutoDeny {
		t.Fatalf("Bash tool annotations: %#v", bashTool.Annotations)
	}
	otherTool := rpcResp.Result.Tools[byName["Other"]]
	if otherTool.Annotations == nil || otherTool.Annotations.Atryum.EffectiveAction != invocation.RuleActionHumanApproval {
		t.Fatalf("Other tool annotations: %#v", otherTool.Annotations)
	}
	if _, ok := byName["atryum.rules.get"]; ok {
		t.Fatalf("did not expect synthetic rules tool in tools/list")
	}
}

func TestMCPToolsListAnnotationsUseDefaultAgentScopedRules(t *testing.T) {
	defaultAgent := store.AgentRecord{ID: "agent-default", VMCUID: "vm-default", AgentIDs: "[]"}
	rules := &stubRulesRepo{rules: []store.Rule{
		{ID: "bash-deny-other", Action: invocation.RuleActionAutoDeny, ServerPatterns: []string{"demo"}, ToolPatterns: []string{"Bash"}, AgentCUIDs: []string{"agent-other"}, Enabled: true, Order: 0},
		{ID: "bash-auto-default", Action: invocation.RuleActionAutoApprove, ServerPatterns: []string{"demo"}, ToolPatterns: []string{"Bash"}, AgentCUIDs: []string{defaultAgent.ID}, Enabled: true, Order: 1},
		{ID: "bash-human-fallback", Action: invocation.RuleActionHumanApproval, ServerPatterns: []string{"demo"}, ToolPatterns: []string{"Bash"}, Enabled: true, Order: 2},
	}}
	svc := &stubService{tools: []mcp.Tool{{Name: "Bash", Description: "run a shell command"}}}
	agents := &stubAgentsRepo{byVMCUID: map[string]store.AgentRecord{defaultAgent.VMCUID: defaultAgent}}
	settings := &stubAgentSyncSettingsRepo{settings: store.AgentSyncSettings{DefaultAgentVMCUID: defaultAgent.VMCUID}}
	h := NewHandler(svc, stubServerService{}, nil, rules, agents, settings, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/mcp/demo", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
	w := httptest.NewRecorder()

	h.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	var rpcResp struct {
		Result struct {
			Tools []struct {
				Name        string `json:"name"`
				Description string `json:"description"`
				Annotations *struct {
					Atryum struct {
						EffectiveAction string `json:"effective_action"`
						MatchedRuleID   string `json:"matched_rule_id"`
					} `json:"atryum"`
				} `json:"annotations"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &rpcResp); err != nil {
		t.Fatal(err)
	}
	if len(rpcResp.Result.Tools) == 0 {
		t.Fatalf("expected tools, got %#v", rpcResp.Result.Tools)
	}
	bashTool := rpcResp.Result.Tools[0]
	if bashTool.Name != "Bash" {
		t.Fatalf("expected Bash tool, got %q", bashTool.Name)
	}
	if bashTool.Annotations == nil || bashTool.Annotations.Atryum.EffectiveAction != invocation.RuleActionAutoApprove {
		t.Fatalf("Bash tool annotations: %#v", bashTool.Annotations)
	}
	if bashTool.Annotations.Atryum.MatchedRuleID != "bash-auto-default" {
		t.Fatalf("Bash matched rule: %q", bashTool.Annotations.Atryum.MatchedRuleID)
	}
	if !strings.HasPrefix(bashTool.Description, "[atryum policy: auto_approve]") {
		t.Fatalf("Bash description prefix: %q", bashTool.Description)
	}
}

func TestMCPToolsCallDenialIncludesRulesContext(t *testing.T) {
	now := time.Now().UTC()
	rules := &stubRulesRepo{rules: []store.Rule{
		{ID: "bash-deny", Action: invocation.RuleActionAutoDeny, ServerPatterns: []string{"demo"}, ToolPatterns: []string{"Bash"}, Enabled: true, Order: 0},
	}}
	svc := &stubService{invoke: invocation.InvocationResponse{
		InvocationID: "inv_denied", ServerName: "demo", ToolName: "Bash",
		Status:      invocation.StatusDenied,
		SubmittedAt: now, CompletedAt: &now,
		Error: json.RawMessage(`{"content":[{"type":"text","text":"Tool call denied by approval rule (auto_deny)."}],"isError":true}`),
	}}
	h := NewHandler(svc, stubServerService{}, nil, rules, nil, nil, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/mcp/demo", strings.NewReader(`{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"Bash","arguments":{"cmd":"ls"}}}`))
	w := httptest.NewRecorder()

	h.Routes().ServeHTTP(w, req)

	var rpcResp struct {
		Result struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &rpcResp); err != nil {
		t.Fatal(err)
	}
	if !rpcResp.Result.IsError {
		t.Fatalf("expected isError=true, got %#v", rpcResp.Result)
	}
	if len(rpcResp.Result.Content) < 2 {
		t.Fatalf("expected denial text plus rules context, got %#v", rpcResp.Result.Content)
	}
	last := rpcResp.Result.Content[len(rpcResp.Result.Content)-1].Text
	if !strings.Contains(last, "Atryum approval rules") || !strings.Contains(last, "bash-deny") {
		t.Fatalf("expected rules context in denial result, got %q", last)
	}
}

func TestAdminInvocationsResponsesIncludeServerToolAndInput(t *testing.T) {
	now := time.Now().UTC()
	svc := &stubService{invoke: invocation.InvocationResponse{InvocationID: "inv_123", ServerName: "demo-server", ToolName: "demo_tool", Status: invocation.StatusSucceeded, Input: json.RawMessage(`{"issue":123,"verbose":true}`), SubmittedAt: now, CompletedAt: &now}}
	h := NewHandler(svc, stubServerService{}, nil, nil, nil, nil, nil, nil, nil, nil)

	t.Run("list", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/invocations", nil)
		w := httptest.NewRecorder()
		h.Routes().ServeHTTP(w, req)
		if !strings.Contains(w.Body.String(), `"server_name":"demo-server"`) {
			t.Fatalf("expected server_name in list response, got %s", w.Body.String())
		}
		if !strings.Contains(w.Body.String(), `"tool_name":"demo_tool"`) {
			t.Fatalf("expected tool_name in list response, got %s", w.Body.String())
		}
		if !strings.Contains(w.Body.String(), `"input":{"issue":123,"verbose":true}`) {
			t.Fatalf("expected input in list response, got %s", w.Body.String())
		}
	})

	t.Run("detail", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/invocations/inv_123", nil)
		w := httptest.NewRecorder()
		h.Routes().ServeHTTP(w, req)
		if !strings.Contains(w.Body.String(), `"server_name":"demo-server"`) {
			t.Fatalf("expected server_name in detail response, got %s", w.Body.String())
		}
		if !strings.Contains(w.Body.String(), `"tool_name":"demo_tool"`) {
			t.Fatalf("expected tool_name in detail response, got %s", w.Body.String())
		}
		if !strings.Contains(w.Body.String(), `"input":{"issue":123,"verbose":true}`) {
			t.Fatalf("expected input in detail response, got %s", w.Body.String())
		}
	})
}

// TestInvocationsByVMCUID tests the invocationsByVMCUID handler directly,
// bypassing the API key middleware (same pattern as TestAgentIDsUsesAgentsTable).
func TestInvocationsByVMCUID(t *testing.T) {
	inv := invocation.InvocationResponse{
		InvocationID: "inv_vm_1",
		ServerName:   "amp",
		ToolName:     "Read",
		Status:       invocation.StatusSucceeded,
	}
	svc := &stubService{invoke: inv}

	t.Run("returns invocations for known vm_cuid", func(t *testing.T) {
		agents := &stubAgentsRepo{
			byVMCUID: map[string]store.AgentRecord{
				"mdl_abc": {ID: "agent_1", AgentIDs: `["worker-1"]`, Enabled: true},
			},
		}
		h := NewHandler(svc, stubServerService{}, nil, nil, agents, nil, nil, nil, nil, nil)
		req := httptest.NewRequest(http.MethodGet, "/models/mdl_abc/invocations", nil)
		w := httptest.NewRecorder()
		h.invocationsByVMCUID(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
		}
		var resp invocation.InvocationListResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		if len(resp.Items) != 1 || resp.Items[0].InvocationID != "inv_vm_1" {
			t.Fatalf("unexpected items: %+v", resp.Items)
		}
	})

	t.Run("returns 404 for unknown vm_cuid", func(t *testing.T) {
		agents := &stubAgentsRepo{} // byVMCUID is nil — stub returns no rows error
		h := NewHandler(svc, stubServerService{}, nil, nil, agents, nil, nil, nil, nil, nil)
		req := httptest.NewRequest(http.MethodGet, "/models/mdl_unknown/invocations", nil)
		w := httptest.NewRecorder()
		h.invocationsByVMCUID(w, req)
		if w.Code != http.StatusNotFound {
			t.Fatalf("expected 404, got %d body=%s", w.Code, w.Body.String())
		}
	})

	t.Run("returns empty list when agent has no agent_ids", func(t *testing.T) {
		agents := &stubAgentsRepo{
			byVMCUID: map[string]store.AgentRecord{
				"mdl_noids": {ID: "agent_2", AgentIDs: `[]`, Enabled: true},
			},
		}
		h := NewHandler(svc, stubServerService{}, nil, nil, agents, nil, nil, nil, nil, nil)
		req := httptest.NewRequest(http.MethodGet, "/models/mdl_noids/invocations", nil)
		w := httptest.NewRecorder()
		h.invocationsByVMCUID(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
		}
		var resp invocation.InvocationListResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		if len(resp.Items) != 0 || resp.Total != 0 {
			t.Fatalf("expected empty list, got %+v", resp)
		}
	})

	t.Run("returns 400 when vm_cuid missing from path", func(t *testing.T) {
		agents := &stubAgentsRepo{}
		h := NewHandler(svc, stubServerService{}, nil, nil, agents, nil, nil, nil, nil, nil)
		req := httptest.NewRequest(http.MethodGet, "/models//invocations", nil)
		w := httptest.NewRecorder()
		h.invocationsByVMCUID(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d body=%s", w.Code, w.Body.String())
		}
	})
}
