package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// Kuadrant / Red Hat MCP Gateway (contracts/kuadrant.md).
//
// A1, the guardrails hook: the gateway's NeMo-style client POSTs
// {url}/v1/checks with {"messages":[{"role","name","content"}], ...}. Role
// "user" is a tool call about to run (name = tool, content = its arguments as
// a JSON string); role "assistant" is its result. It expects HTTP 200 with
// {"status":"passed"|"blocked", ...} and waits 8 seconds (hard-coded). It
// sends no auth header, no user, server or session: the key travels in the
// URL path (see PathTokenToBearer) and the user is recorded as unknown.
//
// A2, an AuthPolicy (Authorino) HTTP callout: sees the user, tool and server
// but not the arguments. It posts the generic Check shape built in CEL.

const kuadrantBudgetMS = 6500

type nemoRequest struct {
	Messages []struct {
		Role    string `json:"role"`
		Name    string `json:"name"`
		Content string `json:"content"`
	} `json:"messages"`
}

type nemoResponse struct {
	Status  string `json:"status"`
	Rail    string `json:"rail,omitempty"`
	Content string `json:"content,omitempty"`
}

func (h *Handler) serveKuadrantGuardrail(w http.ResponseWriter, r *http.Request, gw caller) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	var req nemoRequest
	if err == nil {
		err = json.Unmarshal(body, &req)
	}
	if err != nil || len(req.Messages) == 0 {
		writeJSON(w, http.StatusOK, blocked("Agent Authority could not read this request."))
		return
	}
	if capture(r, body) {
		writeJSON(w, http.StatusOK, blocked("Agent Authority capture mode: call blocked while its payload is recorded."))
		return
	}
	m := req.Messages[0]
	tool := strings.TrimSpace(m.Name)
	if m.Role == "assistant" {
		h.Record(r.Context(), gw, Outcome{Tool: tool, Status: "completed", Result: textResult(m.Content)})
		// The tool has already run; never withhold its result.
		writeJSON(w, http.StatusOK, nemoResponse{Status: "passed"})
		return
	}
	args, _ := json.Marshal(m.Content) // a JSON string; parseArguments unwraps it
	res := h.Decide(r.Context(), gw, Check{Tool: tool, Arguments: args, DeadlineMS: kuadrantBudgetMS})
	if res.Verdict == VerdictAllow {
		writeJSON(w, http.StatusOK, nemoResponse{Status: "passed"})
		return
	}
	writeJSON(w, http.StatusOK, blocked(messageFor(res)))
}

func (h *Handler) serveKuadrantAuthz(w http.ResponseWriter, r *http.Request, gw caller) {
	var c Check
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(&c); err != nil {
		writeJSON(w, http.StatusOK, unreadable("invalid JSON body"))
		return
	}
	// AuthPolicy cannot see the arguments: every call to a tool looks the
	// same to rules on arguments and to approvals.
	c.Arguments = json.RawMessage(`{}`)
	if c.DeadlineMS == 0 {
		c.DeadlineMS = kuadrantBudgetMS
	}
	writeJSON(w, http.StatusOK, h.Decide(r.Context(), gw, c))
}

func blocked(reason string) nemoResponse {
	return nemoResponse{Status: "blocked", Rail: "agent-authority", Content: reason}
}

func textResult(text string) json.RawMessage {
	b, _ := json.Marshal(map[string]any{"content": []map[string]string{{"type": "text", "text": text}}})
	return b
}
