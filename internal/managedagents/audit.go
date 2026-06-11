package managedagents

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"atryum/internal/invocation"
)

// sessionAuditTool is the synthetic tool name used for the per-session audit
// invocation that collects every raw Anthropic event as invocation_events.
const sessionAuditTool = "_managed_agent_session"

// InvocationAuditStore is the minimal repository surface needed to create the
// synthetic per-session audit invocation and append raw session events to it.
type InvocationAuditStore interface {
	Create(ctx context.Context, inv invocation.Invocation) error
	GetByIdempotencyKey(ctx context.Context, key string) (invocation.Invocation, error)
	CreateEvent(ctx context.Context, evt invocation.Event) error
	ListEvents(ctx context.Context, invocationID string, filter invocation.EventListFilter) ([]invocation.Event, int, error)
}

func sessionAuditKey(sessionID string) string {
	return "managed-agent-session:" + sessionID
}

// ensureSessionAuditInvocation returns the invocation ID of the session's audit
// row, creating it on first use. It is idempotent across restarts via the
// session-scoped idempotency key.
func (s *Service) ensureSessionAuditInvocation(ctx context.Context, reg SessionRegistration, cfg Config) (string, error) {
	key := sessionAuditKey(reg.SessionID)
	if existing, err := s.audit.GetByIdempotencyKey(ctx, key); err == nil && existing.InvocationID != "" {
		return existing.InvocationID, nil
	}
	now := time.Now().UTC()
	id := "inv_" + uuid.NewString()
	inv := invocation.Invocation{
		InvocationID:   id,
		RequestID:      strPtr(reg.SessionID),
		IdempotencyKey: strPtr(key),
		Tool:           sessionAuditTool,
		Upstream:       "",
		Status:         invocation.StatusSucceeded,
		Input: mustJSON(map[string]any{
			"session_id":  reg.SessionID,
			"agent_id":    reg.AgentID,
			"description": reg.Description,
		}),
		SubmittedAt:   now,
		CompletedAt:   &now,
		ClientName:    strPtr(cfg.ClientName),
		ClientVersion: optStrPtr(cfg.ClientVersion),
		AgentID:       optStrPtr(reg.AgentID),
	}
	if err := s.audit.Create(ctx, inv); err != nil {
		// A concurrent watcher may have created it first; fall back to lookup.
		if existing, gerr := s.audit.GetByIdempotencyKey(ctx, key); gerr == nil && existing.InvocationID != "" {
			return existing.InvocationID, nil
		}
		return "", err
	}
	return id, nil
}

// appendSessionEvent records one raw Anthropic event on the session audit
// invocation. Every event the bridge sees is captured here regardless of type.
func (s *Service) appendSessionEvent(ctx context.Context, auditInvocationID, sessionID string, evt RawEvent) {
	_ = s.audit.CreateEvent(ctx, invocation.Event{
		InvocationID: auditInvocationID,
		EventType:    "managed_agents.session_event",
		Payload: mustJSON(map[string]any{
			"session_id":   sessionID,
			"event_id":     evt.ID,
			"event_type":   evt.Type,
			"processed_at": evt.ProcessedAt,
			"raw":          json.RawMessage(evt.Raw),
		}),
		CreatedAt: evt.ProcessedAt,
	})
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func strPtr(s string) *string { return &s }

func optStrPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
