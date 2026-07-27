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
	// PlanID links this invocation to the approved plan whose pass
	// auto-approved it, when applicable.
	PlanID *string `json:"plan_id,omitempty"`
	// PlanStepIndex is the zero-based position of the declared plan action
	// matched by this invocation. It is internal state used to prevent a plan
	// from moving backwards after a later step has run.
	PlanStepIndex *int    `json:"-"`
	AgentID       *string `json:"agent_id,omitempty"`
	// SessionID links this invocation to an external harness session (see
	// ExternalSession). Set on the Invocations API path so the judge can be
	// given the session's prior tool calls as context.
	SessionID *string `json:"session_id,omitempty"`
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
	PlanID     string   // filters to invocations linked to one approved plan
	SessionID  string   // filters to invocations belonging to one external session
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
	// Meta carries the agent's MCP tools/call params._meta (e.g.
	// progressToken) through to the upstream call so progress/logging
	// notifications the upstream emits can be correlated back to the
	// agent's own request.
	Meta map[string]any `json:"-"`
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
	// ClientSessionID is the harness's own session/thread identifier (the id it
	// already tracks for its host conversation). When set and no SessionID is
	// provided, Atryum resolves the internal session with get-or-create
	// semantics keyed by (agent binding, client_session_id): first sight mints
	// the internal ses_ row, subsequent calls reuse it, and an expired row under
	// the same key rolls over to a fresh session. This is the preferred path —
	// the harness never has to mint, persist, or echo an Atryum session id.
	ClientSessionID string `json:"client_session_id,omitempty"`
	// Deprecated: prefer ClientSessionID (server-side get-or-create). SessionID
	// is an Atryum-minted session identifier (from the deprecated POST
	// /api/v1/external/sessions). When set, Atryum reconstructs the judge's
	// session context from the prior invocations it recorded for this session,
	// rather than trusting a harness-supplied context blob. Still fully
	// functional; the ownership/expiry/unbound rejections on this path remain
	// hard.
	SessionID string `json:"session_id,omitempty"`
	// SessionContext is the agent's recent session history (human messages and
	// tool calls/results) passed to the LLM judge. It is populated in-process by
	// the Claude managed-agents watcher. Harnesses on the Invocations API must
	// use SessionID instead — they cannot supply their own context blob.
	SessionContext         string `json:"session_context,omitempty"`
	SessionContextMessages int    `json:"session_context_messages,omitempty"`
	// Deprecated: use SessionContext / SessionContextMessages. Older harness
	// plugins send chat_context / chat_context_messages / context; these are
	// still accepted on input and treated as SessionContext.
	ChatContext         string `json:"chat_context,omitempty"`
	ChatContextMessages int    `json:"chat_context_messages,omitempty"`
	Context             string `json:"context,omitempty"`
	// ClientName / ClientVersion identify the harness making the call.
	// Optional — when omitted, Source is used for ClientName. Prefer these
	// when the caller knows its own identity (e.g. amp plugin sending
	// "amp" + its build version) rather than relying on Source which
	// doubles as the approval-rule matching key.
	ClientName    string `json:"client_name,omitempty"`
	ClientVersion string `json:"client_version,omitempty"`
	// AgentID is a self-declared runtime agent identifier (e.g. an amp
	// session id, or a stable harness identity). When no OAuth identity is
	// attached to the request (the external endpoint is anonymous), this
	// field is used to tag the invocation and to resolve the Agent Record
	// via the agents.agent_ids array. A verified OAuth identity in the
	// request context always wins. Optional.
	AgentID string `json:"agent_id,omitempty"`
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
	PlanID        *string   `json:"plan_id,omitempty"`
	AgentID       *string   `json:"agent_id,omitempty"`
	// SessionID is the internal Atryum session (ses_...) this invocation was
	// linked to, if any. Exposed for observability/debugging — clients never
	// need to send it back. Populated on both the explicit session_id path and
	// the client_session_id get-or-create path.
	SessionID *string `json:"session_id,omitempty"`
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

// ExternalSession is a harness session minted by Atryum (POST
// /api/v1/external/sessions). The harness calls once at startup, gets back an
// ID, and echoes that ID on every subsequent tool-call submission. Each
// invocation is then linked to (AgentID, ID) so the judge can be given the
// session's prior tool calls as context.
//
// The ID is Atryum-minted (not self-asserted) and bound to AgentID at creation;
// a Submit whose authenticated agent does not own the session is rejected. This
// keeps the trust boundary at "trust the harness, not the LLM": the harness can
// only ever read/poison its own session.
//
// AgentID must be non-empty: session history is an identity-keyed feature, and
// an empty binding is not a valid identity, just the absence of one. Two
// anonymous callers both bound to "" would satisfy a naive equality check and
// read/append to each other's history, so Atryum requires a non-empty binding
// at mint time (CreateSession) and re-checks it at use time
// (lookupSessionForAgent) in case older, pre-enforcement rows exist. Harnesses
// with no stable identity claim fall back to the no-session_id path, which
// evaluates each call without cross-call history instead.
type ExternalSession struct {
	ID      string
	AgentID string
	// Harness identifies the calling harness (e.g. "amp", "claude"). Bookkeeping
	// only; Atryum keys off ID.
	Harness string
	// ClientSessionID is the harness's own session identifier, kept for
	// cross-referencing. Atryum does not key off it.
	ClientSessionID string
	CreatedAt       time.Time
	LastSeenAt      time.Time
	// ExpiresAt is a soft lifecycle boundary. Expired sessions are rejected so
	// stale leaked IDs do not remain usable forever; invocation audit rows remain
	// governed by the broader invocation retention policy.
	ExpiresAt time.Time
}

// CreateSessionRequest is the body for POST /api/v1/external/sessions.
type CreateSessionRequest struct {
	// AgentID is the self-declared agent id used only in no-auth mode; when an
	// authenticated identity is present it wins.
	AgentID         string `json:"agent_id,omitempty"`
	Harness         string `json:"harness,omitempty"`
	ClientSessionID string `json:"client_session_id,omitempty"`
}

// SessionResponse is returned from POST /api/v1/external/sessions.
type SessionResponse struct {
	SessionID       string    `json:"session_id"`
	AgentID         string    `json:"agent_id,omitempty"`
	Harness         string    `json:"harness,omitempty"`
	ClientSessionID string    `json:"client_session_id,omitempty"`
	ExpiresAt       time.Time `json:"expires_at"`
}
