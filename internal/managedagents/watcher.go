package managedagents

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"atryum/internal/invocation"
)

// pendingCall links an Anthropic tool-use event to the Atryum invocation it
// created, so a later requires_action / tool_result can find it.
type pendingCall struct {
	InvocationID string
	IsCustom     bool
	KindKnown    bool
}

const toolUseKindEvent = "managed_agents.tool_use"

type decisionResult struct {
	BlockingID string
	Delivered  bool
}

// watcher streams one session's events and drives the approval lifecycle.
type watcher struct {
	svc  *Service
	acct account // the Anthropic account (client + cfg) this session belongs to
	reg  SessionRegistration

	auditID     string
	lastEventID string

	mu            sync.Mutex
	pending       map[string]pendingCall // keyed by tool-use event ID
	toolUseCustom map[string]bool
	recentChat    []chatMessage
}

func (w *watcher) log() *slog.Logger {
	return slog.With("component", "managed_agents", "session_id", w.reg.SessionID)
}

func (w *watcher) recordChatMessage(msg chatMessage) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.recentChat = append(w.recentChat, msg)
	limit := w.acct.cfg.RecentChatMessagesLimit
	if len(w.recentChat) > limit {
		w.recentChat = append([]chatMessage(nil), w.recentChat[len(w.recentChat)-limit:]...)
	}
}

func (w *watcher) recentChatContext() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.recentChat) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Recent chat messages (oldest to newest, up to ")
	b.WriteString(strconv.Itoa(w.acct.cfg.RecentChatMessagesLimit))
	b.WriteString("):")
	for _, msg := range w.recentChat {
		b.WriteString("\n- ")
		b.WriteString(msg.Role)
		b.WriteString(": ")
		b.WriteString(msg.Text)
	}
	return b.String()
}

// run is the per-session supervisor loop: catch up on missed history, then
// follow the live stream, reconnecting with backoff until the context is done.
func (w *watcher) run(ctx context.Context) {
	w.log().Info("watcher started", "agent_id", w.reg.AgentID, "from_event_id", w.lastEventID)
	for {
		if ctx.Err() != nil {
			return
		}
		if err := w.catchUp(ctx); err != nil && ctx.Err() == nil {
			w.log().Warn("catch-up failed", "error", err)
		}
		if err := w.follow(ctx); err != nil && ctx.Err() == nil {
			w.log().Warn("stream ended; will reconnect", "error", err)
		}
		if ctx.Err() != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(w.acct.cfg.ReconnectBackoff):
		}
	}
}

// catchUp replays events that arrived while disconnected, using the persisted
// cursor. Tool-use creation is idempotent so replays are safe.
func (w *watcher) catchUp(ctx context.Context) error {
	events, err := w.acct.client.ListEventsSince(ctx, w.reg.SessionID, w.lastEventID)
	if err != nil {
		return err
	}
	for _, evt := range events {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		w.handleEvent(ctx, evt)
	}
	return nil
}

// follow consumes the live SSE stream until it errors or the context is done.
func (w *watcher) follow(ctx context.Context) error {
	stream, err := w.acct.client.StreamEvents(ctx, w.reg.SessionID)
	if err != nil {
		return err
	}
	// Closing the stream unblocks a Next() that is parked on a read when the
	// context is cancelled.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = stream.Close()
		case <-done:
		}
	}()
	defer stream.Close()
	for {
		evt, err := stream.Next(ctx)
		if err != nil {
			if errors.Is(err, io.EOF) || ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		w.handleEvent(ctx, evt)
	}
}

// handleEvent is the per-event state machine. Every event is audited; tool-use
// events become invocations, requires_action drives approvals, and tool_result
// records the outcome.
func (w *watcher) handleEvent(ctx context.Context, evt RawEvent) {
	if err := w.ensureAudit(ctx); err != nil {
		w.log().Warn("could not create session audit invocation", "error", err)
	} else {
		w.svc.appendSessionEvent(ctx, w.auditID, w.reg.SessionID, evt)
	}
	if msg, ok := parseChatMessage(evt); ok {
		w.recordChatMessage(msg)
	}

	switch {
	case isToolUse(evt.Type):
		if !w.handleToolUse(ctx, evt) {
			return
		}
	case evt.Type == evtSessionIdle:
		if ids, ok := requiresAction(evt); ok {
			if !w.handleRequiresAction(ctx, ids) {
				return
			}
		}
	default:
		if tr, ok := parseToolResult(evt); ok {
			w.handleToolResult(ctx, tr)
		}
	}

	if evt.ID != "" {
		w.advanceCursor(ctx, evt.ID)
	}
}

