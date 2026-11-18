package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/validmind/atryum/internal/invocation"
)

// The LLM path: an AI gateway that relays agents' model traffic (LiteLLM,
// TrueFoundry's AI Gateway, or anything that can call a webhook) shows Atryum
// both halves of every exchange.
//
//	agent ──request──▶ AI gateway ──▶ model
//	                      │  ▲
//	     llm/input        │  │        llm/output
//	     (the request)    ▼  │        (the model's response)
//	                    Atryum
//	agent ◀── the response, with the calls that may not run taken out
//
// POST llm/output, before the response reaches the agent: every tool call the
// model proposed is decided against the gateway agent's rules, and the response
// comes back with the calls that may not run replaced by a sentence saying
// why. The agent never sees a call it may not make, so it cannot run it.
//
// POST llm/input, before the request reaches the model: the tool results in
// it are attached to the calls they answer (by the model's own call ID), and
// tools the request asks the provider itself to run are allowed or taken out,
// since their calls never pass the agent.
//
// The gateway must send whole responses. A streamed response has to be
// collected first; a gateway that cannot do that must not stream requests
// that carry tools.

// LLMOutput is a model response on its way to an agent.
type LLMOutput struct {
	// Format is anthropic, openai_chat or openai_responses; empty means work
	// it out from the body.
	Format string `json:"format,omitempty"`
	// Response is the model's whole response body.
	Response json.RawMessage `json:"response"`
	// Request is the request that produced it. Optional: only the last few
	// things the person typed are read from it, for the judge.
	Request          json.RawMessage `json:"request,omitempty"`
	User             *Principal      `json:"user,omitempty"`
	Agent            *Principal      `json:"agent,omitempty"`
	SessionID        string          `json:"session_id,omitempty"`
	GatewayRequestID string          `json:"gateway_request_id,omitempty"`
	DeadlineMS       int             `json:"deadline_ms,omitempty"`
}

// CallVerdict is the answer for one tool call or provider tool.
type CallVerdict struct {
	CallID string `json:"call_id,omitempty"`
	Tool   string `json:"tool"`
	Server string `json:"server,omitempty"`
	Result
}

// LLMOutputResult is what the gateway does with the response: send Response
// to the agent in place of the model's. Without a Response (verdict deny,
// code unreadable_response) nothing may be sent on.
type LLMOutputResult struct {
	// Verdict is allow when every call may run, block when at least one
	// waits for a person and none was denied, otherwise deny.
	Verdict  string          `json:"verdict"`
	Code     string          `json:"code"`
	Reason   string          `json:"reason,omitempty"`
	Changed  bool            `json:"changed"`
	Response json.RawMessage `json:"response,omitempty"`
	Calls    []CallVerdict   `json:"calls"`
}

// LLMInput is an agent's request on its way to a model.
type LLMInput struct {
	Format     string          `json:"format,omitempty"`
	Request    json.RawMessage `json:"request"`
	User       *Principal      `json:"user,omitempty"`
	Agent      *Principal      `json:"agent,omitempty"`
	SessionID  string          `json:"session_id,omitempty"`
	DeadlineMS int             `json:"deadline_ms,omitempty"`
}

// OutcomeReport says what became of one tool result in a request.
type OutcomeReport struct {
	CallID       string `json:"call_id"`
	InvocationID string `json:"invocation_id,omitempty"`
	// Status is completed or failed (attached to its call), unchecked (the
	// call never came through llm/output) or withheld (Atryum took the call
	// out of the response, and a result for it arrived anyway).
	Status string `json:"status"`
}

// LLMInputResult is what the gateway does with the request: send Request to
// the model in place of the agent's. Without a Request nothing may be sent.
type LLMInputResult struct {
	Verdict  string          `json:"verdict"`
	Code     string          `json:"code"`
	Reason   string          `json:"reason,omitempty"`
	Changed  bool            `json:"changed"`
	Request  json.RawMessage `json:"request,omitempty"`
	Outcomes []OutcomeReport `json:"outcomes"`
	// Tools are the provider-run tools the request asked for, with verdicts;
	// the ones not allowed are gone from Request.
	Tools []CallVerdict `json:"tools"`
}

const (
	CodeUnreadableResponse = "unreadable_response"
	CodeUnreadableRequest  = "unreadable_request"
	CodeAdjusted           = "adjusted"
	CodeTooManyCalls       = "too_many_calls"
	CodeUnknownOutcome     = "outcome_unknown"
)

