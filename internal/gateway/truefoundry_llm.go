package gateway

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
)

// TrueFoundry's AI Gateway, LLM hooks.
//
// A custom guardrail attached to "LLM Input" receives {requestBody, config,
// context}; attached to "LLM Output" it also receives responseBody. The
// bodies are in the shape of the route the agent called: Chat Completions on
// /chat/completions, Responses on /responses. So the format is read from
// each body, never assumed. The answer is always HTTP 200:
//
//	{"verdict": true}                                   let it through
//	{"verdict": false, "message": "…"}                  refuse it
//	{"verdict": true, "transformed": true, "result": …} replace the body
//
// The last needs the guardrail's operation set to Mutate. The gateway waits
// about five seconds.
//
// Checked against a live gateway on 2026-11-17 and 2026-11-18
// (TestTrueFoundryLLMRealPayloadShape and TestTrueFoundryLLMResponsesRoute
// hold the bodies it sent):
//
//   - responseBody carries the model's tool calls (choices[].message.tool_calls,
//     or output[] items on the Responses route), and a Mutate guardrail's
//     result replaces the whole response the caller receives.
//   - LLM Output guardrails are skipped for streamed responses: the stream's
//     tool calls reach the agent unchecked. So the input hook refuses a
//     streamed request that carries tools, unless the guardrail's config
//     says {"on_stream": "allow"}.
//   - The person is context.user (an opaque subjectId, their email as
//     subjectSlug); context.claims carries the verified JWT. A caller using a
//     virtual account's token arrives as that account (subjectType
//     "virtualaccount", its name as subjectSlug); no other agent identity is
//     sent. A session ID arrives only if the caller sends
//     X-TFY-METADATA: {"session_id": "…"}; the gateway sends no request ID.
//   - config echoes the guardrail integration, including any Headers the
//     customer set: a key put there reaches AA in every body (capture mode
//     redacts it). Put the key in the integration's Auth Data (bearer-auth)
//     instead: it then arrives only as the Authorization header.
//   - The gateway waits for a guardrail as long as the tenant's one guardrail
//     timeout (5 s by default, per TrueFoundry; 15 s on the tenant tested).
//     On a timeout, Enforce refuses the request and Enforce But Ignore On
//     Error sends the response on unchecked. Mutate guardrails run in turn.
//   - A refusal reaches the caller as HTTP 400 "guardrail_checks_failed";
//     the message returned here is under guardrail_checks in that body, not
//     in its top-level message. An unreachable guardrail set to Enforce is
//     refused the same way.
//   - A new guardrail group can take several minutes before the gateway
//     finds it; until then every call it covers fails with 403 or 404.
//
// Guardrail config read here: {"operation": "mutate"} on a guardrail whose
// operation is Mutate, so a response with a withheld call is rewritten;
// without it (TrueFoundry does not send the operation) that response is
// refused whole, which is safe under Validate too. And {"on_stream": "allow"}.

// trueFoundryMutates reports whether the guardrail's config says it may
// rewrite. What a Validate guardrail does with a rewritten body is not
// known, and it may pass the original, withheld call included: rewrite only
// when told the guardrail is Mutate.
func trueFoundryMutates(cfg map[string]any) bool {
	return strings.EqualFold(stringAt(cfg, "operation"), "mutate")
}

// trueFoundryBudgetMS is how long a decision may take: the gateway waits
// about five seconds.
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

// tfyWho is who a TrueFoundry request is for, as the gateway verified it.
type tfyWho struct {
	User      *Principal
	Agent     *Principal
	SessionID string
}

func trueFoundryWho(r *http.Request, req tfyRequest) tfyWho {
	var who tfyWho
	u, claims := req.Context.User, req.Context.Claims
	if u.SubjectID != "" {
		who.User = &Principal{ID: u.SubjectID, Type: u.SubjectType, Display: firstNonEmpty(u.SubjectDisplayName, u.SubjectSlug)}
	} else if sub := stringAt(claims, "sub"); sub != "" {
		who.User = &Principal{ID: sub, Type: stringAt(claims, "userType"), Display: stringAt(claims, "email", "username")}
	}
	who.SessionID = stringAt(req.Context.Metadata, "session_id", "sessionId")
	if agent := r.Header.Get("X-Tfy-Agent-Id"); agent != "" {
		who.Agent = &Principal{ID: agent}
	}
	return who
}