func (w *watcher) ensureAudit(ctx context.Context) error {
	if w.auditID != "" {
		return nil
	}
	id, err := w.svc.ensureSessionAuditInvocation(ctx, w.reg, w.acct.cfg)
	if err != nil {
		return err
	}
	w.auditID = id
	return nil
}

// handleToolUse submits the tool call to Atryum's approval engine. It does not
// execute the tool — that's Anthropic's job. The invocation is recorded with
// status from the rule engine (auto-approved/denied or pending_approval).
func (w *watcher) handleToolUse(ctx context.Context, evt RawEvent) bool {
	tu, ok := parseToolUse(evt)
	if !ok {
		return true
	}
	source := w.cfgSource(tu)
	eventID := tu.EventID
	resp, err := w.svc.inv.Submit(ctx, invocation.ExternalSubmitRequest{
		Source:         source,
		Tool:           tu.ToolName,
		Description:    "Claude managed agent " + tu.Kind + " in session " + w.reg.SessionID,
		Input:          tu.Input,
		ChatContext:    w.recentChatContext(),
		RequestID:      &eventID,
		IdempotencyKey: &eventID, // dedupe across stream reconnects/replays
		ThreadID:       w.reg.SessionID,
		ClientName:     w.acct.cfg.ClientName,
		ClientVersion:  w.acct.cfg.ClientVersion,
		AgentID:        w.reg.AgentID,
	})
	if err != nil {
		w.log().Warn("submit tool call failed", "tool", tu.ToolName, "error", err)
		return false
	}
	if err := w.recordToolUseKind(ctx, resp.InvocationID, tu); err != nil {
		w.log().Warn("persist tool-use kind failed", "tool_use_event_id", eventID, "invocation_id", resp.InvocationID, "error", err)
		return false
	}
	w.mu.Lock()
	w.pending[eventID] = pendingCall{InvocationID: resp.InvocationID, IsCustom: tu.IsCustom, KindKnown: true}
	if w.toolUseCustom == nil {
		w.toolUseCustom = make(map[string]bool)
	}
	w.toolUseCustom[eventID] = tu.IsCustom
	w.mu.Unlock()
	w.log().Info("tool call submitted", "tool", tu.ToolName, "invocation_id", resp.InvocationID, "status", resp.Status)
	return true
}

func (w *watcher) recordToolUseKind(ctx context.Context, invocationID string, tu toolUse) error {
	if w.svc.audit == nil {
		return nil
	}
	return w.svc.audit.CreateEvent(ctx, invocation.Event{
		InvocationID: invocationID,
		EventType:    toolUseKindEvent,
		Payload: mustJSON(map[string]any{
			"event_id":  tu.EventID,
			"kind":      tu.Kind,
			"is_custom": tu.IsCustom,
		}),
		CreatedAt: time.Now().UTC(),
	})
}

// handleRequiresAction resolves each blocking tool-use event: it waits for the
// Atryum decision and reports it back to Claude. Blocking IDs are handled
// concurrently so one slow human approval doesn't stall the others.
func (w *watcher) handleRequiresAction(ctx context.Context, blockingIDs []string) bool {
	var wg sync.WaitGroup
	allResolved := true
	results := make(chan decisionResult, len(blockingIDs))
	for _, id := range blockingIDs {
		pc, ok := w.pendingCall(ctx, id, true)
		if !ok {
			w.log().Warn("requires_action references unknown tool-use event; will retry on replay", "tool_use_event_id", id)
			allResolved = false
			continue
		}
		if !pc.KindKnown {
			w.log().Warn("requires_action references recovered tool-use event with unknown kind; will retry on replay", "tool_use_event_id", id)
			allResolved = false
			continue
		}
		wg.Add(1)
		go func(blockingID string, pc pendingCall) {
			defer wg.Done()
			results <- decisionResult{BlockingID: blockingID, Delivered: w.resolveDecision(ctx, blockingID, pc)}
		}(id, pc)
	}
	wg.Wait()
	close(results)
	for result := range results {
		if !result.Delivered {
			w.log().Warn("requires_action confirmation not delivered; will retry on replay", "tool_use_event_id", result.BlockingID)
			allResolved = false
		}
	}
	return allResolved
}