// More event kinds for Options.OnEvent, from the LLM path.
const (
	// EventUncheckedToolResult: an agent sent the model a result for a call
	// that never came through llm/output, so the tool ran unchecked (a
	// streamed response the gateway did not collect, say).
	EventUncheckedToolResult = "gateway.unchecked_tool_result"
	// EventWithheldCallRan: a result arrived for a call Atryum took out of
	// the response. The gateway sent the model's original response on.
	EventWithheldCallRan = "gateway.withheld_call_ran"
	// EventProviderToolRan: the model provider ran a tool itself.
	EventProviderToolRan = "gateway.provider_tool_ran"
)

const (
	// maxCallsPerResponse bounds how many calls of one response are decided
	// (all at once, within one deadline). The rest are taken out.
	maxCallsPerResponse = 64
	// providerToolTTL is how long an allowed provider tool stays allowed for
	// the same gateway, person and session without asking again: requests
	// repeat their tool list on every turn.
	providerToolTTL = 5 * time.Minute
	// reportedTTL keeps one event per call ID when a request is retried.
	reportedTTL = time.Hour
)

func (h *Handler) serveLLMOutput(w http.ResponseWriter, r *http.Request, gw caller) {
	var in LLMOutput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxLLMBody)).Decode(&in); err != nil {
		writeJSON(w, http.StatusOK, LLMOutputResult{Verdict: VerdictDeny, Code: CodeUnreadableResponse, Reason: "Agent Authority could not read this response: invalid JSON body", Calls: []CallVerdict{}})
		return
	}
	writeJSON(w, http.StatusOK, h.CheckOutput(r.Context(), gw, in))
}

func (h *Handler) serveLLMInput(w http.ResponseWriter, r *http.Request, gw caller) {
	var in LLMInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxLLMBody)).Decode(&in); err != nil {
		writeJSON(w, http.StatusOK, LLMInputResult{Verdict: VerdictDeny, Code: CodeUnreadableRequest, Reason: "Agent Authority could not read this request: invalid JSON body", Outcomes: []OutcomeReport{}, Tools: []CallVerdict{}})
		return
	}
	writeJSON(w, http.StatusOK, h.CheckInput(r.Context(), gw, in))
}

// maxLLMBody bounds an LLM request or response: a long conversation with
// tool results is far larger than one tool call.
const maxLLMBody = 32 << 20

