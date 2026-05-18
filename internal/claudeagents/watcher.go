package claudeagents

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"sync"
	"time"
)

// SessionRecord is what the watcher needs to know about a single registered
// session. It is decoupled from the store layer so tests can supply fakes.
type SessionRecord struct {
	ID              string // atryum-side session row id
	AgentID         string // atryum-side agent id
	AgentName       string // human-facing agent name (e.g. "PR Reviewer")
	SessionID       string // Claude-side session id
	LastEventCursor string // RFC3339 created_at of the last processed status_idle
}

// SessionStore is the slice of the agents repo the watcher needs. The
// "Ensure" methods make discovery upserts the watcher's responsibility while
// keeping the watcher decoupled from the store package.
type SessionStore interface {
	ListWatchableSessions(ctx context.Context) ([]SessionRecord, error)
	UpdateSessionCursor(ctx context.Context, id, cursor string) error
	// EnsureAgent creates a claude_agents row for the given Anthropic agent
	// when none already exists; existing rows are left untouched (so a human
	// can edit name/enabled without discovery clobbering changes).
	EnsureAgent(ctx context.Context, agentID, name, description string) error
	// EnsureSession creates a claude_agent_sessions row for the given
	// Anthropic session when none already exists. agentID is the
	// Anthropic-side agent id; the implementation is responsible for
	// resolving it to the local row id.
	EnsureSession(ctx context.Context, agentID, sessionID string) error
}

// ToolUseEvent is the per-tool-call summary the watcher emits to the handler
// after correlating a `session.status_idle` (requires_action) event with its
// referenced `agent.tool_use` / `agent.mcp_tool_use` event.
type ToolUseEvent struct {
	AgentID        string          // atryum agent id
	AgentName      string          // atryum agent name (used as the rule scope)
	SessionRowID   string          // atryum session row id
	SessionID      string          // Claude session id
	ToolUseEventID string          // Claude event id of the tool_use, used for idempotency + confirmation
	ToolName       string          // e.g. "read", or for MCP "github__create_issue"
	Input          json.RawMessage // tool input as posted by the agent
	IsMCP          bool            // true when sourced from an `agent.mcp_tool_use` event
}

// ToolUseHandler is invoked exactly once per blocking tool_use_event_id the
// watcher observes (modulo retries on transient handler errors). The handler
// is responsible for downstream idempotency and for posting the eventual
// confirmation back to Anthropic.
type ToolUseHandler func(ctx context.Context, evt ToolUseEvent) error

// Watcher polls registered sessions for tool-approval events and dispatches
// them to a ToolUseHandler. One Watcher serves all sessions; per-session
// goroutines are not used because polling is cheap and ordering across
// sessions does not matter.
type Watcher struct {
	client       *Client
	store        SessionStore
	handler      ToolUseHandler
	pollInterval time.Duration
	logger       *log.Logger

	mu sync.Mutex
}

// WatcherOptions configures a Watcher. PollInterval defaults to 5 seconds.
type WatcherOptions struct {
	Client       *Client
	Store        SessionStore
	Handler      ToolUseHandler
	PollInterval time.Duration
	Logger       *log.Logger
}

// NewWatcher constructs a Watcher. All fields except PollInterval and Logger
// are required.
func NewWatcher(opts WatcherOptions) (*Watcher, error) {
	if opts.Client == nil {
		return nil, errors.New("claude agents watcher: client is required")
	}
	if opts.Store == nil {
		return nil, errors.New("claude agents watcher: store is required")
	}
	if opts.Handler == nil {
		return nil, errors.New("claude agents watcher: handler is required")
	}
	interval := opts.PollInterval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	logger := opts.Logger
	if logger == nil {
		logger = log.Default()
	}
	return &Watcher{
		client:       opts.Client,
		store:        opts.Store,
		handler:      opts.Handler,
		pollInterval: interval,
		logger:       logger,
	}, nil
}

// Start runs the watcher loop until ctx is cancelled. It returns when the
// context is done.
func (w *Watcher) Start(ctx context.Context) {
	t := time.NewTicker(w.pollInterval)
	defer t.Stop()
	// Tick once immediately so a freshly registered session is picked up
	// without waiting for a full interval.
	w.tickOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.tickOnce(ctx)
		}
	}
}

// tickOnce performs a single sweep: discover Anthropic-side agents/sessions,
// then poll each registered session for new events. Errors on individual
// sessions are logged and otherwise swallowed so a single bad session does
// not stall the loop.
func (w *Watcher) tickOnce(ctx context.Context) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.discover(ctx)

	sessions, err := w.store.ListWatchableSessions(ctx)
	if err != nil {
		w.logger.Printf("claude agents watcher: list sessions: %v", err)
		return
	}
	for _, s := range sessions {
		if err := w.pollSession(ctx, s); err != nil {
			w.logger.Printf("claude agents watcher: session %s (%s): %v", s.SessionID, s.AgentName, err)
		}
	}
}

