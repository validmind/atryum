package invocation

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/validmind/atryum/internal/auth"
	"github.com/validmind/atryum/internal/invocation/policy"
)

// Verdict is a Decider's answer for one tool call.
type Verdict string

const (
	VerdictAllow   Verdict = "allow"
	VerdictNotify  Verdict = "notify" // allow; the decider tells someone after
	VerdictDeny    Verdict = "deny"
	VerdictPending Verdict = "pending" // hold the call for the review in ReviewID
)

// ToolCall is what a Decider is asked about. Path is "proxy" when Atryum runs
// the tool (MCP proxy and direct invocation) and "submit" when the caller
// runs it (harness hooks and the Managed Agents bridge).
type ToolCall struct {
	InvocationID string          `json:"invocation_id"`
	Path         string          `json:"path"`
	AgentID      string          `json:"agent_id,omitempty"`
	Server       string          `json:"server"`
	Tool         string          `json:"tool"`
	Arguments    json.RawMessage `json:"arguments"`
	SessionID    string          `json:"session_id,omitempty"`
	// Auth is how the caller proved AgentID.
	Auth Auth `json:"auth"`
}

// Auth is how a caller authenticated. Method is "jwt" (an IdP token, Issuer
// is its iss), "api_key" (an Atryum agent API key, Issuer "atryum:api-key",
// KeyID its api_keys.id), "managed_agent" (a Claude Managed Agents session
// the bridge registered, Issuer "anthropic:<workspace>", KeyID the session
// ID) or "no_auth" (a self-declared agent ID, nothing verified); all fields
// are empty for an anonymous caller.
type Auth struct {
	Issuer string `json:"issuer,omitempty"`
	Method string `json:"method,omitempty"`
	KeyID  string `json:"key_id,omitempty"`
}

// authOf is how the request in ctx authenticated agentID (the identity the
// invocation is recorded under).
func authOf(ctx context.Context, agentID string) Auth {
	id, ok := auth.IdentityFromContext(ctx)
	if !ok || id.AgentID == "" || id.AgentID != agentID {
		if agentID != "" {
			return Auth{Method: auth.IdentityMethodNoAuth} // declared, e.g. a submit body's agent_id
		}
		return Auth{}
	}
	a := Auth{Issuer: id.Issuer, Method: id.Method, KeyID: id.KeyID}
	if a.Method == "" {
		a.Method = auth.IdentityMethodNoAuth // the no-auth ?agent_id= hint
	}
	return a
}

// Decision is a Decider's result. It is recorded under "decision" in the
// invocation.received event payload.
type Decision struct {
	Verdict  Verdict `json:"verdict"`
	Reason   string  `json:"reason,omitempty"`
	ReviewID string  `json:"review_id,omitempty"`
	// Explanation is the decider's account of this decision, rendered for
	// the agent whose call it was. Optional.
	Explanation *Explanation `json:"explanation,omitempty"`
}

// Decider replaces Atryum's rules, plans and default policy for every call.
type Decider func(ctx context.Context, call ToolCall) (Decision, error)

// Change is one durable change to an invocation. Type is the Atryum event
// type written with it ("invocation.received", ...), or "invocation.created"
// for the initial insert, which has no Atryum event of its own.
type Change struct {
	Type       string          `json:"type"`
	Invocation Invocation      `json:"invocation"`
	Payload    json.RawMessage `json:"payload,omitempty"`
	At         time.Time       `json:"at"`
}

// EventHook is called inside the transaction that writes each Change. An
// error rolls the whole write back.
type EventHook func(ctx context.Context, tx *sql.Tx, c Change) error

// TxRunner runs fn in one database transaction; repository writes made with
// the ctx it passes join that transaction.
type TxRunner func(ctx context.Context, fn func(ctx context.Context, tx *sql.Tx) error) error

// SetTx makes invocation writes and their events atomic. Without it each
// statement commits on its own and EventHook receives a nil tx.
func (s *Service) SetTx(run TxRunner) { s.inTx = run }

// SetDecider hands every decision to d. Rules, plans and the default policy
// are skipped; a decider error denies the call.
func (s *Service) SetDecider(d Decider) { s.decider = d }

// DecisionWaker announces decisions recorded by any process.
type DecisionWaker interface {
	// Wait returns a channel that receives when the invocation may have been
	// decided, and a func that stops waiting.
	Wait(invocationID string) (<-chan struct{}, func())
	// Connected reports whether announcements are arriving right now.
	Connected() bool
}

// SetDecisionWaker makes held calls re-read their row as soon as w announces
// a decision. Polling stays as the fallback: every two seconds while w is
// connected, at the usual pace otherwise.
func (s *Service) SetDecisionWaker(w DecisionWaker) { s.wake = w }

// SetEventHook calls h for every invocation change, inside its transaction.
func (s *Service) SetEventHook(h EventHook) { s.hook = h }