// CheckOutput decides every tool call in a model response and returns the
// response the agent may see.
func (h *Handler) CheckOutput(ctx context.Context, gw caller, in LLMOutput) LLMOutputResult {
	unreadable := func(why string) LLMOutputResult {
		return LLMOutputResult{Verdict: VerdictDeny, Code: CodeUnreadableResponse, Reason: "Agent Authority could not read this response: " + why, Calls: []CallVerdict{}}
	}
	format, ok := normalizeFormat(in.Format)
	if !ok {
		return unreadable(fmt.Sprintf("unknown format %q", in.Format))
	}
	resp, err := decodeObject(in.Response)
	if err != nil {
		return unreadable("the response is not a JSON object")
	}
	if format == "" {
		if format = detectResponseFormat(resp); format == "" {
			return unreadable("it is not a whole Anthropic Messages, OpenAI Chat Completions or OpenAI Responses response (collect a stream before sending it)")
		}
	} else if !isResponseFormat(format, resp) {
		// Read in the wrong format a response shows no calls, and every call
		// in it would pass.
		return unreadable(fmt.Sprintf("it is not a whole %s response, the format it was sent as (collect a stream before sending it)", format))
	}
	for _, name := range providerCalls(format, resp) {
		h.event(ctx, EventProviderToolRan, map[string]any{"agent_id": gw.AgentID, "vendor": gw.Vendor, "tool": name, "server": ProviderServer, "reason": "The model provider ran this tool itself; it never passed the agent."})
	}
	calls := modelCalls(format, resp)
	if len(calls) == 0 {
		return LLMOutputResult{Verdict: VerdictAllow, Code: CodeAllow, Response: in.Response, Calls: []CallVerdict{}}
	}

	// What the person asked, for the judge. Only read when the gateway sent
	// the request along.
	var excerpt string
	var excerptN int
	if req, err := decodeObject(in.Request); err == nil {
		excerpt, excerptN = humanExcerpt(format, req)
	}

	// A retry of this same response replays its decisions; another response
	// that reuses a call ID (call_0 on every turn) is decided afresh.
	responseID := firstNonEmpty(stringAt(resp, "id"), strings.TrimSpace(in.GatewayRequestID))
	verdicts := make([]CallVerdict, len(calls))
	var wg sync.WaitGroup
	for i, c := range calls {
		verdicts[i] = CallVerdict{CallID: c.ID, Tool: c.Tool, Server: c.Server}
		switch {
		case i >= maxCallsPerResponse:
			verdicts[i].Result = Result{Verdict: VerdictDeny, Code: CodeTooManyCalls, Reason: fmt.Sprintf("Agent Authority decides at most %d calls per response.", maxCallsPerResponse)}
		case c.Unreadable != "":
			verdicts[i].Result = unreadableCall(c.Unreadable)
		default:
			wg.Add(1)
			go func() {
				defer wg.Done()
				verdicts[i].Result = h.decideCall(ctx, gw, toolCall{
					callID: c.ID, responseID: responseID, tool: c.Tool, server: c.Server, arguments: c.Arguments,
					user: in.User, agent: in.Agent, sessionID: in.SessionID, deadlineMS: in.DeadlineMS,
					sessionContext: excerpt, sessionContextMessages: excerptN,
				})
			}()
		}
	}
	wg.Wait()

	out := LLMOutputResult{Verdict: VerdictAllow, Code: CodeAllow, Response: in.Response, Calls: verdicts}
	notices := map[string]string{}
	for i, v := range verdicts {
		if v.Verdict == VerdictAllow {
			continue
		}
		notices[calls[i].ID] = noticeFor(calls[i], v.Result)
		if v.Verdict == VerdictDeny || out.Verdict == VerdictAllow {
			out.Verdict = v.Verdict
		}
	}
	if len(notices) == 0 {
		return out
	}
	withhold(format, resp, notices)
	adjusted, err := encodeJSON(resp)
	if err != nil {
		return unreadable("the adjusted response could not be written")
	}
	out.Code, out.Changed, out.Response = CodeAdjusted, true, adjusted
	return out
}

func unreadableCall(why string) Result {
	return Result{Verdict: VerdictDeny, Code: CodeUnreadableArguments, Reason: "Agent Authority could not read this call: " + why}
}

// noticeFor is the sentence the agent reads in place of a call it may not
// run. It stays in the agent's history, so the model sees it on later turns.
func noticeFor(c modelCall, res Result) string {
	name := c.Name
	if name == "" {
		name = "a tool"
	}
	switch {
	case res.Verdict == VerdictBlock:
		return fmt.Sprintf("[Agent Authority] The call to %s has not run. %s", name, res.Reason)
	case res.Code == CodeUnavailable:
		return fmt.Sprintf("[Agent Authority] The call to %s did not run. %s Try again shortly.", name, strings.TrimRight(res.Reason, ". ")+".")
	}
	reason := strings.TrimRight(firstNonEmpty(res.Reason, "Denied by Agent Authority"), ". ") + "."
	return fmt.Sprintf("[Agent Authority] The call to %s was blocked and did not run: %s Do not retry the same call; change the approach or ask a person.", name, reason)
}