func (h *Handler) serveTrueFoundryLLMInput(w http.ResponseWriter, r *http.Request, gw caller) {
	body, req, ok := readTrueFoundryLLM(w, r)
	if !ok {
		return
	}
	if h.opts.Capture && capture(r, body) {
		writeJSON(w, http.StatusOK, tfyLLMResponse{Verdict: false, Message: "Agent Authority capture mode: request refused while its payload is recorded."})
		return
	}
	cfg := trueFoundryConfig(req)
	if parsed, err := decodeObject(req.RequestBody); err == nil {
		if stream, _ := parsed["stream"].(bool); stream && declaresTools(parsed) && stringAt(cfg, "on_stream") != "allow" {
			writeJSON(w, http.StatusOK, tfyLLMResponse{Verdict: false, Message: "Agent Authority cannot check the tool calls of a streamed response at this gateway. Send the request without \"stream\": true."})
			return
		}
	}
	who := trueFoundryWho(r, req)
	res := h.CheckInput(r.Context(), gw, LLMInput{Request: req.RequestBody, User: who.User, Agent: who.Agent, SessionID: who.SessionID, DeadlineMS: trueFoundryBudgetMS})
	switch {
	case res.Request == nil:
		writeJSON(w, http.StatusOK, tfyLLMResponse{Verdict: false, Message: res.Reason})
	case res.Changed && trueFoundryMutates(cfg):
		writeJSON(w, http.StatusOK, tfyLLMResponse{Verdict: true, Transformed: true, Result: res.Request})
	case res.Changed:
		writeJSON(w, http.StatusOK, tfyLLMResponse{Verdict: false, Message: withheldSummary("This request asks the model provider to run tools Agent Authority does not allow", res.Tools)})
	default:
		writeJSON(w, http.StatusOK, tfyLLMResponse{Verdict: true})
	}
}

func (h *Handler) serveTrueFoundryLLMOutput(w http.ResponseWriter, r *http.Request, gw caller) {
	body, req, ok := readTrueFoundryLLM(w, r)
	if !ok {
		return
	}
	if h.opts.Capture && capture(r, body) {
		writeJSON(w, http.StatusOK, tfyLLMResponse{Verdict: false, Message: "Agent Authority capture mode: response withheld while its payload is recorded."})
		return
	}
	// A body in no format this path reads (an embedding, say) can carry a
	// tool call only if the request gave the model tools. Without tools it
	// passes; with them CheckOutput refuses what it cannot read.
	if resp, err := decodeObject(req.ResponseBody); err == nil && detectResponseFormat(resp) == "" {
		if parsed, err := decodeObject(req.RequestBody); err == nil && !declaresTools(parsed) {
			writeJSON(w, http.StatusOK, tfyLLMResponse{Verdict: true})
			return
		}
	}
	who := trueFoundryWho(r, req)
	res := h.CheckOutput(r.Context(), gw, LLMOutput{Response: req.ResponseBody, Request: req.RequestBody, User: who.User, Agent: who.Agent, SessionID: who.SessionID, DeadlineMS: trueFoundryBudgetMS})
	switch {
	case res.Response == nil:
		writeJSON(w, http.StatusOK, tfyLLMResponse{Verdict: false, Message: res.Reason})
	case res.Changed && trueFoundryMutates(trueFoundryConfig(req)):
		writeJSON(w, http.StatusOK, tfyLLMResponse{Verdict: true, Transformed: true, Result: res.Response})
	case res.Changed:
		writeJSON(w, http.StatusOK, tfyLLMResponse{Verdict: false, Message: withheldSummary("Agent Authority did not allow every tool call in this response", res.Calls)})
	default:
		writeJSON(w, http.StatusOK, tfyLLMResponse{Verdict: true})
	}
}

type tfyLLMResponse struct {
	Verdict     bool            `json:"verdict"`
	Message     string          `json:"message,omitempty"`
	Transformed bool            `json:"transformed,omitempty"`
	Result      json.RawMessage `json:"result,omitempty"`
}

func readTrueFoundryLLM(w http.ResponseWriter, r *http.Request) ([]byte, tfyRequest, bool) {
	body, err := readBody(w, r, maxLLMBody)
	var req tfyRequest
	if err == nil {
		err = json.Unmarshal(body, &req)
	}
	if err != nil {
		writeJSON(w, http.StatusOK, tfyLLMResponse{Verdict: false, Message: "Agent Authority could not read this request."})
		return nil, req, false
	}
	return body, req, true
}

// trueFoundryConfig is the guardrail's own config; the UI's Config field may
// arrive nested under "config".
func trueFoundryConfig(req tfyRequest) map[string]any {
	var cfg map[string]any
	_ = json.Unmarshal(req.Config, &cfg)
	merged := map[string]any{}
	for _, m := range []map[string]any{mapAt(cfg, "config"), cfg} {
		for k, v := range m {
			merged[k] = v
		}
	}
	return merged
}

// withheldSummary is the one message a validate-only guardrail can return.
func withheldSummary(lead string, verdicts []CallVerdict) string {
	var parts []string
	for _, v := range verdicts {
		if v.Verdict != VerdictAllow {
			parts = append(parts, v.Tool+": "+messageFor(v.Result))
		}
	}
	return lead + ". " + strings.Join(parts, " ")
}

// messageFor is the text a gateway shows the caller.
func messageFor(res Result) string {
	if res.Reason != "" {
		return res.Reason
	}
	return res.Verdict
}

// capture logs the raw request with credentials redacted when ?capture=1 is
// set, to learn a gateway's real payload; the request is then refused.
func capture(r *http.Request, body []byte) bool {
	if r.URL.Query().Get("capture") != "1" {
		return false
	}
	headers := map[string]string{}
	for k, v := range r.Header {
		lk := strings.ToLower(k)
		if strings.Contains(lk, "authorization") || strings.Contains(lk, "token") || strings.Contains(lk, "cookie") || strings.Contains(lk, "key") {
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
