// Package gateway lets an AI gateway, which relays agents' model traffic
// (LiteLLM, TrueFoundry's AI Gateway, or anything that can call a webhook),
// put Atryum in the path of every tool call a model proposes, without
// changing the agents behind it (llm.go).
//
// The gateway shows Atryum each model response before the agent does (POST
// /api/v1/gateway/llm/output) and each request before the model does (POST
// /api/v1/gateway/llm/input). Every tool call the model proposes is decided
// here, and the ones that may not run are taken out of the response the
// agent receives. Vendor translators (truefoundry_llm.go) map a gateway's
// native hook onto the same two checks.
//
// The gateway's credential is an agent API key with the "asserter"
// capability: the key's agent is the gateway itself, and only such a key may
// say which human (and sub-agent and session) a call is for.
//
// Gateways wait only seconds and some let a request through when their
// policy check errors. So every answer after authentication is HTTP 200, any
// failure is a deny ("fail closed"), and a call that needs a person is
// answered at once with "block" and a review link. Letting the approved
// call's retry through (a new call ID, the same tool, arguments and person)
// is the embedding program's decider's job: Agent Authority does it. Atryum
// alone also has no way to give a key the asserter capability.
package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/validmind/atryum/internal/auth"
	"github.com/validmind/atryum/internal/invocation"
	"github.com/validmind/atryum/pkg/tenant"
)

// Verdicts a gateway receives.
const (
	VerdictAllow = "allow"
	VerdictDeny  = "deny"
	VerdictBlock = "block"
)

// Codes say why; gateways show Reason, operators and tests read Code.
const (
	CodeAllow               = "allow"
	CodeDeny                = "deny"
	CodeReviewOpened        = "review_opened"
	CodeReviewPending       = "review_pending"
	CodeUnavailable         = "aa_unavailable"
	CodeUnreadableArguments = "unreadable_arguments"
)

// Event kinds reported through Options.OnEvent.
const (
	EventAsserterRejected  = "gateway.asserter_rejected"
	EventCheckFailed       = "gateway.check_failed"
	EventOutcomeUnrecorded = "gateway.outcome_unrecorded"
)

const (
	defaultBudget = 3500 * time.Millisecond
	maxBudget     = 7500 * time.Millisecond
	// margin is kept back from the budget for encoding and the network.
	margin = 300 * time.Millisecond
	// recordTimeout bounds the write of a decision that may finish after the
	// gateway has stopped waiting.
	recordTimeout = 30 * time.Second
)

// Principal is a user or agent a gateway asserts.
type Principal struct {
	ID      string `json:"id"`
	Type    string `json:"type,omitempty"`
	Display string `json:"display,omitempty"`
}

// toolCall is one tool call to decide: read out of a model response
// (llm/output), or a provider-run tool a request asks for (llm/input).
type toolCall struct {
	// callID is the model's own ID for the call; it becomes the invocation's
	// request ID, which is how the call's result is found later.
	callID string
	// responseID is the model response's own ID (msg_…, chatcmpl-…, resp_…)
	// or the gateway's ID for the request: a call is replayed only for a
	// retry of the same response.
	responseID string
	tool       string
	server     string
	arguments  json.RawMessage
	user       *Principal
	agent      *Principal
	sessionID  string
	deadlineMS int
	// sessionContext is what the person asked, for the judge.
	sessionContext         string
	sessionContextMessages int
}

// Result is the verdict a gateway receives.
type Result struct {
	Verdict      string `json:"verdict"`
	Reason       string `json:"reason,omitempty"`
	ReviewURL    string `json:"review_url,omitempty"`
	InvocationID string `json:"invocation_id,omitempty"`
	Code         string `json:"code"`
	// dispatch is set on an allow whose invocation is still "approved": it
	// is marked running only once the answer reaches the gateway in time
	// (decideCall), never for an answer the gateway stopped waiting for.
	dispatch bool
}

// outcome is how a call the model proposed turned out, read from the tool
// result in the agent's next request.
type outcome struct {
	invocationID string
	status       string // completed | failed
	result       json.RawMessage
	err          json.RawMessage
}