// CheckInput records the tool results in a request against their calls and
// takes out the provider-run tools the agent may not use.
func (h *Handler) CheckInput(ctx context.Context, gw caller, in LLMInput) LLMInputResult {
	unreadable := func(why string) LLMInputResult {
		return LLMInputResult{Verdict: VerdictDeny, Code: CodeUnreadableRequest, Reason: "Agent Authority could not read this request: " + why, Outcomes: []OutcomeReport{}, Tools: []CallVerdict{}}
	}
	format, ok := normalizeFormat(in.Format)
	if !ok {
		return unreadable(fmt.Sprintf("unknown format %q", in.Format))
	}
	req, err := decodeObject(in.Request)
	if err != nil {
		return unreadable("the request is not a JSON object")
	}
	if format == "" {
		format = detectRequestFormat(req)
	} else if signal := requestFormatSignal(req); signal != "" && signal != format {
		// Read in the wrong format a request shows neither its tool results
		// nor the provider tools it asks for.
		return unreadable(fmt.Sprintf("it is a %s request, not %s, the format it was sent as", signal, format))
	}
	out := LLMInputResult{Verdict: VerdictAllow, Code: CodeAllow, Request: in.Request, Outcomes: []OutcomeReport{}, Tools: []CallVerdict{}}

	// The tool has already run: nothing here may hold the request up, and a
	// failure to record never refuses it.
	proposed := requestCalls(format, req)
	for _, o := range toolOutcomes(format, req) {
		call, known := proposed[o.CallID]
		out.Outcomes = append(out.Outcomes, h.recordModelOutcome(ctx, gw, o, in.User, in.SessionID, call, known))
	}

	tools := providerTools(format, req)
	if len(tools) == 0 {
		return out
	}
	denied := map[string]bool{}
	verdicts := make([]CallVerdict, len(tools))
	var wg sync.WaitGroup
	for i, t := range tools {
		verdicts[i] = CallVerdict{Tool: t.Tool, Server: t.Server}
		wg.Add(1)
		go func() {
			defer wg.Done()
			verdicts[i].Result = h.decideProviderTool(ctx, gw, t, in)
		}()
	}
	wg.Wait()
	for i, v := range verdicts {
		if v.Verdict != VerdictAllow {
			denied[tools[i].key()] = true
		}
	}
	out.Tools = verdicts
	if len(denied) == 0 {
		return out
	}
	dropProviderTools(format, req, denied)
	adjusted, err := encodeJSON(req)
	if err != nil {
		return unreadable("the adjusted request could not be written")
	}
	out.Code, out.Changed, out.Request = CodeAdjusted, true, adjusted
	return out
}

// recordModelOutcome attaches one tool result to the call it answers, found
// by the model's call ID (stored as the invocation's request ID at llm/output).
//
// call is the model's call with this ID, as the same request's history shows
// it (known false when the history doesn't carry it): the result answers a
// call with that tool and those arguments, so a call with the same ID that
// differs in either is someone else's.
func (h *Handler) recordModelOutcome(ctx context.Context, gw caller, o toolOutcome, user *Principal, sessionID string, call modelCall, known bool) OutcomeReport {
	report := OutcomeReport{CallID: o.CallID, Status: CodeUnknownOutcome}
	if o.CallID == "" {
		return report
	}
	userID := ""
	if user != nil {
		userID = strings.TrimSpace(user.ID)
	}
	// Call IDs are only unique within one conversation, and one gateway agent
	// serves every person: only a call of this person's, in this session,
	// can be the one this result answers. The running call with this ID
	// first: a model that reuses call IDs from turn to turn has older,
	// finished calls under the same one.
	// With neither the call it answers in the history nor a person or
	// session, nothing ties this result to one call: it may still raise the
	// alarms below, but it completes no call.
	attributable := known || userID != "" || strings.TrimSpace(sessionID) != ""
	filter := invocation.InvocationListFilter{RequestID: o.CallID, AgentIDs: []string{gw.AgentID}, Status: string(invocation.StatusExecuting), Limit: outcomeCandidates}
	if known && call.Unreadable == "" {
		filter.Tool = call.Tool
	}
	inv, found, err := h.ownCall(ctx, filter, userID, sessionID, call, known)
	if err == nil && !found {
		filter.Status = ""
		inv, found, err = h.ownCall(ctx, filter, userID, sessionID, call, known)
	}
	if err != nil {
		h.event(ctx, EventOutcomeUnrecorded, map[string]any{"agent_id": gw.AgentID, "vendor": gw.Vendor, "call_id": o.CallID, "reason": err.Error()})
		return report
	}
	if !found {
		report.Status = "unchecked"
		if h.firstReport("unchecked:" + gw.AgentID + ":" + o.CallID) {
			h.event(ctx, EventUncheckedToolResult, map[string]any{"agent_id": gw.AgentID, "vendor": gw.Vendor, "call_id": o.CallID, "reason": "A tool result reached the model for a call Agent Authority never saw: the response that proposed it did not pass llm/output."})
		}
		return report
	}
	report.InvocationID = inv.InvocationID
	switch inv.Status {
	case invocation.StatusDenied, invocation.StatusPendingApproval:
		report.Status = "withheld"
		if h.firstReport("withheld:" + inv.InvocationID) {
			h.event(ctx, EventWithheldCallRan, map[string]any{"agent_id": gw.AgentID, "vendor": gw.Vendor, "tool": inv.ToolName, "invocation_id": inv.InvocationID, "call_id": o.CallID, "reason": "A tool result reached the model for a call Agent Authority took out of the response: the gateway sent the model's original response to the agent."})
		}
		return report
	case invocation.StatusSucceeded:
		report.Status = "completed" // recorded on an earlier attempt at this request
		return report
	case invocation.StatusFailed:
		report.Status = "failed"
		return report
	}
	if !attributable {
		report.Status = "unattributed"
		return report
	}
	report.Status = "completed"
	done := outcome{invocationID: inv.InvocationID, status: "completed", result: o.Content}
	if o.Failed {
		report.Status = "failed"
		done.status, done.err, done.result = "failed", o.Content, nil
	}
	h.record(ctx, gw, done)
	return report
}

