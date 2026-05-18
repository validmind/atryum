// Package claudeagents wraps Anthropic's Managed Agents API for atryum.
//
// Atryum is a single-tenant approval gateway: it polls registered Claude
// sessions for tool-approval events (`session.status_idle` with
// `stop_reason.type == requires_action`), feeds them into the existing
// invocation/rules pipeline, and posts back `user.tool_confirmation` events
// when the human (or a rule) decides allow/deny.
//
// This file holds the thin HTTP wrapper. The polling loop lives in
// watcher.go.
package claudeagents

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultBaseURL is the production Anthropic API base.
const DefaultBaseURL = "https://api.anthropic.com"

// AnthropicVersion is the API version pinned by atryum. Managed Agents is a
// beta endpoint; the AnthropicBeta header opts in to it.
const (
	AnthropicVersion = "2023-06-01"
	AnthropicBeta    = "managed-agents-2026-04-01"
)

// Client is a small HTTP wrapper around the Managed Agents endpoints atryum
// needs. It is safe for concurrent use.
type Client struct {
	apiKey     string
	baseURL    string
	httpClient *http.Client
}

// Options configures a Client. Empty fields fall back to sensible defaults.
type Options struct {
	APIKey     string
	BaseURL    string
	HTTPClient *http.Client
}

// New builds a Client. APIKey is required.
func New(opts Options) (*Client, error) {
	if strings.TrimSpace(opts.APIKey) == "" {
		return nil, fmt.Errorf("claude agents: api key is required")
	}
	base := strings.TrimRight(opts.BaseURL, "/")
	if base == "" {
		base = DefaultBaseURL
	}
	hc := opts.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{apiKey: opts.APIKey, baseURL: base, httpClient: hc}, nil
}

// Event is a minimal projection of an event in the Agents API. Only the
// fields atryum needs are typed; the original payload is preserved in Raw
// so callers can inspect tool inputs without us hard-coding every shape.
type Event struct {
	ID         string          `json:"id"`
	Type       string          `json:"type"`
	CreatedAt  time.Time       `json:"created_at"`
	StopReason *StopReason     `json:"stop_reason,omitempty"`
	ToolUseID  string          `json:"tool_use_id,omitempty"`
	Name       string          `json:"name,omitempty"`
	Input      json.RawMessage `json:"input,omitempty"`
	Raw        json.RawMessage `json:"-"`
}

// StopReason mirrors the structure used on `session.status_idle` events.
type StopReason struct {
	Type     string   `json:"type"`
	EventIDs []string `json:"event_ids,omitempty"`
}

// Agent is a minimal projection of an Anthropic-side managed agent. Only the
// fields atryum's discovery loop needs are typed.
type Agent struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// ArchivedAt is set when the agent has been archived. Discovery skips
	// archived agents because no new sessions can reference them.
	ArchivedAt string `json:"archived_at,omitempty"`
}

// ListAgentsOptions controls a single GET /v1/agents call.
type ListAgentsOptions struct {
	Limit int    // 0 means no explicit limit
	Page  string // pagination cursor returned by a previous call
	Order string // "asc" or "desc"
}

type listAgentsResponse struct {
	Data     []Agent `json:"data"`
	NextPage string  `json:"next_page,omitempty"`
	HasMore  bool    `json:"has_more,omitempty"`
}

// ListAgents returns one page of agents plus the next-page cursor (empty when
// there are no more pages).
func (c *Client) ListAgents(ctx context.Context, opts ListAgentsOptions) ([]Agent, string, error) {
	u, err := url.Parse(c.baseURL + "/v1/agents")
	if err != nil {
		return nil, "", fmt.Errorf("build agents url: %w", err)
	}
	q := u.Query()
	if opts.Limit > 0 {
		q.Set("limit", fmt.Sprintf("%d", opts.Limit))
	}
	if opts.Page != "" {
		q.Set("page", opts.Page)
	}
	if opts.Order != "" {
		q.Set("order", opts.Order)
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "", err
	}
	c.applyAuthHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("list agents: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("read agents body: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		return nil, "", fmt.Errorf("list agents: status %d: %s", resp.StatusCode, truncate(string(body), 512))
	}
	var parsed listAgentsResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, "", fmt.Errorf("decode agents: %w", err)
	}
	return parsed.Data, parsed.NextPage, nil
}

// Session is a minimal projection of an Anthropic-side managed session. The
// nested Agent carries the session's owning agent_id, which discovery uses to
// link sessions back to atryum's claude_agents rows.
type Session struct {
	ID     string        `json:"id"`
	Status string        `json:"status,omitempty"`
	Agent  SessionAgent  `json:"agent"`
}

// SessionAgent is the resolved-agent snapshot embedded on each session.
type SessionAgent struct {
	ID      string `json:"id"`
	Name    string `json:"name,omitempty"`
	Version int    `json:"version,omitempty"`
}

// ListSessionsOptions controls a single GET /v1/sessions call.
type ListSessionsOptions struct {
	AgentID         string   // optional filter by Anthropic agent id
	Statuses        []string // optional filter; e.g. "idle", "running"
	IncludeArchived bool
	Limit           int
	Page            string
	Order           string // "asc" or "desc"
}

type listSessionsResponse struct {
	Data     []Session `json:"data"`
	NextPage string    `json:"next_page,omitempty"`
	HasMore  bool      `json:"has_more,omitempty"`
}