// Service is the slice of invocation.Service the gateway uses.
type Service interface {
	Submit(ctx context.Context, req invocation.ExternalSubmitRequest) (invocation.InvocationResponse, error)
	List(ctx context.Context, filter invocation.InvocationListFilter) (invocation.InvocationListResponse, error)
	RecordExecution(ctx context.Context, invocationID string, update invocation.ExternalExecutionUpdate) (invocation.InvocationResponse, error)
}

// Options are supplied by the embedding program.
type Options struct {
	// ConsoleLink returns the page where a person decides a held call
	// (reviewID may be empty). Without it, block answers carry no link.
	ConsoleLink func(ctx context.Context, invocationID, reviewID string) string
	// OnEvent records gateway events that have no invocation of their own
	// (a rejected asserter, a check that failed closed). Best-effort.
	OnEvent func(ctx context.Context, kind string, payload map[string]any)
	// Capture lets ?capture=1 on the TrueFoundry routes log a request's whole
	// body (credentials redacted), to learn a gateway's real payload. The
	// body is a conversation, so it is off unless the operator turns it on.
	Capture bool
}

// Handler serves /api/v1/gateway/*. Mount it behind the agent-runtime auth
// middleware.
type Handler struct {
	svc  Service
	opts Options
	// memo remembers, for a few minutes, what the LLM path need not ask or
	// report twice (llm.go).
	memo memo
}

// New returns a gateway handler over svc.
func New(svc Service, opts Options) *Handler { return &Handler{svc: svc, opts: opts} }

// Prefix is where the gateway routes live.
const Prefix = "/api/v1/gateway/"

// ServeHTTP routes the generic and vendor endpoints.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	gw, ok := h.authorize(w, r)
	if !ok {
		return
	}
	switch strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, Prefix), "/") {
	case "llm/output":
		h.serveLLMOutput(w, r, gw)
	case "llm/input":
		h.serveLLMInput(w, r, gw)
	case "truefoundry/llm/output":
		h.serveTrueFoundryLLMOutput(w, r, gw)
	case "truefoundry/llm/input":
		h.serveTrueFoundryLLMInput(w, r, gw)
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	}
}

// caller is the authenticated gateway.
type caller struct {
	AgentID string
	Vendor  string
}

// authorize admits only a verified key with the asserter capability. These
// are the only non-200 answers: a gateway configured to fail closed blocks
// the call on them, and without a valid credential nothing can be recorded
// against an agent anyway.
func (h *Handler) authorize(w http.ResponseWriter, r *http.Request) (caller, bool) {
	id, ok := auth.IdentityFromContext(r.Context())
	if !ok || id.AgentID == "" || id.Method != auth.IdentityMethodAPIKey {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "an agent API key with the asserter capability is required", "code": "unauthenticated"})
		return caller{}, false
	}
	if !id.HasCapability(auth.CapabilityAsserter) {
		h.event(r.Context(), EventAsserterRejected, map[string]any{"agent_id": id.AgentID, "key_id": id.KeyID, "path": r.URL.Path})
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "this key may not assert identity: it lacks the asserter capability", "code": "not_an_asserter"})
		return caller{}, false
	}
	return caller{AgentID: id.AgentID, Vendor: vendorOf(r.URL.Path)}, true
}

func vendorOf(path string) string {
	if strings.HasPrefix(path, Prefix+"truefoundry/") {
		return "truefoundry"
	}
	return "generic"
}

