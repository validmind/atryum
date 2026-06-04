package invocation

import (
	"encoding/json"
	"time"
)

type Status string

const (
	StatusReceived        Status = "received"
	StatusExecuting       Status = "executing"
	StatusPendingApproval Status = "pending_approval"
	StatusApproved        Status = "approved"
	StatusDenied          Status = "denied"
	StatusExpired         Status = "expired"
	StatusCancelled       Status = "cancelled"
	StatusSucceeded       Status = "succeeded"
	StatusFailed          Status = "failed"
)

type Approval struct {
	Status          string   `json:"status"`
	RequestID       *string  `json:"request_id,omitempty"`
	ExpiresAt       *string  `json:"expires_at,omitempty"`
	Reason          *string  `json:"reason,omitempty"`
	ActorID         *string  `json:"actor_id,omitempty"`
	DecisionAt      *string  `json:"decision_at,omitempty"`
	ConfidenceScore *float64 `json:"confidence_score,omitempty"`
}

type Invocation struct {
	InvocationID   string    `json:"invocation_id"`
	RequestID      *string   `json:"request_id,omitempty"`
	IdempotencyKey *string   `json:"idempotency_key,omitempty"`
	Tool           string    `json:"tool"`
	Upstream       string    `json:"upstream"`
	Status         Status    `json:"status"`
	Approval       *Approval `json:"approval"`
	MatchedRuleID  *string   `json:"matched_rule_id,omitempty"`
	AgentID        *string   `json:"agent_id,omitempty"`
	// ClientName / ClientVersion mirror the MCP `initialize.clientInfo`
	// the harness reported on the connection that issued this invocation.
	// Captured even when auth is disabled (no agent_id) so the UI can still
	// show "Amp 0.0.1234" for anonymous traffic.
	ClientName    *string    `json:"client_name,omitempty"`
	ClientVersion *string    `json:"client_version,omitempty"`
	Input         []byte     `json:"-"`
	Response      []byte     `json:"-"`
	Error         []byte     `json:"-"`
	Summary       *string    `json:"summary,omitempty"`
	SubmittedAt   time.Time  `json:"submitted_at"`
	CompletedAt   *time.Time `json:"completed_at,omitempty"`
}

type Event struct {
	ID           int64           `json:"-"`
	InvocationID string          `json:"-"`
	EventType    string          `json:"-"`
	Payload      json.RawMessage `json:"-"`
	CreatedAt    time.Time       `json:"-"`
}

type InvocationListFilter struct {
	Offset     uint64
	Limit      uint64
	Server     string
	Tool       string
	Status     string
	AgentIDs   []string // filters to invocations whose agent_id is in this list
	ClientName string
	StartDate  *time.Time
	EndDate    *time.Time
}

type EventListFilter struct {
	Offset uint64
	Limit  uint64
}

type EventResponse struct {
	Type      string          `json:"type"`
	Timestamp time.Time       `json:"timestamp"`
	Data      json.RawMessage `json:"data"`
}

type CreateInvocationRequest struct {
	Server         string         `json:"server,omitempty"`
	Tool           string         `json:"tool"`
	Input          map[string]any `json:"input"`
	RequestID      *string        `json:"request_id,omitempty"`
	IdempotencyKey *string        `json:"idempotency_key,omitempty"`
	// ClientName / ClientVersion let the caller pass through the harness's
	// MCP `initialize.clientInfo` for this specific call. Used to populate
	// the per-row fallback for anonymous (no agent_id) invocations.
	ClientName    string `json:"-"`
	ClientVersion string `json:"-"`
}

// ExternalSubmitRequest is used by callers that execute the tool themselves
// (e.g. an amp coding harness via a plugin) and only want Atryum to gate the
// call for human approval and record audit events. Atryum will NOT execute
// the tool when this path is used.
type ExternalSubmitRequest struct {
	Source         string         `json:"source"`
	Tool           string         `json:"tool"`
	Description    string         `json:"description,omitempty"`
	Input          map[string]any `json:"input"`
	RequestID      *string        `json:"request_id,omitempty"`
	IdempotencyKey *string        `json:"idempotency_key,omitempty"`
	ThreadID       string         `json:"thread_id,omitempty"`
	// ClientName / ClientVersion identify the harness making the call.
	// Optional — when omitted, Source is used for ClientName. Prefer these
	// when the caller knows its own identity (e.g. amp plugin sending
	// "amp" + its build version) rather than relying on Source which
	// doubles as the approval-rule matching key.
	ClientName    string `json:"client_name,omitempty"`
	ClientVersion string `json:"client_version,omitempty"`
}

// ExternalExecutionUpdate is sent by the external executor to report on a
// pending or completed run. ExecutionStatus is one of:
//
//	running | completed | failed | cancelled
type ExternalExecutionUpdate struct {
	ExecutionStatus string          `json:"execution_status"`
	Result          json.RawMessage `json:"result,omitempty"`
	Error           json.RawMessage `json:"error,omitempty"`
	Message         string          `json:"message,omitempty"`
}

type InvocationResponse struct {
	InvocationID  string    `json:"invocation_id"`
	ServerName    string    `json:"server_name"`
	ToolName      string    `json:"tool_name"`
	Status        Status    `json:"status"`
	Approval      *Approval `json:"approval"`
	MatchedRuleID *string   `json:"matched_rule_id,omitempty"`
	AgentID       *string   `json:"agent_id,omitempty"`
	// AgentClientName / AgentClientVersion identify the MCP client software
	// (e.g. "amp", "cursor", "claude-code") captured from the most recent
	// `initialize` handshake associated with this AgentID. They describe the
	// agent type/harness, not the human user.
	AgentClientName    *string `json:"agent_client_name,omitempty"`
	AgentClientVersion *string `json:"agent_client_version,omitempty"`
	// UserID is the JWT subject claim (`sub`) captured alongside the agent's
	// most recent `initialize` — the human/service principal behind the agent
	// when available.
	UserID      *string         `json:"user_id,omitempty"`
	RequestID   *string         `json:"request_id,omitempty"`
	Input       json.RawMessage `json:"input,omitempty"`
	SubmittedAt time.Time       `json:"submitted_at"`
	CompletedAt *time.Time      `json:"completed_at,omitempty"`
	Result      json.RawMessage `json:"result,omitempty"`
	Error       json.RawMessage `json:"error,omitempty"`
	Summary     string          `json:"summary,omitempty"`
}

type InvocationListResponse struct {
	Items  []InvocationResponse `json:"items"`
	Total  int                  `json:"total"`
	Offset uint64               `json:"offset"`
	Limit  uint64               `json:"limit"`
}

type EventListResponse struct {
	Items  []EventResponse `json:"items"`
	Total  int             `json:"total"`
	Offset uint64          `json:"offset"`
	Limit  uint64          `json:"limit"`
}
