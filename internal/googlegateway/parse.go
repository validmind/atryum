package googlegateway

import "encoding/json"

// ExtractToolCall builds a ToolCall from the pieces an Envoy ext_authz
// CheckRequest carries. Agent Gateway surfaces agent identity and tool metadata
// through a mix of:
//   - context extensions / CEL attributes (e.g. the IAP authz extension exposes
//     iap.googleapis.com/mcp.toolName, mcp.tool.isReadOnly, request.auth.type),
//   - request headers, and
//   - for a CONTENT_AUTHZ policy, the request body (the MCP JSON-RPC tools/call).
//
// The exact attribute schema is still firming up (see the example README's open
// questions), so we read defensively from all three sources and let the rule
// engine match on whatever is present. Header keys are lower-cased by Envoy.
func ExtractToolCall(contextExtensions, headers map[string]string, body []byte) ToolCall {
	tc := ToolCall{Input: map[string]any{}}
	tc.AgentID = firstNonEmpty(
		contextExtensions["agent_id"],
		headers["x-agent-id"],
		headers["x-goog-agent-id"],
		headers["x-serverless-authorization"],
	)
	tc.Tool = firstNonEmpty(
		contextExtensions["mcp.toolName"],
		contextExtensions["tool_name"],
		headers["x-tool-name"],
	)
	tc.ServerName = firstNonEmpty(
		contextExtensions["mcp.serverName"],
		contextExtensions["mcp_server"],
		headers["x-mcp-server"],
	)
	tc.CallID = firstNonEmpty(headers["x-request-id"], contextExtensions["call_id"])

	// Body: an MCP JSON-RPC tools/call —
	//   {"method":"tools/call","params":{"name":"...","arguments":{...}}}
	if len(body) > 0 {
		var env struct {
			Method string `json:"method"`
			Params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		if json.Unmarshal(body, &env) == nil {
			if tc.Tool == "" {
				tc.Tool = env.Params.Name
			}
			if len(env.Params.Arguments) > 0 {
				tc.Input = env.Params.Arguments
			}
		}
	}
	return tc
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
