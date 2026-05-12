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
}

func (s *stubService) Invoke(_ context.Context, req invocation.CreateInvocationRequest) (invocation.InvocationResponse, error) {
	s.invokedReq = &req
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
func (s *stubService) Events(context.Context, string, invocation.EventListFilter) (invocation.EventListResponse, error) {
	return invocation.EventListResponse{}, nil
}
func (s *stubService) Approve(context.Context, string) error      { return nil }
func (s *stubService) Deny(context.Context, string, string) error { return nil }
func (s *stubService) Submit(context.Context, invocation.ExternalSubmitRequest) (invocation.InvocationResponse, error) {
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

func TestMCPInitializeNegotiatesProtocolVersion(t *testing.T) {
	h := NewHandler(&stubService{}, stubServerService{}, nil, nil)
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
}

func TestMCPInitializedNotificationReturnsAccepted(t *testing.T) {
	h := NewHandler(&stubService{}, stubServerService{}, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/mcp/demo", strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`))
	w := httptest.NewRecorder()

	h.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", w.Code)
	}
}

func TestMCPPingPassThrough(t *testing.T) {
	svc := &stubService{upstream: mcp.Upstream{Name: "demo", Mode: mcp.UpstreamModeHTTP}, forward: mcp.ForwardResult{StatusCode: http.StatusOK, Body: []byte(`{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`), ContentType: "application/json", ProtocolVersion: "2025-11-25"}}
	h := NewHandler(svc, stubServerService{}, nil, nil)
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
	h := NewHandler(svc, stubServerService{}, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/mcp/demo", strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/custom","params":{"x":1}}`))
	w := httptest.NewRecorder()

	h.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", w.Code)
	}
}

func TestMCPMalformedJSONReturnsParseError(t *testing.T) {
	h := NewHandler(&stubService{}, stubServerService{}, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/mcp/demo", strings.NewReader(`{"jsonrpc":`))
	w := httptest.NewRecorder()

	h.Routes().ServeHTTP(w, req)

	if !strings.Contains(w.Body.String(), `"code":-32700`) {
		t.Fatalf("expected parse error, got %s", w.Body.String())
	}
}

func TestMCPGetOpenSSEStream(t *testing.T) {
	h := NewHandler(&stubService{}, stubServerService{}, nil, nil)
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
	h := NewHandler(&stubService{}, stubServerService{}, nil, nil)
	req := httptest.NewRequest(http.MethodDelete, "/mcp/demo", nil)
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("DELETE expected 405, got %d", w.Code)
	}
}

func TestMCPToolsList(t *testing.T) {
	h := NewHandler(&stubService{tools: []mcp.Tool{{Name: "demo_tool"}}}, stubServerService{}, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/mcp/demo", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
	w := httptest.NewRecorder()

	h.Routes().ServeHTTP(w, req)

	if !strings.Contains(w.Body.String(), `"demo_tool"`) {
		t.Fatalf("expected tools list, got %s", w.Body.String())
	}
}

func TestMCPToolsCallInterceptsInvocation(t *testing.T) {
	now := time.Now().UTC()
	svc := &stubService{invoke: invocation.InvocationResponse{InvocationID: "inv_123", ServerName: "demo", ToolName: "demo_tool", Status: invocation.StatusSucceeded, Input: json.RawMessage(`{"a":1}`), SubmittedAt: now, CompletedAt: &now, Result: json.RawMessage(`{"content":[{"type":"text","text":"ok"}]}`)}}
	h := NewHandler(svc, stubServerService{}, nil, nil)
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

func TestAdminInvocationsResponsesIncludeServerToolAndInput(t *testing.T) {
	now := time.Now().UTC()
	svc := &stubService{invoke: invocation.InvocationResponse{InvocationID: "inv_123", ServerName: "demo-server", ToolName: "demo_tool", Status: invocation.StatusSucceeded, Input: json.RawMessage(`{"issue":123,"verbose":true}`), SubmittedAt: now, CompletedAt: &now}}
	h := NewHandler(svc, stubServerService{}, nil, nil)

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
