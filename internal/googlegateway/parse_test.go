package googlegateway

import "testing"

func TestExtractToolCall_FromContextExtensions(t *testing.T) {
	tc := ExtractToolCall(
		map[string]string{"mcp.toolName": "read_file", "agent_id": "agent-7", "mcp.serverName": "fs"},
		map[string]string{"x-request-id": "req-1"},
		nil,
	)
	if tc.Tool != "read_file" || tc.AgentID != "agent-7" || tc.ServerName != "fs" || tc.CallID != "req-1" {
		t.Fatalf("unexpected extraction: %+v", tc)
	}
}

func TestExtractToolCall_FromMCPBody(t *testing.T) {
	body := []byte(`{"method":"tools/call","params":{"name":"transfer_funds","arguments":{"amount":500,"to":"acct-9"}}}`)
	tc := ExtractToolCall(nil, nil, body)
	if tc.Tool != "transfer_funds" {
		t.Fatalf("expected tool from body, got %q", tc.Tool)
	}
	if tc.Input["amount"].(float64) != 500 || tc.Input["to"].(string) != "acct-9" {
		t.Fatalf("expected args from body, got %+v", tc.Input)
	}
}

func TestExtractToolCall_ContextExtensionWinsOverBody(t *testing.T) {
	body := []byte(`{"method":"tools/call","params":{"name":"body_tool"}}`)
	tc := ExtractToolCall(map[string]string{"mcp.toolName": "attr_tool"}, nil, body)
	if tc.Tool != "attr_tool" {
		t.Fatalf("expected context-extension tool to win, got %q", tc.Tool)
	}
}