// outcomeCandidates bounds the calls with one call ID read to find the
// person's own: newest first, so an older conversation's never wins.
const outcomeCandidates = 50

// ownCall is the newest call in filter made for userID in sessionID, the
// identity and session the gateway asserted when the call was decided.
func (h *Handler) ownCall(ctx context.Context, filter invocation.InvocationListFilter, userID, sessionID string, call modelCall, known bool) (invocation.InvocationResponse, bool, error) {
	list, err := h.svc.List(ctx, filter)
	if err != nil {
		return invocation.InvocationResponse{}, false, err
	}
	for _, inv := range list.Items {
		a := inv.Asserted
		if a == nil || a.UserID != userID || strings.TrimSpace(a.SessionID) != strings.TrimSpace(sessionID) {
			continue
		}
		if known && (inv.ToolName != call.Tool || call.Unreadable != "" || !sameArguments(inv.Input, call.Arguments)) {
			continue
		}
		return inv, true, nil
	}
	return invocation.InvocationResponse{}, false, nil
}

// decideProviderTool answers "may this request ask the provider to run this
// tool". An allow is remembered for a few minutes, since every turn of a
// conversation repeats its tool list, and closed at once: there is no call of
// the agent's to wait for.
func (h *Handler) decideProviderTool(ctx context.Context, gw caller, t providerTool, in LLMInput) Result {
	user := ""
	if in.User != nil {
		user = strings.TrimSpace(in.User.ID)
	}
	memo := strings.Join([]string{"provider", gw.AgentID, user, in.SessionID, t.key(), string(t.Arguments)}, "\x00")
	if h.remembered(memo) {
		return Result{Verdict: VerdictAllow, Code: CodeAllow, Reason: "Allowed for this session a moment ago."}
	}
	res := h.decideCall(ctx, gw, toolCall{
		tool: t.Tool, server: t.Server, arguments: t.Arguments,
		user: in.User, agent: in.Agent, sessionID: in.SessionID, deadlineMS: in.DeadlineMS,
	})
	if res.Verdict != VerdictAllow {
		return res
	}
	h.remember(memo, providerToolTTL)
	if res.InvocationID != "" {
		note := json.RawMessage(`{"content":"Made available to the model. The model provider runs this tool itself, so its calls never pass the agent or Agent Authority."}`)
		h.record(ctx, gw, outcome{invocationID: res.InvocationID, status: "completed", result: note})
	}
	return res
}

// memo is a small set of keys that expire: allowed provider tools, and the
// call IDs an event was already recorded for. It is per process; another
// replica asking again costs one more decision, never a wrong one.
type memo struct {
	mu      sync.Mutex
	expires map[string]time.Time
}

func (h *Handler) remembered(key string) bool {
	h.memo.mu.Lock()
	defer h.memo.mu.Unlock()
	until, ok := h.memo.expires[key]
	return ok && time.Now().Before(until)
}

func (h *Handler) remember(key string, ttl time.Duration) {
	h.memo.mu.Lock()
	defer h.memo.mu.Unlock()
	now := time.Now()
	if h.memo.expires == nil {
		h.memo.expires = map[string]time.Time{}
	}
	if len(h.memo.expires) > 10_000 { // keep it small: drop what has expired
		for k, until := range h.memo.expires {
			if now.After(until) {
				delete(h.memo.expires, k)
			}
		}
	}
	h.memo.expires[key] = now.Add(ttl)
}

// firstReport is true once per key per reportedTTL.
func (h *Handler) firstReport(key string) bool {
	if h.remembered(key) {
		return false
	}
	h.remember(key, reportedTTL)
	return true
}

func readBody(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, error) {
	return io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
}
