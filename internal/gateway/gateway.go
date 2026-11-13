// Package gateway lets a third-party MCP gateway (TrueFoundry, Red Hat /
// Kuadrant, or anything that can call a webhook) put Atryum in the loop on
// every tool call that passes through it, without changing the agents behind
// it.
//
// The gateway asks before each call (POST /api/v1/gateway/checks) and reports
// after it (POST /api/v1/gateway/outcomes). Its credential is an agent API key
// with the "asserter" capability: the key's agent is the gateway itself, and
// only such a key may say which human (and sub-agent) a call is for. Vendor
// translators (truefoundry.go, kuadrant.go) map each gateway's native hook
// onto the same generic Check and Outcome.
//
// Gateways wait only seconds and some let a call through when their policy
// check errors. So every answer after authentication is HTTP 200 with a
// verdict, any failure is a deny ("fail closed"), and a call that needs a
// person is answered at once with "block" and a review link; the approval
// then lets one identical retry through.
package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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
	CodeCapture             = "capture"
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
	// outcomeWindow is how far back an outcome report without an ID looks
	// for the call it belongs to (AA's approval lease is 15 minutes).
	outcomeWindow = 15 * time.Minute
)

// Principal is a user or agent a gateway asserts.
type Principal struct {
	ID      string `json:"id"`
	Type    string `json:"type,omitempty"`
	Display string `json:"display,omitempty"`
}

// Check is a pre-call verdict request (contracts/gateway-contract.md §1).
type Check struct {
	GatewayRequestID string          `json:"gateway_request_id,omitempty"`
	Tool             string          `json:"tool"`
	Server           string          `json:"server,omitempty"`
	Arguments        json.RawMessage `json:"arguments"`
	User             *Principal      `json:"user,omitempty"`
	Agent            *Principal      `json:"agent,omitempty"`
	SessionID        string          `json:"session_id,omitempty"`
	DeadlineMS       int             `json:"deadline_ms,omitempty"`
	Raw              json.RawMessage `json:"raw,omitempty"`
}

// Result is the verdict a gateway receives.
type Result struct {
	Verdict      string `json:"verdict"`
	Reason       string `json:"reason,omitempty"`
	ReviewURL    string `json:"review_url,omitempty"`
	InvocationID string `json:"invocation_id,omitempty"`
	Code         string `json:"code"`
}

// Outcome is a post-call report (contracts/gateway-contract.md §2). Without
// InvocationID or GatewayRequestID it is matched to the newest running call
// from the same gateway with the same tool (and arguments and user, when
// sent) in the last 15 minutes.
type Outcome struct {
	InvocationID     string          `json:"invocation_id,omitempty"`
	GatewayRequestID string          `json:"gateway_request_id,omitempty"`
	Status           string          `json:"status"` // completed | failed
	Result           json.RawMessage `json:"result,omitempty"`
	Error            json.RawMessage `json:"error,omitempty"`
	Tool             string          `json:"tool,omitempty"`
	Server           string          `json:"server,omitempty"`
	Arguments        json.RawMessage `json:"arguments,omitempty"`
	User             *Principal      `json:"user,omitempty"`
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
}

// Handler serves /api/v1/gateway/*. Mount it behind the agent-runtime auth
// middleware (and PathTokenToBearer, for Kuadrant).
type Handler struct {
	svc  Service
	opts Options
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
	case "checks":
		h.serveGenericCheck(w, r, gw)
	case "outcomes":
		h.serveGenericOutcome(w, r, gw)
	case "truefoundry/pre":
		h.serveTrueFoundryPre(w, r, gw)
	case "truefoundry/post":
		h.serveTrueFoundryPost(w, r, gw)
	case "kuadrant/v1/checks":
		h.serveKuadrantGuardrail(w, r, gw)
	case "kuadrant/authz":
		h.serveKuadrantAuthz(w, r, gw)
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
	switch {
	case strings.HasPrefix(path, Prefix+"truefoundry/"):
		return "truefoundry"
	case strings.HasPrefix(path, Prefix+"kuadrant/"):
		return "kuadrant"
	}
	return "generic"
}

func (h *Handler) serveGenericCheck(w http.ResponseWriter, r *http.Request, gw caller) {
	var c Check
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(&c); err != nil {
		writeJSON(w, http.StatusOK, unreadable("invalid JSON body"))
		return
	}
	writeJSON(w, http.StatusOK, h.Decide(r.Context(), gw, c))
}

