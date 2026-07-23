package invocation

import (
	"encoding/json"
	"time"
)

type PlanStatus string

const (
	PlanStatusReceived        PlanStatus = "received"
	PlanStatusPendingApproval PlanStatus = "pending_approval"
	PlanStatusApproved        PlanStatus = "approved"
	PlanStatusDenied          PlanStatus = "denied"
	PlanStatusNeedsRevision   PlanStatus = "needs_revision"
	PlanStatusSuperseded      PlanStatus = "superseded"
	PlanStatusCompleted       PlanStatus = "completed"
	PlanStatusExpired         PlanStatus = "expired"
	PlanStatusCancelled       PlanStatus = "cancelled"
)

// PlanAction is a single intended tool call declared up front in a plan. Tool
// and Server are matched exactly against later invocations; an omitted Server
// is stamped with the submitting plan source. Description and InputSummary are
// shown to reviewers and to the adherence judge, which compares them with the
// concrete later invocation.
type PlanAction struct {
	Tool         string `json:"tool"`
	Server       string `json:"server,omitempty"`
	Description  string `json:"description,omitempty"`
	InputSummary string `json:"input_summary,omitempty"`
}

// Plan is a batch of intended actions submitted for review before any tool
// executes. An approved plan grants a scoped pass only after the adherence
// judge confirms that a concrete matching invocation follows the plan; the
// sole exception is a read-only poll of the plan's own status.
type Plan struct {
	PlanID        string       `json:"plan_id"`
	AgentID       string       `json:"agent_id"`
	Source        string       `json:"source,omitempty"`
	ThreadID      string       `json:"thread_id,omitempty"`
	Goal          string       `json:"goal"`
	Rationale     string       `json:"rationale,omitempty"`
	Actions       []PlanAction `json:"actions"`
	Status        PlanStatus   `json:"status"`
	Approval      *Approval    `json:"approval,omitempty"`
	MatchedRuleID *string      `json:"matched_rule_id,omitempty"`
	Feedback      string       `json:"feedback,omitempty"`
	ParentPlanID  *string      `json:"parent_plan_id,omitempty"`
	Revision      int          `json:"revision"`
	// TTLSeconds is how long the approval pass stays valid once granted;
	// ExpiresAt is stamped from it at decision time.
	TTLSeconds    int        `json:"ttl_seconds"`
	ClientName    *string    `json:"client_name,omitempty"`
	ClientVersion *string    `json:"client_version,omitempty"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
	SubmittedAt   time.Time  `json:"submitted_at"`
	DecidedAt     *time.Time `json:"decided_at,omitempty"`
}

// PlanSubmitRequest is the agent-facing payload for proposing a plan.
// TTLSeconds bounds how long the approval pass stays valid once granted.
type PlanSubmitRequest struct {
	Source        string       `json:"source,omitempty"`
	AgentID       string       `json:"agent_id,omitempty"`
	ThreadID      string       `json:"thread_id,omitempty"`
	Goal          string       `json:"goal"`
	Rationale     string       `json:"rationale,omitempty"`
	Actions       []PlanAction `json:"actions"`
	RevisionOf    string       `json:"revision_of,omitempty"`
	TTLSeconds    int          `json:"ttl_seconds,omitempty"`
	ChatContext   string       `json:"chat_context,omitempty"`
	ClientName    string       `json:"client_name,omitempty"`
	ClientVersion string       `json:"client_version,omitempty"`
}

type PlanEvent struct {
	ID        int64           `json:"-"`
	PlanID    string          `json:"-"`
	EventType string          `json:"-"`
	Payload   json.RawMessage `json:"-"`
	CreatedAt time.Time       `json:"-"`
}

type PlanListFilter struct {
	Offset  uint64
	Limit   uint64
	Status  string
	AgentID string
}

type PlanListResponse struct {
	Items  []Plan `json:"items"`
	Total  int    `json:"total"`
	Offset uint64 `json:"offset"`
	Limit  uint64 `json:"limit"`
}
