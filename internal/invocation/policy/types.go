package policy

import (
	"context"
	"time"
)

// Disposition is the outcome of policy evaluation for a tool call.
// Maps directly to the four tiers in the disposition engine design.
type Disposition string

const (
	// DispositionAuto executes immediately and logs the action. (Green channel)
	DispositionAuto Disposition = "auto"
	// DispositionHuman queues the call for a single human approver. (Yellow channel)
	DispositionHuman Disposition = "human"
	// DispositionWorkflow delegates to a multi-step approval chain (e.g. ValidMind). (Yellow channel)
	DispositionWorkflow Disposition = "workflow"
	// DispositionNever hard-denies the call with no execution path. (Red channel)
	DispositionNever Disposition = "never"
)

// CallContext carries the information available at policy evaluation time.
// AgentID is empty in the PoC; it will be populated once identity is plumbed
// through from auth middleware (US-4).
type CallContext struct {
	AgentID string
	Server  string
	Tool    string
	Input   map[string]any
}

// Decision is the outcome returned by a Provider.
type Decision struct {
	Disposition Disposition
	// Reason is a human-readable policy justification included in the audit log.
	Reason string
	// ExpiresAt is non-nil for time-bounded auto approvals (timed_approve).
	ExpiresAt *time.Time
}

// Provider evaluates a tool call and returns a disposition decision.
// Implementations must be safe for concurrent use.
type Provider interface {
	ID() string
	DisplayName() string
	Evaluate(ctx context.Context, call CallContext) (Decision, error)
}

// WindowSetter is an optional interface for providers that support a dynamic
// approval window settable at runtime (e.g. TimedApproveProvider).
type WindowSetter interface {
	SetWindow(expiresAt time.Time)
	WindowExpiresAt() *time.Time
}
