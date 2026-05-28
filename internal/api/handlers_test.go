package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"atryum/internal/invocation"
	"atryum/internal/mcp"
	"atryum/internal/store"
)

type stubService struct {
	tools    []mcp.Tool
	invoke   invocation.InvocationResponse
	invErr   error
	listErr  error
	upstream mcp.Upstream
	forward  mcp.ForwardResult
	fwdErr   error

	invokedReq *invocation.CreateInvocationRequest
	invokedCtx context.Context
}

func (s *stubService) Invoke(ctx context.Context, req invocation.CreateInvocationRequest) (invocation.InvocationResponse, error) {
	s.invokedReq = &req
	s.invokedCtx = ctx
	return s.invoke, s.invErr
}
func (s *stubService) ListTools(context.Context, string) ([]mcp.Tool, error) {
	return s.tools, s.listErr
}
func (s *stubService) ListAllTools(context.Context) ([]mcp.Tool, error) {
	return s.tools, s.listErr
}
func (s *stubService) ResolveToolServer(_ context.Context, _ string) (string, error) {
	return s.upstream.Name, nil
}
func (s *stubService) Get(context.Context, string) (invocation.InvocationResponse, error) {
	return s.invoke, nil
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
func (s *stubService) Submit(context.Context, invocation.ExternalSubmitRequest) (invocation.InvocationResponse, error) {
	return invocation.InvocationResponse{}, nil
}
func (s *stubService) SetSummary(context.Context, string, string) (invocation.InvocationResponse, error) {
	return invocation.InvocationResponse{}, nil
}
func (s *stubService) RecordExecution(context.Context, string, invocation.ExternalExecutionUpdate) (invocation.InvocationResponse, error) {
	return invocation.InvocationResponse{}, nil
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

func TestMCPInitializeNegotiatesProtocolVersion(t *testing.T) {
	h := NewHandler(&stubService{}, stubServerService{}, nil, nil, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/mcp/demo", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("MCP-Protocol-Version", "2025-11-25")
	w := httptest.NewRecorder()

	h.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if got := w.Header().Get("MCP-Protocol-Version"); got != "2025-11-25" {
		t.Fatalf("expected negotiated header, got %q", got)
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	result := resp["result"].(map[string]any)
	if result["protocolVersion"] != "2025-11-25" {
		t.Fatalf("expected protocol version 2025-11-25, got %#v", result["protocolVersion"])
	}
	instructions, ok := result["instructions"].(string)
	if !ok || !strings.Contains(instructions, "atryum.rules.get") {
		t.Fatalf("expected initialize instructions to mention atryum.rules.get, got %#v", result["instructions"])
	}
}

func TestMCPInitializedNotificationReturnsAccepted(t *testing.T) {
	h := NewHandler(&stubService{}, stubServerService{}, nil, nil, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/mcp/demo", strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`))
	w := httptest.NewRecorder()

	h.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", w.Code)
	}
}

func TestMCPPingPassThrough(t *testing.T) {
	svc := &stubService{upstream: mcp.Upstream{Name: "demo", Mode: mcp.UpstreamModeHTTP}, forward: mcp.ForwardResult{StatusCode: http.StatusOK, Body: []byte(`{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`), ContentType: "application/json", ProtocolVersion: "2025-11-25"}}
	h := NewHandler(svc, stubServerService{}, nil, nil, nil, nil, nil, nil)
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

func TestMCPUnknownNotificationPassThroughFallbackAccepted(t *testing.T) {
	svc := &stubService{fwdErr: context.Canceled}
	h := NewHandler(svc, stubServerService{}, nil, nil, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/mcp/demo", strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/custom","params":{"x":1}}`))
	w := httptest.NewRecorder()

	h.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", w.Code)
	}
}

func TestMCPMalformedJSONReturnsParseError(t *testing.T) {
	h := NewHandler(&stubService{}, stubServerService{}, nil, nil, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/mcp/demo", strings.NewReader(`{"jsonrpc":`))
	w := httptest.NewRecorder()

	h.Routes().ServeHTTP(w, req)

	if !strings.Contains(w.Body.String(), `"code":-32700`) {
		t.Fatalf("expected parse error, got %s", w.Body.String())
	}
}

func TestMCPGetOpenSSEStream(t *testing.T) {
	h := NewHandler(&stubService{}, stubServerService{}, nil, nil, nil, nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/mcp/demo", nil).WithContext(ctx)
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
}

func TestMCPDeleteReturn405(t *testing.T) {
	h := NewHandler(&stubService{}, stubServerService{}, nil, nil, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodDelete, "/mcp/demo", nil)
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("DELETE expected 405, got %d", w.Code)
	}
}

func TestMCPToolsList(t *testing.T) {
	h := NewHandler(&stubService{tools: []mcp.Tool{{Name: "demo_tool"}}}, stubServerService{}, nil, nil, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/mcp/demo", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
	w := httptest.NewRecorder()

	h.Routes().ServeHTTP(w, req)

	if !strings.Contains(w.Body.String(), `"demo_tool"`) {
		t.Fatalf("expected tools list, got %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"atryum.rules.get"`) {
		t.Fatalf("expected synthetic rules tool, got %s", w.Body.String())
	}
}

func TestMCPToolsCallInterceptsInvocation(t *testing.T) {
	now := time.Now().UTC()
	svc := &stubService{invoke: invocation.InvocationResponse{InvocationID: "inv_123", ServerName: "demo", ToolName: "demo_tool", Status: invocation.StatusSucceeded, Input: json.RawMessage(`{"a":1}`), SubmittedAt: now, CompletedAt: &now, Result: json.RawMessage(`{"content":[{"type":"text","text":"ok"}]}`)}}
	h := NewHandler(svc, stubServerService{}, nil, nil, nil, nil, nil, nil)
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

func TestMCPRulesToolReturnsApplicableRulesWithoutInvocation(t *testing.T) {
	rules := &stubRulesRepo{rules: []store.Rule{
		{ID: "read-auto", Action: invocation.RuleActionAutoApprove, ServerPatterns: []string{"demo"}, ToolPatterns: []string{"Read"}, AgentIDPattern: "*", Enabled: true, Order: 0},
	}}
	svc := &stubService{}
	h := NewHandler(svc, stubServerService{}, nil, rules, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/mcp/demo", strings.NewReader(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"atryum.rules.get","arguments":{"tool":"Read"}}}`))
	w := httptest.NewRecorder()

	h.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if svc.invokedReq != nil {
		t.Fatalf("rules tool should not create invocation, got %#v", svc.invokedReq)
	}
	var rpcResp struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &rpcResp); err != nil {
		t.Fatal(err)
	}
	if len(rpcResp.Result.Content) != 1 {
		t.Fatalf("expected one content item, got %#v", rpcResp.Result.Content)
	}
	if !strings.Contains(rpcResp.Result.Content[0].Text, `"read-auto"`) || !strings.Contains(rpcResp.Result.Content[0].Text, `"action": "auto_approve"`) {
		t.Fatalf("expected rules payload in tool result, got %s", rpcResp.Result.Content[0].Text)
	}
}

func TestAgentRulesListsApplicableRulesAndDisposition(t *testing.T) {
	rules := &stubRulesRepo{rules: []store.Rule{
		{ID: "other-agent", Action: invocation.RuleActionAutoDeny, ServerPatterns: []string{"amp"}, ToolPatterns: []string{"Read"}, AgentIDPattern: "agent-other", Enabled: true, Order: 0},
		{ID: "read-auto", Action: invocation.RuleActionAutoApprove, ServerPatterns: []string{"amp"}, ToolPatterns: []string{"Read"}, AgentIDPattern: "agent-007", Description: "Read is safe", Enabled: true, Order: 1},
		{ID: "fallback-human", Action: invocation.RuleActionHumanApproval, ServerPatterns: []string{"*"}, ToolPatterns: []string{"*"}, AgentIDPattern: "*", Enabled: true, Order: 2},
		{ID: "disabled", Action: invocation.RuleActionAutoApprove, ServerPatterns: []string{"amp"}, ToolPatterns: []string{"Bash"}, AgentIDPattern: "agent-007", Enabled: false, Order: 3},
	}}
	h := NewHandler(&stubService{}, stubServerService{}, nil, rules, nil, nil, nil, nil)
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
	if len(resp.Items) != 2 {
		t.Fatalf("expected two applicable rules, got %#v", resp.Items)
	}
	if resp.Items[0].ID != "read-auto" || resp.Items[1].ID != "fallback-human" {
		t.Fatalf("unexpected applicable rules order: %#v", resp.Items)
	}
}

func TestMCPToolsListAnnotatesEffectiveAction(t *testing.T) {
	rules := &stubRulesRepo{rules: []store.Rule{
		{ID: "read-auto", Action: invocation.RuleActionAutoApprove, ServerPatterns: []string{"demo"}, ToolPatterns: []string{"Read"}, AgentIDPattern: "*", Enabled: true, Order: 0},
		{ID: "bash-deny", Action: invocation.RuleActionAutoDeny, ServerPatterns: []string{"demo"}, ToolPatterns: []string{"Bash"}, AgentIDPattern: "*", Enabled: true, Order: 1},
	}}
	svc := &stubService{tools: []mcp.Tool{{Name: "Read", Description: "read a file"}, {Name: "Bash", Description: "run a shell command"}, {Name: "Other"}}}
	h := NewHandler(svc, stubServerService{}, nil, rules, nil, nil, nil, nil)
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
	rulesTool := rpcResp.Result.Tools[byName["atryum.rules.get"]]
	if rulesTool.Annotations != nil {
		t.Fatalf("synthetic rules tool should not be annotated, got %#v", rulesTool.Annotations)
	}
}

func TestMCPToolsCallDenialIncludesRulesContext(t *testing.T) {
	now := time.Now().UTC()
	rules := &stubRulesRepo{rules: []store.Rule{
		{ID: "bash-deny", Action: invocation.RuleActionAutoDeny, ServerPatterns: []string{"demo"}, ToolPatterns: []string{"Bash"}, AgentIDPattern: "*", Enabled: true, Order: 0},
	}}
	svc := &stubService{invoke: invocation.InvocationResponse{
		InvocationID: "inv_denied", ServerName: "demo", ToolName: "Bash",
		Status:      invocation.StatusDenied,
		SubmittedAt: now, CompletedAt: &now,
		Error: json.RawMessage(`{"content":[{"type":"text","text":"Tool call denied by approval rule (auto_deny)."}],"isError":true}`),
	}}
	h := NewHandler(svc, stubServerService{}, nil, rules, nil, nil, nil, nil)
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
	h := NewHandler(svc, stubServerService{}, nil, nil, nil, nil, nil, nil)

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