// discover lists Anthropic-side agents and their sessions, ensuring an
// atryum-side row exists for each. Existing rows are left untouched so a
// human can edit them without discovery clobbering changes. All errors are
// logged and swallowed; partial discovery is better than none.
func (w *Watcher) discover(ctx context.Context) {
	page := ""
	for {
		agents, next, err := w.client.ListAgents(ctx, ListAgentsOptions{Page: page, Limit: 100})
		if err != nil {
			w.logger.Printf("claude agents watcher: list agents: %v", err)
			return
		}
		for _, a := range agents {
			if a.ArchivedAt != "" {
				continue
			}
			if err := w.store.EnsureAgent(ctx, a.ID, a.Name, a.Description); err != nil {
				w.logger.Printf("claude agents watcher: ensure agent %s: %v", a.ID, err)
				continue
			}
			w.discoverSessionsFor(ctx, a.ID)
		}
		if next == "" {
			return
		}
		page = next
	}
}

// discoverSessionsFor walks GET /v1/sessions filtered by agent_id and ensures
// an atryum-side session row exists for each non-terminal session. Terminated
// sessions are skipped — they will not produce more requires_action events.
func (w *Watcher) discoverSessionsFor(ctx context.Context, agentID string) {
	page := ""
	for {
		sessions, next, err := w.client.ListSessions(ctx, ListSessionsOptions{
			AgentID:  agentID,
			Statuses: []string{"idle", "running", "rescheduling"},
			Limit:    100,
			Page:     page,
		})
		if err != nil {
			w.logger.Printf("claude agents watcher: list sessions for %s: %v", agentID, err)
			return
		}
		for _, s := range sessions {
			if err := w.store.EnsureSession(ctx, agentID, s.ID); err != nil {
				w.logger.Printf("claude agents watcher: ensure session %s: %v", s.ID, err)
			}
		}
		if next == "" {
			return
		}
		page = next
	}
}

// pollSession fetches new events for one session and dispatches any
// requires_action tool uses to the handler. It advances the persisted cursor
// to the latest fully-processed status_idle event.
func (w *Watcher) pollSession(ctx context.Context, s SessionRecord) error {
	after := time.Time{}
	if s.LastEventCursor != "" {
		if t, err := time.Parse(time.RFC3339Nano, s.LastEventCursor); err == nil {
			after = t
		}
	}

	events, err := w.client.ListEvents(ctx, s.SessionID, ListEventsOptions{
		Types: []string{
			"session.status_idle",
			"agent.tool_use",
			"agent.mcp_tool_use",
		},
		CreatedAfter: after,
		Order:        "asc",
	})
	if err != nil {
		return err
	}

	// Index tool_use events by their id so we can correlate them with the
	// status_idle events that reference them.
	toolUses := make(map[string]Event, len(events))
	for _, e := range events {
		switch e.Type {
		case "agent.tool_use", "agent.mcp_tool_use":
			toolUses[e.ID] = e
		}
	}

	var lastIdleCursor string
	for _, e := range events {
		if e.Type != "session.status_idle" {
			continue
		}
		if e.StopReason == nil || e.StopReason.Type != "requires_action" {
			// Track the cursor anyway so we don't keep refetching this idle.
			lastIdleCursor = e.CreatedAt.UTC().Format(time.RFC3339Nano)
			continue
		}

		allDispatched := true
		for _, toolUseID := range e.StopReason.EventIDs {
			tu, ok := toolUses[toolUseID]
			if !ok {
				// Tool use event not in this batch yet. Leave cursor where
				// it is so we re-poll and pick it up next tick.
				allDispatched = false
				continue
			}
			evt := ToolUseEvent{
				AgentID:        s.AgentID,
				AgentName:      s.AgentName,
				SessionRowID:   s.ID,
				SessionID:      s.SessionID,
				ToolUseEventID: tu.ID,
				ToolName:       tu.Name,
				Input:          tu.Input,
				IsMCP:          tu.Type == "agent.mcp_tool_use",
			}
			if err := w.handler(ctx, evt); err != nil {
				w.logger.Printf("claude agents watcher: handler %s: %v", tu.ID, err)
				allDispatched = false
			}
		}
		if allDispatched {
			lastIdleCursor = e.CreatedAt.UTC().Format(time.RFC3339Nano)
		} else {
			// Stop advancing past an idle whose blocking events couldn't all
			// be processed; we'll retry it next tick.
			break
		}
	}

	if lastIdleCursor != "" && lastIdleCursor != s.LastEventCursor {
		if err := w.store.UpdateSessionCursor(ctx, s.ID, lastIdleCursor); err != nil {
			return err
		}
	}
	return nil
}