func (s *Service) runTx(ctx context.Context, fn func(ctx context.Context, tx *sql.Tx) error) error {
	if s.inTx == nil {
		return fn(ctx, nil)
	}
	return s.inTx(ctx, fn)
}

// create inserts inv and reports it to the hook as "invocation.created".
func (s *Service) create(ctx context.Context, inv Invocation) error {
	return s.runTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if err := s.invocations.Create(ctx, inv); err != nil {
			return err
		}
		if s.hook == nil {
			return nil
		}
		return s.hook(ctx, tx, Change{Type: "invocation.created", Invocation: inv, At: inv.SubmittedAt})
	})
}

// commit runs write (nil for none) and appends evts for inv in one
// transaction, calling the hook for each event inside it.
func (s *Service) commit(ctx context.Context, inv Invocation, write func(context.Context, Invocation) error, evts ...Event) error {
	return s.runTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if write != nil {
			if err := write(ctx, inv); err != nil {
				return err
			}
		}
		for _, e := range evts {
			if err := s.events.Create(ctx, e); err != nil {
				return err
			}
			if s.hook != nil {
				if err := s.hook(ctx, tx, Change{Type: e.EventType, Invocation: inv, Payload: e.Payload, At: e.CreatedAt}); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// commitOrLog is commit for paths that cannot stop the call any further (a
// denial, or recording a tool that already ran): a failure is logged loudly
// rather than returned.
func (s *Service) commitOrLog(ctx context.Context, inv Invocation, write func(context.Context, Invocation) error, evts ...Event) {
	if err := s.commit(ctx, inv, write, evts...); err != nil {
		types := make([]string, len(evts))
		for i, e := range evts {
			types[i] = e.EventType
		}
		slog.Error("invocation change NOT recorded", "invocation_id", inv.InvocationID, "status", inv.Status, "events", types, "error", err)
	}
}

// updateFrom writes inv only if its row is still in status from, so two
// processes cannot both decide or finish the same call.
func (s *Service) updateFrom(from Status) func(context.Context, Invocation) error {
	return func(ctx context.Context, inv Invocation) error {
		ok, err := s.invocations.UpdateResultFrom(ctx, inv, from)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("invocation %s is no longer %s: %w", inv.InvocationID, from, ErrInvalidTransition)
		}
		return nil
	}
}

// decide asks the decider, failing closed to deny on an error, a panic or an
// unknown verdict.
func (s *Service) decide(ctx context.Context, call ToolCall) (d Decision) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("decider panicked; denying the call", "invocation_id", call.InvocationID, "panic", r)
			d = Decision{Verdict: VerdictDeny, Reason: fmt.Sprintf("decider panic: %v", r)}
		}
	}()
	d, err := s.decider(ctx, call)
	if err != nil {
		return Decision{Verdict: VerdictDeny, Reason: "decider error: " + err.Error()}
	}
	switch d.Verdict {
	case VerdictAllow, VerdictNotify, VerdictDeny, VerdictPending:
		// A decider's account is model-written text that reaches the agent:
		// bound it here, where every decider's answer arrives, so nothing
		// downstream has to trust its size or its spelling.
		d.Explanation = d.Explanation.Bounded()
		return d
	}
	return Decision{Verdict: VerdictDeny, Reason: fmt.Sprintf("decider returned unknown verdict %q", d.Verdict)}
}

// explanation is d's account of itself, or nil. Safe on a nil Decision, so
// callers that may not have consulted a decider can ask unconditionally.
func (d *Decision) explanation() *Explanation {
	if d == nil {
		return nil
	}
	return d.Explanation
}

func (d Decision) disposition() policy.Decision {
	switch d.Verdict {
	case VerdictAllow, VerdictNotify:
		return policy.Decision{Disposition: policy.DispositionAuto, Reason: d.Reason}
	case VerdictPending:
		return policy.Decision{Disposition: policy.DispositionHuman, Reason: d.Reason}
	}
	return policy.Decision{Disposition: policy.DispositionNever, Reason: d.Reason}
}

// StuckReportTool is the MCP tool an agent calls to report that it cannot
// finish its task within its authority. See ReportStuck.
const StuckReportTool = "atryum_report_stuck"

// ErrInvalidStuckReport is wrapped by ReportStuck's errors that the agent can
// fix: a missing task or boundary, or an unknown server.
var ErrInvalidStuckReport = errors.New("invalid stuck report")

// stuckReportLimits caps each stuck-report field, in bytes.
var stuckReportLimits = map[string]int{"task": 1024, "boundary": 1024, "detail": 4096}

