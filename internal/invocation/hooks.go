package invocation

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

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
}

// Decision is a Decider's result. It is recorded under "decision" in the
// invocation.received event payload.
type Decision struct {
	Verdict  Verdict `json:"verdict"`
	Reason   string  `json:"reason,omitempty"`
	ReviewID string  `json:"review_id,omitempty"`
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

// decide asks the decider, failing closed to deny on error or an unknown verdict.
func (s *Service) decide(ctx context.Context, call ToolCall) Decision {
	d, err := s.decider(ctx, call)
	if err != nil {
		return Decision{Verdict: VerdictDeny, Reason: "decider error: " + err.Error()}
	}
	switch d.Verdict {
	case VerdictAllow, VerdictNotify, VerdictDeny, VerdictPending:
		return d
	}
	return Decision{Verdict: VerdictDeny, Reason: fmt.Sprintf("decider returned unknown verdict %q", d.Verdict)}
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