// decideCall decides one tool call. It always returns a verdict: any error,
// panic or overrun becomes a deny with code aa_unavailable.
func (h *Handler) decideCall(ctx context.Context, gw caller, c toolCall) (res Result) {
	budget := defaultBudget
	if c.deadlineMS > 0 {
		budget = min(time.Duration(c.deadlineMS)*time.Millisecond, maxBudget)
	}
	answerBy := time.Now().Add(budget - margin)

	done := make(chan Result, 1)
	go func() {
		defer func() {
			if p := recover(); p != nil {
				slog.Error("gateway check panicked; failing closed", "panic", p)
				done <- h.unavailable(ctx, gw, c, "internal error", fmt.Errorf("panic: %v", p))
			}
		}()
		// The decision is recorded even if the gateway stops waiting: a
		// late "pending" still opens the review the retry will find.
		work := context.WithoutCancel(ctx)
		work, cancel := context.WithTimeout(work, recordTimeout)
		defer cancel()
		done <- h.decide(invocation.WithDecisionDeadline(work, answerBy), gw, c)
	}()
	timer := time.NewTimer(time.Until(answerBy))
	defer timer.Stop()
	select {
	case res = <-done:
		if res.dispatch {
			// The gateway hears this allow, so the agent receives the call.
			if _, err := h.svc.RecordExecution(context.WithoutCancel(ctx), res.InvocationID, invocation.ExternalExecutionUpdate{ExecutionStatus: "running"}); err != nil {
				slog.Warn("gateway: could not mark an allowed call running", "invocation_id", res.InvocationID, "error", err)
			}
		}
		return res
	case <-timer.C:
		// A decision that lands after this stays as decided (an allow stays
		// "approved", never "running"): the agent was told it may not run it.
		return h.unavailable(ctx, gw, c, "no decision within the gateway's timeout", nil)
	}
}

func (h *Handler) decide(ctx context.Context, gw caller, c toolCall) Result {
	c.tool = strings.TrimSpace(c.tool)
	if c.tool == "" {
		return unreadable("tool is required")
	}
	args, err := parseArguments(c.arguments)
	if err != nil {
		return unreadable(err.Error())
	}
	asserted := &invocation.AssertedIdentity{By: gw.AgentID, Vendor: gw.Vendor, SessionID: c.sessionID}
	if c.user != nil {
		asserted.UserID, asserted.UserType, asserted.UserDisplay = strings.TrimSpace(c.user.ID), c.user.Type, c.user.Display
	}
	if c.agent != nil {
		asserted.AgentID = strings.TrimSpace(c.agent.ID)
	}
	key, err := CheckKey(tenant.From(ctx), gw.AgentID, asserted.UserID, c.server, c.tool, c.arguments)
	if err != nil {
		return unreadable(err.Error())
	}

	// A repeat of a call that is still waiting for a person gets the same
	// review back, not a second one.
	if pending, err := h.svc.List(ctx, invocation.InvocationListFilter{GatewayCheckKey: key, Status: string(invocation.StatusPendingApproval), AgentIDs: []string{gw.AgentID}, Limit: 1}); err != nil {
		return h.unavailable(ctx, gw, c, "could not look up open reviews", err)
	} else if len(pending.Items) > 0 {
		inv := pending.Items[0]
		url := h.link(ctx, inv.InvocationID, "")
		return Result{Verdict: VerdictBlock, Code: CodeReviewPending, InvocationID: inv.InvocationID, ReviewURL: url, Reason: heldReason(url, "This call is already waiting for a person.")}
	}

	req := invocation.ExternalSubmitRequest{
		Source:                 c.server,
		Tool:                   c.tool,
		Input:                  args,
		Description:            "Proposed by the model; read from its response at the AI gateway",
		ClientSessionID:        personalSession(c.sessionID, asserted.UserID),
		ClientName:             clientName(gw),
		Asserted:               asserted,
		GatewayCheckKey:        key,
		SessionContext:         c.sessionContext,
		SessionContextMessages: c.sessionContextMessages,
	}
	if id := strings.TrimSpace(c.callID); id != "" {
		req.RequestID = &id
		// Call IDs are only unique within one response (some models number
		// them call_0, call_1 on every turn), and one gateway agent serves
		// every person and sub-agent: only a retry of the same response, for
		// the same person, sub-agent and session, replays a decision. Without
		// the response's ID every proposal is decided and recorded on its own.
		if rid := strings.TrimSpace(c.responseID); rid != "" {
			idem := callKey(asserted.UserID, asserted.AgentID, c.sessionID, rid, id)
			req.IdempotencyKey = &idem
		}
	}
	resp, err := h.svc.Submit(ctx, req)
	// Some models number their calls from zero on every turn ("call_0"), so
	// a call ID seen before is not always a replay: when the ID belongs to a
	// different call, or to one that has finished, this is a new call.
	if req.IdempotencyKey != nil && (errors.Is(err, invocation.ErrIdempotencyConflict) || (err == nil && finished(resp.Status))) {
		req.IdempotencyKey = nil
		resp, err = h.svc.Submit(ctx, req)
	}
	if err != nil {
		return h.unavailable(ctx, gw, c, "could not record the call", err)
	}
	return h.verdictFor(ctx, resp)
}