// ReportStuck records an agent's stuck report (task, boundary and an optional
// detail, each capped; other arguments are dropped) as an invocation of
// StuckReportTool on server, in status reported. It authorizes nothing: no
// decider, rule or upstream is involved. The row, its event and the event
// hook commit together, and the hook sees only "invocation.stuck_reported",
// never "invocation.created", so an embedder does not count it as an action.
func (s *Service) ReportStuck(ctx context.Context, server string, args map[string]any) (InvocationResponse, error) {
	report := map[string]string{}
	for k, limit := range stuckReportLimits {
		v, _ := args[k].(string)
		v = strings.TrimSpace(v)
		if len(v) > limit {
			v = strings.ToValidUTF8(v[:limit], "") + "…"
		}
		if v != "" {
			report[k] = v
		}
	}
	if report["task"] == "" || report["boundary"] == "" {
		return InvocationResponse{}, fmt.Errorf("%w: task and boundary are required", ErrInvalidStuckReport)
	}
	upstream, err := s.resolver.ResolveContext(ctx, server)
	if err != nil {
		return InvocationResponse{}, fmt.Errorf("%w: unknown server %q", ErrInvalidStuckReport, server)
	}
	input := mustJSON(report)
	now := time.Now().UTC()
	inv := Invocation{
		InvocationID: "inv_" + uuid.NewString(),
		Tool:         StuckReportTool,
		Upstream:     upstream.Name,
		Status:       StatusReported,
		Input:        input,
		SubmittedAt:  now,
		CompletedAt:  &now,
	}
	agentID := auth.AgentIDFromContext(ctx)
	if agentID != "" {
		inv.AgentID = &agentID
	}
	inv.Auth = authOf(ctx, agentID)
	inv.Response = mustJSON(map[string]any{"content": []map[string]any{{"type": "text", "text": "Stuck report recorded (" + inv.InvocationID + "). Stop this task; a person will follow up."}}})
	evt := Event{
		InvocationID: inv.InvocationID,
		EventType:    "invocation.stuck_reported",
		Payload:      mustJSON(map[string]any{"upstream": upstream.Name, "arguments": json.RawMessage(input)}),
		CreatedAt:    now,
	}
	err = s.runTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if err := s.invocations.Create(ctx, inv); err != nil {
			return err
		}
		if err := s.events.Create(ctx, evt); err != nil {
			return err
		}
		if s.hook == nil {
			return nil
		}
		return s.hook(ctx, tx, Change{Type: evt.EventType, Invocation: inv, Payload: evt.Payload, At: now})
	})
	if err != nil {
		return InvocationResponse{}, err
	}
	return s.toResponse(inv), nil
}

// ErrIdempotencyConflict means the caller reused an idempotency key for a
// different request (another tool, server or input, or the other submit
// path). The earlier invocation is not returned or described.
var ErrIdempotencyConflict = errors.New("idempotency key was already used for a different request")

// priorRequest returns the invocation an earlier request from agentID created
// with key, when the request was the same: same upstream (empty on the submit
// path), tool and input (compared as JSON values). A different request
// returns ErrIdempotencyConflict; no key or no earlier request returns nil.
//
// Keys are scoped to agentID, the identity the invocation is recorded under:
// the verified agent when auth is on (always with WithRequireAgentAuth), else
// on the submit path the agent_id the caller declares, else "" (anonymous
// callers share one scope). Without verified identity a key is only as
// private as the agent ID a caller claims, which is how standalone Atryum
// without auth trusts its callers anyway.
func (s *Service) priorRequest(ctx context.Context, agentID string, key *string, upstream, tool string, input []byte) (*Invocation, error) {
	if key == nil || *key == "" {
		return nil, nil
	}
	prior, err := s.invocations.GetByIdempotencyKey(ctx, agentID, *key)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if prior.Upstream != upstream || prior.Tool != tool || !sameJSON(prior.Input, input) {
		return nil, ErrIdempotencyConflict
	}
	return &prior, nil
}

// lostCreateRace handles a failed insert: when a concurrent request with the
// same key won, it answers like priorRequest; otherwise it returns createErr.
func (s *Service) lostCreateRace(ctx context.Context, agentID string, key *string, upstream, tool string, input []byte, createErr error) (InvocationResponse, error) {
	prior, err := s.priorRequest(ctx, agentID, key, upstream, tool, input)
	if err != nil || prior != nil {
		return prior.response(s), err
	}
	return InvocationResponse{}, createErr
}

func (inv *Invocation) response(s *Service) InvocationResponse {
	if inv == nil {
		return InvocationResponse{}
	}
	return s.toResponse(*inv)
}

// sameJSON compares two JSON documents as values, ignoring key order and
// whitespace. Numbers compare by their exact text (json.Number), so integers
// beyond float64 precision stay distinct; equal numbers spelled differently
// (100 and 1e2) count as different, which errs toward a conflict.
func sameJSON(a, b []byte) bool {
	x, errA := decodeExact(a)
	y, errB := decodeExact(b)
	return errA == nil && errB == nil && reflect.DeepEqual(x, y)
}

func decodeExact(b []byte) (any, error) {
	var v any
	return v, unmarshalExact(b, &v)
}

// unmarshalExact is json.Unmarshal keeping numbers as json.Number, so a large
// integer keeps its digits instead of rounding through float64.
func unmarshalExact(b []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	return d.Decode(v)
}