func (h *Handler) serveGenericOutcome(w http.ResponseWriter, r *http.Request, gw caller) {
	var o Outcome
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(&o); err != nil {
		writeJSON(w, http.StatusOK, Result{Verdict: VerdictAllow, Code: "outcome_unreadable"})
		return
	}
	h.Record(r.Context(), gw, o)
	writeJSON(w, http.StatusOK, Result{Verdict: VerdictAllow, Code: "recorded"})
}

// maxBody bounds a gateway request (tool arguments and results included).
const maxBody = 4 << 20

// Decide answers one pre-call check. It always returns a verdict: any error,
// panic or overrun becomes a deny with code aa_unavailable.
func (h *Handler) Decide(ctx context.Context, gw caller, c Check) (res Result) {
	budget := defaultBudget
	if c.DeadlineMS > 0 {
		budget = min(time.Duration(c.DeadlineMS)*time.Millisecond, maxBudget)
	}
	answerBy := time.Now().Add(budget - margin)

	done := make(chan Result, 1)
	go func() {
		defer func() {
			if p := recover(); p != nil {
				slog.Error("gateway check panicked; failing closed", "panic", p)
				done <- h.unavailable(ctx, gw, c, fmt.Sprintf("internal error: %v", p))
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
		return res
	case <-timer.C:
		return h.unavailable(ctx, gw, c, "no decision within the gateway's timeout")
	}
}

func (h *Handler) decide(ctx context.Context, gw caller, c Check) Result {
	c.Tool = strings.TrimSpace(c.Tool)
	if c.Tool == "" {
		return unreadable("tool is required")
	}
	args, err := parseArguments(c.Arguments)
	if err != nil {
		return unreadable(err.Error())
	}
	asserted := &invocation.AssertedIdentity{By: gw.AgentID, Vendor: gw.Vendor, SessionID: c.SessionID}
	if c.User != nil {
		asserted.UserID, asserted.UserType, asserted.UserDisplay = strings.TrimSpace(c.User.ID), c.User.Type, c.User.Display
	}
	if c.Agent != nil {
		asserted.AgentID = strings.TrimSpace(c.Agent.ID)
	}
	key, err := CheckKey(tenant.From(ctx), gw.AgentID, asserted.UserID, c.Server, c.Tool, c.Arguments)
	if err != nil {
		return unreadable(err.Error())
	}

	// A repeat of a call that is still waiting for a person gets the same
	// review back, not a second one.
	if pending, err := h.svc.List(ctx, invocation.InvocationListFilter{GatewayCheckKey: key, Status: string(invocation.StatusPendingApproval), AgentIDs: []string{gw.AgentID}, Limit: 1}); err != nil {
		return h.unavailable(ctx, gw, c, "could not look up open reviews: "+err.Error())
	} else if len(pending.Items) > 0 {
		inv := pending.Items[0]
		url := h.link(ctx, inv.InvocationID, "")
		return Result{Verdict: VerdictBlock, Code: CodeReviewPending, InvocationID: inv.InvocationID, ReviewURL: url, Reason: heldReason(url, "This call is already waiting for a person.")}
	}

	req := invocation.ExternalSubmitRequest{
		Source:          c.Server,
		Tool:            c.Tool,
		Input:           args,
		ClientSessionID: c.SessionID,
		ClientName:      gw.Vendor + "-gateway",
		Asserted:        asserted,
		GatewayCheckKey: key,
	}
	if id := strings.TrimSpace(c.GatewayRequestID); id != "" {
		req.IdempotencyKey = &id
		req.RequestID = &id
	}
	resp, err := h.svc.Submit(ctx, req)
	if err != nil {
		return h.unavailable(ctx, gw, c, "could not record the call: "+err.Error())
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
		// Mark it dispatched, so the record shows the gateway ran it.
		if resp.Status == invocation.StatusApproved {
			if _, err := h.svc.RecordExecution(ctx, resp.InvocationID, invocation.ExternalExecutionUpdate{ExecutionStatus: "running"}); err != nil {
				slog.Warn("gateway: could not mark an allowed call running", "invocation_id", resp.InvocationID, "error", err)
			}
		}
		return Result{Verdict: VerdictAllow, Code: CodeAllow, InvocationID: resp.InvocationID, Reason: reason}
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
func (h *Handler) unavailable(ctx context.Context, gw caller, c Check, why string) Result {
	slog.Warn("gateway check failed closed", "gateway", gw.AgentID, "tool", c.Tool, "reason", why)
	h.event(ctx, EventCheckFailed, map[string]any{"agent_id": gw.AgentID, "vendor": gw.Vendor, "tool": c.Tool, "server": c.Server, "reason": why})
	return Result{Verdict: VerdictDeny, Code: CodeUnavailable, Reason: "Agent Authority unavailable: " + why}
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

// Record attaches a post-call outcome to its invocation. It never fails the
// gateway: the tool has already run.
func (h *Handler) Record(ctx context.Context, gw caller, o Outcome) {
	status := strings.ToLower(strings.TrimSpace(o.Status))
	if status != "completed" && status != "failed" {
		status = "completed"
	}
	id, err := h.findOutcomeTarget(ctx, gw, o)
	if err != nil || id == "" {
		h.event(ctx, EventOutcomeUnrecorded, map[string]any{"agent_id": gw.AgentID, "vendor": gw.Vendor, "tool": o.Tool, "reason": errString(err, "no matching call")})
		return
	}
	update := invocation.ExternalExecutionUpdate{ExecutionStatus: status, Result: o.Result, Error: o.Error}
	if status == "failed" && len(update.Error) == 0 {
		update.Error = json.RawMessage(`{"message":"the tool reported an error"}`)
	}
	if _, err := h.svc.RecordExecution(ctx, id, update); err != nil && !errors.Is(err, invocation.ErrInvalidTransition) {
		h.event(ctx, EventOutcomeUnrecorded, map[string]any{"agent_id": gw.AgentID, "invocation_id": id, "reason": err.Error()})
	}
}

func (h *Handler) findOutcomeTarget(ctx context.Context, gw caller, o Outcome) (string, error) {
	if o.InvocationID != "" {
		return o.InvocationID, nil
	}
	filter := invocation.InvocationListFilter{AgentIDs: []string{gw.AgentID}, Limit: 1}
	since := time.Now().Add(-outcomeWindow)
	filter.StartDate = &since
	switch {
	case o.GatewayRequestID != "":
		// The gateway's request ID is stored as the invocation's request_id
		// (see decide); scan this gateway's recent calls for it.
		filter.Limit = 50
		list, err := h.svc.List(ctx, filter)
		if err != nil {
			return "", err
		}
		for _, inv := range list.Items {
			if inv.RequestID != nil && *inv.RequestID == o.GatewayRequestID {
				return inv.InvocationID, nil
			}
		}
		return "", nil
	case len(o.Arguments) > 0:
		user := ""
		if o.User != nil {
			user = o.User.ID
		}
		key, err := CheckKey(tenant.From(ctx), gw.AgentID, user, o.Server, o.Tool, o.Arguments)
		if err != nil {
			return "", err
		}
		filter.GatewayCheckKey = key
	default:
		filter.Tool = o.Tool
	}
	filter.Status = string(invocation.StatusExecuting)
	list, err := h.svc.List(ctx, filter)
	if err != nil || len(list.Items) == 0 {
		return "", err
	}
	return list.Items[0].InvocationID, nil
}

func errString(err error, fallback string) string {
	if err == nil {
		return fallback
	}
	return err.Error()
}

// parseArguments accepts an object, or a JSON string holding one (some
// gateways pass arguments as text). Null and absent mean no arguments.
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

// PathTokenToBearer serves gateways that cannot send an Authorization header
// (Kuadrant's guardrails client): a request to
// /api/v1/gateway/kuadrant/k/{token}/rest is passed on as
// /api/v1/gateway/kuadrant/rest with "Authorization: Bearer {token}". The
// token never reaches logs: the rewritten path no longer holds it.
func PathTokenToBearer(next http.Handler) http.Handler {
	const marker = Prefix + "kuadrant/k/"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rest, ok := strings.CutPrefix(r.URL.Path, marker); ok {
			token, tail, _ := strings.Cut(rest, "/")
			if token == "" {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "missing key in path", "code": "unauthenticated"})
				return
			}
			r2 := r.Clone(r.Context())
			r2.Header.Set("Authorization", "Bearer "+token)
			r2.URL.Path = Prefix + "kuadrant/" + tail
			r2.URL.RawPath = ""
			r2.RequestURI = r2.URL.RequestURI()
			r = r2
		}
		next.ServeHTTP(w, r)
	})
}