// verdictFor maps a submitted invocation to the gateway's answer.
func (h *Handler) verdictFor(ctx context.Context, resp invocation.InvocationResponse) Result {
	reason := ""
	reviewID := ""
	if resp.Decision != nil {
		reason, reviewID = resp.Decision.Reason, resp.Decision.ReviewID
	} else if resp.Approval != nil && resp.Approval.Reason != nil {
		reason = *resp.Approval.Reason
	}
	switch resp.Status {
	case invocation.StatusApproved, invocation.StatusExecuting:
		// decideCall marks an approved call running once the gateway has the
		// answer, so the record shows the gateway ran it.
		return Result{Verdict: VerdictAllow, Code: CodeAllow, InvocationID: resp.InvocationID, Reason: reason, dispatch: resp.Status == invocation.StatusApproved}
	case invocation.StatusDenied:
		if reason == "" {
			reason = "Denied by Agent Authority."
		}
		return Result{Verdict: VerdictDeny, Code: CodeDeny, InvocationID: resp.InvocationID, Reason: reason}
	case invocation.StatusPendingApproval:
		url := h.link(ctx, resp.InvocationID, reviewID)
		return Result{Verdict: VerdictBlock, Code: CodeReviewOpened, InvocationID: resp.InvocationID, ReviewURL: url, Reason: heldReason(url, "This call needs a person's approval.")}
	}
	return Result{Verdict: VerdictDeny, Code: CodeUnavailable, InvocationID: resp.InvocationID, Reason: fmt.Sprintf("Agent Authority unavailable: unexpected state %q.", resp.Status)}
}

// clientName is the client recorded on calls read from model responses.
func clientName(gw caller) string { return gw.Vendor + "-llm-gateway" }

// finished reports a call whose tool has run (or never will).
func finished(s invocation.Status) bool {
	switch s {
	case invocation.StatusSucceeded, invocation.StatusFailed, invocation.StatusCancelled, invocation.StatusExpired:
		return true
	}
	return false
}

func heldReason(url, lead string) string {
	if url == "" {
		return lead + " Ask a reviewer in Agent Authority, then retry the same call after it is approved."
	}
	return lead + " Open " + url + " and retry the same call after it is approved."
}

func (h *Handler) link(ctx context.Context, invocationID, reviewID string) string {
	if h.opts.ConsoleLink == nil {
		return ""
	}
	return h.opts.ConsoleLink(ctx, invocationID, reviewID)
}

// unavailable is the fail-closed answer.
// The reason is a fixed sentence: it becomes the text the agent, the model
// and the person read in place of the call. The cause (a database error, a
// panic) goes only to the log and the gateway.check_failed event.
func (h *Handler) unavailable(ctx context.Context, gw caller, c toolCall, why string, cause error) Result {
	payload := map[string]any{"agent_id": gw.AgentID, "vendor": gw.Vendor, "tool": c.tool, "server": c.server, "reason": why}
	if cause != nil {
		payload["error"] = cause.Error()
		slog.Warn("gateway check failed closed", "gateway", gw.AgentID, "tool", c.tool, "reason", why, "error", cause)
	} else {
		slog.Warn("gateway check failed closed", "gateway", gw.AgentID, "tool", c.tool, "reason", why)
	}
	h.event(ctx, EventCheckFailed, payload)
	return Result{Verdict: VerdictDeny, Code: CodeUnavailable, Reason: "Agent Authority unavailable: " + why + "."}
}

func unreadable(why string) Result {
	return Result{Verdict: VerdictDeny, Code: CodeUnreadableArguments, Reason: "Agent Authority could not read this call: " + why}
}

