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
	Status     string  `json:"status"`
	RequestID  *string `json:"request_id,omitempty"`
	ExpiresAt  *string `json:"expires_at,omitempty"`
	Reason     *string `json:"reason,omitempty"`
	ActorID    *string `json:"actor_id,omitempty"`
	DecisionAt *string `json:"decision_at,omitempty"`
}

type Invocation struct {
	InvocationID   string     `json:"invocation_id"`
	RequestID      *string    `json:"request_id,omitempty"`
	IdempotencyKey *string    `json:"idempotency_key,omitempty"`
	Tool           string     `json:"tool"`
	Upstream       string     `json:"upstream"`
	Status         Status     `json:"status"`
	Approval       *Approval  `json:"approval"`
	Input          []byte     `json:"-"`
	Response       []byte     `json:"-"`
	Error          []byte     `json:"-"`
	SubmittedAt    time.Time  `json:"submitted_at"`
	CompletedAt    *time.Time `json:"completed_at,omitempty"`
}

type Event struct {
	ID           int64           `json:"-"`
	InvocationID string          `json:"-"`
	EventType    string          `json:"-"`
	Payload      json.RawMessage `json:"-"`
	CreatedAt    time.Time       `json:"-"`
}

type InvocationListFilter struct {
	Offset uint64
	Limit  uint64
	Server string
	Tool   string
	Status string
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
}

type InvocationResponse struct {
	InvocationID string          `json:"invocation_id"`
	ServerName   string          `json:"server_name"`
	ToolName     string          `json:"tool_name"`
	Status       Status          `json:"status"`
	Approval     *Approval       `json:"approval"`
	RequestID    *string         `json:"request_id,omitempty"`
	Input        json.RawMessage `json:"input,omitempty"`
	SubmittedAt  time.Time       `json:"submitted_at"`
	CompletedAt  *time.Time      `json:"completed_at,omitempty"`
	Result       json.RawMessage `json:"result,omitempty"`
	Error        json.RawMessage `json:"error,omitempty"`
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