// resolveDecision polls the invocation until a terminal approval decision is
// reached, then sends the corresponding confirmation event to the session.
func (w *watcher) resolveDecision(ctx context.Context, blockingID string, pc pendingCall) bool {
	for {
		if ctx.Err() != nil {
			return false
		}
		resp, err := w.svc.inv.Get(ctx, pc.InvocationID)
		if err != nil {
			w.log().Warn("get invocation failed", "invocation_id", pc.InvocationID, "error", err)
			return false
		}
		switch resp.Status {
		case invocation.StatusApproved:
			return w.sendAllow(ctx, blockingID, pc)
		case invocation.StatusDenied:
			return w.sendDeny(ctx, blockingID, pc, denyReason(resp))
		case invocation.StatusExecuting, invocation.StatusSucceeded, invocation.StatusFailed, invocation.StatusCancelled:
			// Already advanced (e.g. replayed after we sent the confirmation).
			return true
		default: // received, pending_approval
			select {
			case <-ctx.Done():
				return false
			case <-time.After(w.acct.cfg.PollInterval):
			}
		}
	}
}

func (w *watcher) sendAllow(ctx context.Context, blockingID string, pc pendingCall) bool {
	evt := allowEvent(blockingID, pc)
	if err := w.acct.client.SendEvents(ctx, w.reg.SessionID, []OutboundEvent{evt}); err != nil {
		w.log().Warn("send allow failed", "tool_use_event_id", blockingID, "error", err)
		return false
	}
	// Mark the invocation as running now that Claude will execute it.
	if _, err := w.svc.inv.RecordExecution(ctx, pc.InvocationID, invocation.ExternalExecutionUpdate{ExecutionStatus: "running"}); err != nil {
		w.log().Debug("record running failed", "invocation_id", pc.InvocationID, "error", err)
	}
	w.log().Info("tool call approved -> allow sent", "invocation_id", pc.InvocationID)
	return true
}

func (w *watcher) sendDeny(ctx context.Context, blockingID string, pc pendingCall, reason string) bool {
	evt := denyEvent(blockingID, pc, reason)
	if err := w.acct.client.SendEvents(ctx, w.reg.SessionID, []OutboundEvent{evt}); err != nil {
		w.log().Warn("send deny failed", "tool_use_event_id", blockingID, "error", err)
		return false
	}
	w.log().Info("tool call denied -> deny sent", "invocation_id", pc.InvocationID, "reason", reason)
	return true
}

// handleToolResult records the executor outcome reported by Anthropic onto the
// matching invocation.
func (w *watcher) handleToolResult(ctx context.Context, tr toolResult) {
	pc, ok := w.pendingCall(ctx, tr.ToolUseID, false)
	if !ok {
		return
	}
	update := invocation.ExternalExecutionUpdate{}
	if tr.IsError {
		update.ExecutionStatus = "failed"
		if len(tr.Content) > 0 {
			update.Error = tr.Content
		}
	} else {
		update.ExecutionStatus = "completed"
		if len(tr.Content) > 0 {
			update.Result = tr.Content
		}
	}
	if _, err := w.svc.inv.RecordExecution(ctx, pc.InvocationID, update); err != nil {
		w.log().Debug("record execution outcome failed", "invocation_id", pc.InvocationID, "error", err)
	}
	w.mu.Lock()
	delete(w.pending, tr.ToolUseID)
	w.mu.Unlock()
}

func (w *watcher) pendingCall(ctx context.Context, toolUseEventID string, needKind bool) (pendingCall, bool) {
	w.mu.Lock()
	pc, ok := w.pending[toolUseEventID]
	w.mu.Unlock()
	if ok {
		return pc, true
	}
	if w.svc.audit == nil {
		return pendingCall{}, false
	}
	inv, err := w.svc.audit.GetByIdempotencyKey(ctx, toolUseEventID)
	if err != nil || inv.InvocationID == "" {
		return pendingCall{}, false
	}
	pc = pendingCall{
		InvocationID: inv.InvocationID,
	}
	if needKind {
		if isCustom, ok := w.customToolUse(ctx, toolUseEventID); ok {
			pc.IsCustom = isCustom
			pc.KindKnown = true
		}
	}
	// Only cache a fully-resolved entry. When the kind was needed but couldn't
	// be determined, leave it uncached so the next replay re-attempts recovery
	// instead of permanently skipping.
	if pc.KindKnown || !needKind {
		w.mu.Lock()
		w.pending[toolUseEventID] = pc
		w.mu.Unlock()
	}
	w.log().Info("recovered pending tool call", "tool_use_event_id", toolUseEventID, "invocation_id", pc.InvocationID)
	return pc, true
}