// ListSessions returns one page of sessions plus the next-page cursor.
func (c *Client) ListSessions(ctx context.Context, opts ListSessionsOptions) ([]Session, string, error) {
	u, err := url.Parse(c.baseURL + "/v1/sessions")
	if err != nil {
		return nil, "", fmt.Errorf("build sessions url: %w", err)
	}
	q := u.Query()
	if opts.AgentID != "" {
		q.Set("agent_id", opts.AgentID)
	}
	for _, s := range opts.Statuses {
		q.Add("statuses[]", s)
	}
	if opts.IncludeArchived {
		q.Set("include_archived", "true")
	}
	if opts.Limit > 0 {
		q.Set("limit", fmt.Sprintf("%d", opts.Limit))
	}
	if opts.Page != "" {
		q.Set("page", opts.Page)
	}
	if opts.Order != "" {
		q.Set("order", opts.Order)
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "", err
	}
	c.applyAuthHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("list sessions: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("read sessions body: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		return nil, "", fmt.Errorf("list sessions: status %d: %s", resp.StatusCode, truncate(string(body), 512))
	}
	var parsed listSessionsResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, "", fmt.Errorf("decode sessions: %w", err)
	}
	return parsed.Data, parsed.NextPage, nil
}

// ListEventsOptions controls a single GET /v1/sessions/{id}/events call.
type ListEventsOptions struct {
	Types        []string  // event types to filter, e.g. "session.status_idle"
	CreatedAfter time.Time // exclusive lower bound (created_at[gt])
	Limit        int       // 0 means no explicit limit
	Order        string    // "asc" or "desc"; empty falls back to API default
}

type listEventsResponse struct {
	Data []json.RawMessage `json:"data"`
}

// ListEvents returns events for a session, ordered as requested. The Raw
// field on each Event is populated with the original JSON so callers can
// access fields atryum has not yet typed.
func (c *Client) ListEvents(ctx context.Context, sessionID string, opts ListEventsOptions) ([]Event, error) {
	if strings.TrimSpace(sessionID) == "" {
		return nil, fmt.Errorf("claude agents: session id is required")
	}
	u, err := url.Parse(fmt.Sprintf("%s/v1/sessions/%s/events", c.baseURL, url.PathEscape(sessionID)))
	if err != nil {
		return nil, fmt.Errorf("build events url: %w", err)
	}
	q := u.Query()
	for _, t := range opts.Types {
		// Anthropic expects the array-style "types[]" form per the error
		// message returned when the bare "types" key is used.
		q.Add("types[]", t)
	}
	if !opts.CreatedAfter.IsZero() {
		q.Set("created_at[gt]", opts.CreatedAfter.UTC().Format(time.RFC3339Nano))
	}
	if opts.Limit > 0 {
		q.Set("limit", fmt.Sprintf("%d", opts.Limit))
	}
	if opts.Order != "" {
		q.Set("order", opts.Order)
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	c.applyAuthHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read events body: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("list events: status %d: %s", resp.StatusCode, truncate(string(body), 512))
	}
	var parsed listEventsResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("decode events: %w", err)
	}
	out := make([]Event, 0, len(parsed.Data))
	for _, raw := range parsed.Data {
		var e Event
		if err := json.Unmarshal(raw, &e); err != nil {
			return nil, fmt.Errorf("decode event: %w", err)
		}
		e.Raw = raw
		out = append(out, e)
	}
	return out, nil
}

// ConfirmationResult is "allow" or "deny".
type ConfirmationResult string

const (
	ResultAllow ConfirmationResult = "allow"
	ResultDeny  ConfirmationResult = "deny"
)

type toolConfirmationEvent struct {
	Type        string             `json:"type"`
	ToolUseID   string             `json:"tool_use_id"`
	Result      ConfirmationResult `json:"result"`
	DenyMessage string             `json:"deny_message,omitempty"`
}

// sendEventsRequest is the envelope Anthropic expects for POST
// /v1/sessions/{id}/events — a bare event object is silently accepted but
// never delivered to the agent loop, leaving the session idle forever.
type sendEventsRequest struct {
	Events []toolConfirmationEvent `json:"events"`
}

// SendToolConfirmation posts a `user.tool_confirmation` event to the session.
// denyMessage is ignored when result is "allow".
func (c *Client) SendToolConfirmation(ctx context.Context, sessionID, toolUseID string, result ConfirmationResult, denyMessage string) error {
	if strings.TrimSpace(sessionID) == "" {
		return fmt.Errorf("claude agents: session id is required")
	}
	if strings.TrimSpace(toolUseID) == "" {
		return fmt.Errorf("claude agents: tool_use_id is required")
	}
	if result != ResultAllow && result != ResultDeny {
		return fmt.Errorf("claude agents: result must be allow or deny, got %q", result)
	}

	evt := toolConfirmationEvent{
		Type:      "user.tool_confirmation",
		ToolUseID: toolUseID,
		Result:    result,
	}
	if result == ResultDeny {
		evt.DenyMessage = denyMessage
	}
	body, err := json.Marshal(sendEventsRequest{Events: []toolConfirmationEvent{evt}})
	if err != nil {
		return err
	}

	endpoint := fmt.Sprintf("%s/v1/sessions/%s/events", c.baseURL, url.PathEscape(sessionID))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	c.applyAuthHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("send tool confirmation: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("send tool confirmation: status %d: %s", resp.StatusCode, truncate(string(respBody), 512))
	}
	return nil
}

func (c *Client) applyAuthHeaders(req *http.Request) {
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("anthropic-version", AnthropicVersion)
	req.Header.Set("anthropic-beta", AnthropicBeta)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
