package gateway

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
)

// TrueFoundry custom guardrail (contracts/truefoundry.md).
//
// The gateway POSTs {requestBody, config, context} to a custom guardrail and
// expects HTTP 200 with {"verdict": bool, "message": "..."}; it waits about
// five seconds.
//
// Captured from the real gateway (validmind-dev, 2026-11-13) for
// mcp_tool_pre_invoke: requestBody is the tool's arguments and nothing else.
// Neither the body nor the headers name the tool or the MCP server. config
// echoes the guardrail integration (url, headers — including the secret the
// customer set — and any custom config). context.user carries subjectId,
// subjectSlug, subjectType, tenantName, teams and roles; context.claims (not
// context.metadata.claims, as the docs say) carries the verified JWT;
// context.metadata only has the gateway region and zone. No agent identity,
// session ID or request ID arrives.
//
// So the tool and server must come from the guardrail's own config
// ({"tool": ..., "server": ...}), one integration per tool, until TrueFoundry
// sends them. The other shapes below are kept for gateways or versions that
// do send them.

const trueFoundryBudgetMS = 3500

type tfyRequest struct {
	RequestBody  json.RawMessage `json:"requestBody"`
	ResponseBody json.RawMessage `json:"responseBody"`
	Config       json.RawMessage `json:"config"`
	Context      struct {
		User struct {
			SubjectID          string `json:"subjectId"`
			SubjectType        string `json:"subjectType"`
			SubjectSlug        string `json:"subjectSlug"`
			SubjectDisplayName string `json:"subjectDisplayName"`
		} `json:"user"`
		Metadata map[string]any `json:"metadata"`
		Claims   map[string]any `json:"claims"`
	} `json:"context"`
}

type tfyResponse struct {
	Verdict bool   `json:"verdict"`
	Message string `json:"message,omitempty"`
}

func (h *Handler) serveTrueFoundryPre(w http.ResponseWriter, r *http.Request, gw caller) {
	body, req, ok := readTrueFoundry(w, r)
	if !ok {
		return
	}
	if capture(r, body) {
		writeJSON(w, http.StatusOK, tfyResponse{Verdict: false, Message: "Agent Authority capture mode: call blocked while its payload is recorded."})
		return
	}
	c := trueFoundryCheck(r, req)
	res := h.Decide(r.Context(), gw, c)
	writeJSON(w, http.StatusOK, tfyResponse{Verdict: res.Verdict == VerdictAllow, Message: messageFor(res)})
}

func (h *Handler) serveTrueFoundryPost(w http.ResponseWriter, r *http.Request, gw caller) {
	body, req, ok := readTrueFoundry(w, r)
	if !ok {
		return
	}
	if capture(r, body) {
		writeJSON(w, http.StatusOK, tfyResponse{Verdict: true, Message: "captured"})
		return
	}
	c := trueFoundryCheck(r, req)
	// On the real gateway the post hook's requestBody is the tool result
	// (the same as responseBody), not the arguments, so it cannot key the
	// match; fall back to the newest running call of this tool.
	args := c.Arguments
	if looksLikeToolResult(req.RequestBody) {
		args = nil
	}
	o := Outcome{GatewayRequestID: c.GatewayRequestID, Tool: c.Tool, Server: c.Server, Arguments: args, User: c.User, Status: "completed", Result: req.ResponseBody}
	if isToolError(req.ResponseBody) {
		o.Status, o.Error, o.Result = "failed", req.ResponseBody, nil
	}
	h.Record(r.Context(), gw, o)
	// The tool has already run; never withhold its result.
	writeJSON(w, http.StatusOK, tfyResponse{Verdict: true})
}

func readTrueFoundry(w http.ResponseWriter, r *http.Request) ([]byte, tfyRequest, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	var req tfyRequest
	if err == nil {
		err = json.Unmarshal(body, &req)
	}
	if err != nil {
		writeJSON(w, http.StatusOK, tfyResponse{Verdict: false, Message: "Agent Authority could not read this request."})
		return nil, req, false
	}
	return body, req, true
}