func (w *watcher) customToolUse(ctx context.Context, toolUseEventID string) (bool, bool) {
	if isCustom, ok := w.cachedCustomToolUse(toolUseEventID); ok {
		return isCustom, ok
	}
	inv, err := w.svc.audit.GetByIdempotencyKey(ctx, toolUseEventID)
	if err != nil {
		return false, false
	}
	events, _, err := w.svc.audit.ListEvents(ctx, inv.InvocationID, invocation.EventListFilter{Limit: 200})
	if err != nil {
		w.log().Debug("could not recover persisted tool-use kind", "tool_use_event_id", toolUseEventID, "invocation_id", inv.InvocationID, "error", err)
		return false, false
	}
	isCustom, ok := findPersistedToolUseKind(events, toolUseEventID)
	w.mu.Lock()
	if w.toolUseCustom == nil {
		w.toolUseCustom = make(map[string]bool)
	}
	if ok {
		w.toolUseCustom[toolUseEventID] = isCustom
	}
	w.mu.Unlock()
	return isCustom, ok
}

func findPersistedToolUseKind(events []invocation.Event, toolUseEventID string) (bool, bool) {
	for _, evt := range events {
		if evt.EventType != toolUseKindEvent {
			continue
		}
		var payload struct {
			EventID  string `json:"event_id"`
			IsCustom bool   `json:"is_custom"`
		}
		if err := json.Unmarshal(evt.Payload, &payload); err != nil {
			continue
		}
		if payload.EventID == toolUseEventID {
			return payload.IsCustom, true
		}
	}
	return false, false
}

func (w *watcher) cachedCustomToolUse(toolUseEventID string) (isCustom bool, ok bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.toolUseCustom == nil {
		return false, false
	}
	isCustom, ok = w.toolUseCustom[toolUseEventID]
	return isCustom, ok
}

func (w *watcher) advanceCursor(ctx context.Context, eventID string) {
	w.lastEventID = eventID
	if err := w.svc.sessions.UpdateCursor(ctx, w.reg.SessionID, eventID); err != nil {
		w.log().Debug("update cursor failed", "event_id", eventID, "error", err)
	}
}

// cfgSource picks the approval-rule "server" for a tool call. MCP tool calls
// match on their server name when available; everything else uses a stable
// source so rules remain reusable across sessions.
func (w *watcher) cfgSource(tu toolUse) string {
	if tu.Kind == evtAgentMCPToolUse && tu.ServerName != "" {
		return tu.ServerName
	}
	return w.acct.cfg.ClientName
}

func isToolUse(eventType string) bool {
	switch eventType {
	case evtAgentToolUse, evtAgentMCPToolUse, evtAgentCustomToolUse:
		return true
	default:
		return false
	}
}

func denyReason(resp invocation.InvocationResponse) string {
	if resp.Approval != nil && resp.Approval.Reason != nil && *resp.Approval.Reason != "" {
		return *resp.Approval.Reason
	}
	return "denied by Atryum policy"
}

// allowEvent builds the outbound confirmation for an approved call. Hosted/MCP
// tools use user.tool_confirmation; custom tools use user.custom_tool_result
// with an Atryum decision envelope (see README for the v1 contract).
func allowEvent(blockingID string, pc pendingCall) OutboundEvent {
	if pc.IsCustom {
		return OutboundEvent{
			Type: "user.custom_tool_result",
			Fields: map[string]any{
				"custom_tool_use_id": blockingID,
				"content":            []any{atryumDecisionContent("allow", pc.InvocationID, "")},
			},
		}
	}
	return OutboundEvent{
		Type: "user.tool_confirmation",
		Fields: map[string]any{
			"tool_use_id": blockingID,
			"result":      "allow",
		},
	}
}

func denyEvent(blockingID string, pc pendingCall, reason string) OutboundEvent {
	if pc.IsCustom {
		return OutboundEvent{
			Type: "user.custom_tool_result",
			Fields: map[string]any{
				"custom_tool_use_id": blockingID,
				"content":            []any{atryumDecisionContent("deny", pc.InvocationID, reason)},
			},
		}
	}
	return OutboundEvent{
		Type: "user.tool_confirmation",
		Fields: map[string]any{
			"tool_use_id":  blockingID,
			"result":       "deny",
			"deny_message": reason,
		},
	}
}

// atryumDecisionContent is the v1 envelope sent as a custom_tool_result content
// block when a custom tool is gated. Custom tools have no native allow/deny, so
// Atryum surfaces the decision as a structured text block the agent can read.
func atryumDecisionContent(decision, invocationID, denyMessage string) map[string]any {
	envelope := map[string]any{"decision": decision, "invocation_id": invocationID}
	if denyMessage != "" {
		envelope["deny_message"] = denyMessage
	}
	payload, _ := json.Marshal(map[string]any{"atryum": envelope})
	return map[string]any{"type": "text", "text": string(payload)}
}
