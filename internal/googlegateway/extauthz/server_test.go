package extauthz

import (
	"context"
	"testing"
	"time"

	authv3 "github.com/envoyproxy/go-control-plane/envoy/service/auth/v3"
	typev3 "github.com/envoyproxy/go-control-plane/envoy/type/v3"
	rpccode "google.golang.org/genproto/googleapis/rpc/code"

	"atryum/internal/googlegateway"
	"atryum/internal/invocation"
)

// fakeGW is a canned InvocationGateway: Submit returns a fixed terminal status.
type fakeGW struct {
	status invocation.Status
	reason string
}

func (f fakeGW) Submit(_ context.Context, _ invocation.ExternalSubmitRequest) (invocation.InvocationResponse, error) {
	r := invocation.InvocationResponse{InvocationID: "inv", Status: f.status}
	if f.status == invocation.StatusDenied && f.reason != "" {
		reason := f.reason
		r.Approval = &invocation.Approval{Status: "denied", Reason: &reason}
	}
	return r, nil
}

func (f fakeGW) Get(_ context.Context, _ string) (invocation.InvocationResponse, error) {
	return invocation.InvocationResponse{InvocationID: "inv", Status: f.status}, nil
}

func newServer(gw googlegateway.InvocationGateway) *Server {
	svc := googlegateway.NewService(gw, googlegateway.Config{
		PollInterval:    time.Millisecond,
		DecisionTimeout: 100 * time.Millisecond,
	})
	return New(svc, ":0")
}

func checkReq(ctxExt, headers map[string]string, body string) *authv3.CheckRequest {
	return &authv3.CheckRequest{
		Attributes: &authv3.AttributeContext{
			ContextExtensions: ctxExt,
			Request: &authv3.AttributeContext_Request{
				Http: &authv3.AttributeContext_HttpRequest{
					Headers: headers,
					Body:    body,
				},
			},
		},
	}
}

func TestCheck_Allow(t *testing.T) {
	s := newServer(fakeGW{status: invocation.StatusApproved})
	resp, err := s.Check(context.Background(), checkReq(map[string]string{"mcp.toolName": "bash"}, nil, ""))
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetStatus().GetCode() != int32(rpccode.Code_OK) {
		t.Fatalf("expected OK, got %d", resp.GetStatus().GetCode())
	}
	if resp.GetOkResponse() == nil {
		t.Fatal("expected ok_response on allow")
	}
}

func TestCheck_Deny_CarriesReasonAnd403(t *testing.T) {
	s := newServer(fakeGW{status: invocation.StatusDenied, reason: "no shell in prod"})
	resp, _ := s.Check(context.Background(), checkReq(map[string]string{"mcp.toolName": "bash"}, nil, ""))
	if resp.GetStatus().GetCode() != int32(rpccode.Code_PERMISSION_DENIED) {
		t.Fatalf("expected PERMISSION_DENIED, got %d", resp.GetStatus().GetCode())
	}
	d := resp.GetDeniedResponse()
	if d == nil || d.GetBody() != "no shell in prod" {
		t.Fatalf("expected deny body to carry the rule reason, got %+v", d)
	}
	if d.GetStatus().GetCode() != typev3.StatusCode_Forbidden {
		t.Fatalf("expected 403 Forbidden, got %v", d.GetStatus().GetCode())
	}
}

func TestCheck_ToolParsedFromMCPBody(t *testing.T) {
	s := newServer(fakeGW{status: invocation.StatusApproved})
	body := `{"method":"tools/call","params":{"name":"transfer_funds","arguments":{"amount":5}}}`
	resp, _ := s.Check(context.Background(), checkReq(nil, nil, body))
	if resp.GetStatus().GetCode() != int32(rpccode.Code_OK) {
		t.Fatal("expected allow with the tool parsed from the MCP body")
	}
}

func TestCheck_Unattributable_FailsClosed(t *testing.T) {
	s := newServer(fakeGW{status: invocation.StatusApproved}) // would allow, but no tool is identifiable
	resp, _ := s.Check(context.Background(), checkReq(nil, nil, ""))
	if resp.GetStatus().GetCode() != int32(rpccode.Code_PERMISSION_DENIED) {
		t.Fatal("expected fail-closed deny when no tool can be identified")
	}
}