func (h *Handler) event(ctx context.Context, kind string, payload map[string]any) {
	if h.opts.OnEvent == nil {
		return
	}
	defer func() {
		if p := recover(); p != nil {
			slog.Error("gateway event hook panicked", "kind", kind, "panic", p)
		}
	}()
	h.opts.OnEvent(context.WithoutCancel(ctx), kind, payload)
}

// record attaches a call's outcome to its invocation. It never fails the
// gateway: the tool has already run.
func (h *Handler) record(ctx context.Context, gw caller, o outcome) {
	update := invocation.ExternalExecutionUpdate{ExecutionStatus: o.status, Result: o.result, Error: o.err}
	if o.status == "failed" && len(update.Error) == 0 {
		update.Error = json.RawMessage(`{"message":"the tool reported an error"}`)
	}
	if _, err := h.svc.RecordExecution(ctx, o.invocationID, update); err != nil && !errors.Is(err, invocation.ErrInvalidTransition) {
		h.event(ctx, EventOutcomeUnrecorded, map[string]any{"agent_id": gw.AgentID, "invocation_id": o.invocationID, "reason": err.Error()})
	}
}

// parseArguments accepts an object, or a JSON string holding one (OpenAI's
// formats send arguments as text). Null and absent mean no arguments.
func parseArguments(raw json.RawMessage) (map[string]any, error) {
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	if len(raw) == 0 || string(raw) == "null" {
		return map[string]any{}, nil
	}
	if raw[0] == '"' {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return nil, fmt.Errorf("arguments are not valid JSON")
		}
		text = strings.TrimSpace(text)
		if text == "" {
			return map[string]any{}, nil
		}
		raw = json.RawMessage(text)
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber() // keep numbers exactly as the agent wrote them
	var args map[string]any
	if err := dec.Decode(&args); err != nil || args == nil {
		return nil, fmt.Errorf("arguments must be a JSON object")
	}
	// Two objects back to back (a stream that merged two calls) would be
	// decided on the first while the agent runs something else.
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return nil, fmt.Errorf("arguments must be one JSON object, with nothing after it")
	}
	return args, nil
}

// CheckKey identifies "the same call" for a gateway: same tenant, gateway,
// asserted user, server, tool and arguments (canonical JSON, so key order and
// whitespace don't matter).
func CheckKey(tenantID, gatewayAgentID, userID, server, tool string, rawArgs json.RawMessage) (string, error) {
	args, err := parseArguments(rawArgs)
	if err != nil {
		return "", err
	}
	canon, err := canonicalJSON(args)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	for _, part := range []string{tenantID, gatewayAgentID, userID, server, tool, string(canon)} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return "gck_" + hex.EncodeToString(h.Sum(nil)), nil
}

// callKey is the idempotency key of a model's call ID: the call ID within
// one model response, for one person, sub-agent and session at one gateway
// agent.
func callKey(userID, agentID, sessionID, responseID, callID string) string {
	h := sha256.New()
	for _, part := range []string{userID, agentID, sessionID, responseID, callID} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return "gcall_" + hex.EncodeToString(h.Sum(nil))[:32]
}

// personalSession is the session a gateway's call is grouped under: the
// session ID the gateway sent, per person. Callers often choose session IDs
// themselves (TrueFoundry's X-TFY-METADATA), so two people sending the same
// ID must not share a session, its history or the judge's view of it.
func personalSession(sessionID, userID string) string {
	// The caller chooses the session ID: one spelled "s1 · bob" must not land
	// in bob's session "s1".
	sessionID, userID = strings.ReplaceAll(strings.TrimSpace(sessionID), " · ", " "), strings.TrimSpace(userID)
	if sessionID == "" || userID == "" {
		return sessionID
	}
	// Not ":", which Agent Authority's console (and its hook) read as
	// "<session>:<subagent>".
	return sessionID + " · " + userID
}

// canonicalJSON encodes v with sorted object keys, no insignificant
// whitespace and no HTML escaping. Numbers keep the digits they were sent
// with (json.Number).
func canonicalJSON(v any) ([]byte, error) {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return []byte(strings.TrimSuffix(b.String(), "\n")), nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
