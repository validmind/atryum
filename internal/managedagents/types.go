// Package managedagents bridges Anthropic's Claude Managed Agents "events and
// streaming" API into Atryum's invocation/approval engine.
//
// Anthropic runs the agent loop and executes tools on its own infrastructure;
// it does not call Atryum. To gate those tool calls, Atryum connects outbound
// to a registered session's event stream, records every session event, and —
// when the session blocks on a tool call (a `session.status_idle` event with
// stop_reason `requires_action`) — runs the normal Atryum approval rules and
// reports the decision back to Claude via a `user.tool_confirmation` (hosted /
// MCP tools) or `user.custom_tool_result` (custom tools) event.
package managedagents

import (
	"context"
	"encoding/json"
	"time"
)

// Anthropic API constants. These are pinned rather than configurable: the
// Managed Agents surface is gated on exactly these beta/version headers.
const (
	defaultBaseURL    = "https://api.anthropic.com"
	anthropicVer      = "2023-06-01"
	managedAgentsBeta = "managed-agents-2026-04-01"

	defaultClientName       = "claude-managed-agents"
	defaultPollInterval     = time.Second
	defaultReconnectBackoff = 2 * time.Second
)

// Config holds the bridge's runtime settings.
type Config struct {
	BaseURL          string
	APIKey           string
	PollInterval     time.Duration
	ReconnectBackoff time.Duration
	ClientName       string
	ClientVersion    string
}

func (c Config) withDefaults() Config {
	if c.BaseURL == "" {
		c.BaseURL = defaultBaseURL
	}
	if c.PollInterval <= 0 {
		c.PollInterval = defaultPollInterval
	}
	if c.ReconnectBackoff <= 0 {
		c.ReconnectBackoff = defaultReconnectBackoff
	}
	if c.ClientName == "" {
		c.ClientName = defaultClientName
	}
	return c
}

// RawEvent is a single Anthropic session event with its envelope fields
// extracted and the full body preserved in Raw.
type RawEvent struct {
	ID          string
	Type        string
	ProcessedAt time.Time
	Raw         json.RawMessage
}

// OutboundEvent is an event Atryum sends back to a session (e.g. a tool
// confirmation). Fields is marshalled inline alongside Type.
type OutboundEvent struct {
	Type   string
	Fields map[string]any
}

// MarshalJSON flattens Type and Fields into a single object so the wire format
// is {"type": "...", <fields>}.
func (e OutboundEvent) MarshalJSON() ([]byte, error) {
	out := make(map[string]any, len(e.Fields)+1)
	for k, v := range e.Fields {
		out[k] = v
	}
	out["type"] = e.Type
	return json.Marshal(out)
}

// RegisterSessionRequest is the admin API payload to start watching a session.
type RegisterSessionRequest struct {
	SessionID   string `json:"session_id"`
	AgentID     string `json:"agent_id,omitempty"`
	Description string `json:"description,omitempty"`
}

// SessionRegistration is returned to the admin caller and persisted.
type SessionRegistration struct {
	SessionID   string    `json:"session_id"`
	AgentID     string    `json:"agent_id,omitempty"`
	Description string    `json:"description,omitempty"`
	LastEventID string    `json:"last_event_id,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// toolUse holds the normalized fields parsed out of an agent.tool_use,
// agent.mcp_tool_use, or agent.custom_tool_use event.
type toolUse struct {
	EventID    string
	Kind       string // the Anthropic event type
	ToolName   string
	ServerName string // MCP server name, when Kind == agent.mcp_tool_use
	Input      map[string]any
	IsCustom   bool
}

// AnthropicClient is the minimal Anthropic Managed Agents surface the bridge
// needs. It is an interface so tests can supply a fake.
type AnthropicClient interface {
	// ListEventsSince returns events newer than afterEventID, oldest first.
	// When afterEventID is empty it returns the full history.
	ListEventsSince(ctx context.Context, sessionID, afterEventID string) ([]RawEvent, error)
	// StreamEvents opens the live SSE stream for a session.
	StreamEvents(ctx context.Context, sessionID string) (EventStream, error)
	// SendEvents posts client events back to a session.
	SendEvents(ctx context.Context, sessionID string, events []OutboundEvent) error
}

// EventStream is a live SSE subscription. Next blocks until the next event or
// an error (including io.EOF / context cancellation on close).
type EventStream interface {
	Next(ctx context.Context) (RawEvent, error)
	Close() error
}