// trueFoundryCheck maps a guardrail request onto the generic Check.
func trueFoundryCheck(r *http.Request, req tfyRequest) Check {
	c := Check{DeadlineMS: trueFoundryBudgetMS, Raw: nil}
	u := req.Context.User
	claims := req.Context.Claims
	if u.SubjectID != "" {
		c.User = &Principal{ID: u.SubjectID, Type: u.SubjectType, Display: firstNonEmpty(u.SubjectDisplayName, u.SubjectSlug)}
	} else if sub := stringAt(claims, "sub"); sub != "" {
		c.User = &Principal{ID: sub, Type: stringAt(claims, "userType"), Display: stringAt(claims, "email", "username")}
	}
	meta := req.Context.Metadata
	c.SessionID = stringAt(meta, "session_id", "sessionId", "mcp_session_id")
	c.GatewayRequestID = firstNonEmpty(r.Header.Get("X-Request-Id"), r.Header.Get("X-Tfy-Request-Id"), stringAt(meta, "request_id", "requestId"))

	var body, cfg map[string]any
	_ = json.Unmarshal(req.RequestBody, &body)
	_ = json.Unmarshal(req.Config, &cfg)
	params := mapAt(body, "params")
	// The guardrail's own config names the tool and server (the real
	// gateway sends neither); the UI's Config field may arrive nested.
	for _, m := range []map[string]any{cfg, mapAt(cfg, "config")} {
		c.Tool = firstNonEmpty(c.Tool, stringAt(m, "tool", "tool_name"))
		c.Server = firstNonEmpty(c.Server, stringAt(m, "server", "mcp_server", "mcp_server_name"))
	}
	// Then shapes other versions may send: an MCP params object, explicit
	// tool fields, or TrueFoundry's OPA-style metadata. A bare "name" is
	// trusted only inside params: requestBody is the arguments themselves,
	// so a top-level "name" is usually an argument.
	c.Tool = firstNonEmpty(c.Tool, stringAt(params, "name"), stringAt(body, "tool_name", "toolName"), stringAt(meta, "tool_name", "toolName"))
	c.Server = firstNonEmpty(c.Server, stringAt(body, "mcp_server_name", "mcpServerName", "server_name"), stringAt(meta, "mcp_server_name", "mcpServerName"))
	switch {
	case params != nil && params["arguments"] != nil:
		c.Arguments, _ = json.Marshal(params["arguments"])
	case stringAt(body, "tool_name", "toolName") != "":
		for _, k := range []string{"arguments", "tool_args", "toolArgs", "args", "input"} {
			if v, ok := body[k]; ok && v != nil {
				c.Arguments, _ = json.Marshal(v)
				break
			}
		}
	case len(req.RequestBody) > 0 && string(req.RequestBody) != "null":
		c.Arguments = req.RequestBody
	}
	if agent := r.Header.Get("X-Tfy-Agent-Id"); agent != "" {
		c.Agent = &Principal{ID: agent}
	}
	return c
}

// looksLikeToolResult reports an MCP CallToolResult shape.
func looksLikeToolResult(raw json.RawMessage) bool {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return false
	}
	_, content := m["content"]
	_, isErr := m["isError"]
	_, structured := m["structuredContent"]
	return content || isErr || structured
}

// isToolError reports an MCP result marked isError, or a JSON-RPC error.
func isToolError(raw json.RawMessage) bool {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return false
	}
	if b, _ := m["isError"].(bool); b {
		return true
	}
	if res := mapAt(m, "result"); res != nil {
		if b, _ := res["isError"].(bool); b {
			return true
		}
	}
	_, hasErr := m["error"]
	return hasErr
}

// messageFor is the text a gateway shows the caller.
func messageFor(res Result) string {
	if res.Reason != "" {
		return res.Reason
	}
	return res.Verdict
}

// capture logs the raw request with credentials redacted when ?capture=1 is
// set; the spike uses it to learn a gateway's real payload.
func capture(r *http.Request, body []byte) bool {
	if r.URL.Query().Get("capture") != "1" {
		return false
	}
	headers := map[string]string{}
	for k, v := range r.Header {
		lk := strings.ToLower(k)
		if lk == "authorization" || strings.Contains(lk, "token") || strings.Contains(lk, "authorization") || strings.Contains(lk, "cookie") || strings.Contains(lk, "key") {
			headers[k] = "[redacted]"
			continue
		}
		headers[k] = strings.Join(v, ",")
	}
	slog.Info("gateway_capture", "path", r.URL.Path, "headers", headers, "body", redactConfigHeaders(body))
	return true
}

// redactConfigHeaders hides config.headers values: TrueFoundry echoes the
// guardrail integration's headers, including the key AA issued, in every body.
func redactConfigHeaders(body []byte) string {
	var m map[string]any
	if json.Unmarshal(body, &m) != nil {
		return "[unparseable body withheld]"
	}
	if cfg := mapAt(m, "config"); cfg != nil {
		if h := mapAt(cfg, "headers"); h != nil {
			for k := range h {
				h[k] = "[redacted]"
			}
		}
	}
	out, _ := json.Marshal(m)
	return string(out)
}

func mapAt(m map[string]any, key string) map[string]any {
	if m == nil {
		return nil
	}
	v, _ := m[key].(map[string]any)
	return v
}

func stringAt(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}
