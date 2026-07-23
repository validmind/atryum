package invocation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/validmind/atryum/internal/mcp"
)

// finishExecutionStreaming is finishExecution's live-relay path. It audits
// intermediate events and the durable upstream outcome; the API handler
// records terminal-frame delivery separately because that write happens only
// after this method returns.
func (s *Service) finishExecutionStreaming(ctx context.Context, inv Invocation, upstream mcp.Upstream, req CreateInvocationRequest, sink mcp.StreamSink) (InvocationResponse, error) {
	audited := newAuditingSink(sink, s.events, inv.InvocationID, req.RequestID, upstream.Name, s.streamAuditLimits)
	result, err := s.client.InvokeStream(ctx, upstream, req.Tool, req.Input, req.RequestID, req.Meta, audited, s.effectiveStreamOptions())
	completed := time.Now().UTC()
	inv.CompletedAt = &completed

	if err != nil {
		inv.Status = StatusFailed
		reason, message := classifyStreamError(ctx, audited, err)
		inv.Error = mustJSON(map[string]any{"message": message})
		persistCtx, cancelPersist := context.WithTimeout(context.WithoutCancel(ctx), terminalPersistenceTimeout)
		defer cancelPersist()
		if updateErr := s.invocations.UpdateResult(persistCtx, inv); updateErr != nil {
			audited.finish(completed, "persistence_failed")
			return s.toResponse(inv), fmt.Errorf("persist streaming invocation failure: %w", updateErr)
		}
		audited.finish(completed, "failed")
		// stream_completed (the summary of what happened during the relay)
		// is written before the invocation-level failed/succeeded event, so
		// an audit trail read chronologically sees "here's what the stream
		// did" before "here's how the invocation ended" — the natural
		// narrative order, even though both share the same timestamp.
		_ = s.events.Create(persistCtx, Event{
			InvocationID: inv.InvocationID, EventType: "invocation.failed",
			Payload:   mustJSON(map[string]any{"reason": reason, "message": message, "events_total": audited.seq}),
			CreatedAt: completed,
		})
		return s.toResponse(inv), nil
	}
	var terminalEvent Event
	if result.Failed {
		inv.Status = StatusFailed
		inv.Error = result.Body
		terminalEvent = Event{
			InvocationID: inv.InvocationID, EventType: "invocation.failed",
			Payload:   mustJSON(map[string]any{"request_id": req.RequestID, "input": json.RawMessage(inv.Input), "arguments": json.RawMessage(inv.Input), "body": json.RawMessage(result.Body)}),
			CreatedAt: completed,
		}
	} else {
		inv.Status = StatusSucceeded
		inv.Response = result.Body
		terminalEvent = Event{
			InvocationID: inv.InvocationID, EventType: "invocation.succeeded",
			Payload:   mustJSON(map[string]any{"request_id": req.RequestID, "input": json.RawMessage(inv.Input), "arguments": json.RawMessage(inv.Input), "body": json.RawMessage(result.Body)}),
			CreatedAt: completed,
		}
	}
	persistCtx, cancelPersist := context.WithTimeout(context.WithoutCancel(ctx), terminalPersistenceTimeout)
	defer cancelPersist()
	if err := s.invocations.UpdateResult(persistCtx, inv); err != nil {
		audited.finish(completed, "persistence_failed")
		return s.toResponse(inv), err
	}
	if result.Failed {
		audited.finish(completed, "failed")
	} else {
		audited.finish(completed, "succeeded")
	}
	_ = s.events.Create(persistCtx, terminalEvent)
	return s.toResponse(inv), nil
}

// classifyStreamError distinguishes why InvokeStream returned an error, in
// priority order: the sink itself (i.e. the downstream agent connection)
// failing first — that's the caller's own signal and always the most
// specific one available — then the client's own header/idle/max-duration
// bound (mcp.ErrStreamTimeout), then anything else as a generic transport
// failure. The reason is persisted on the invocation.failed audit event so
// it can be told apart from an ordinary transport error after the fact.
func classifyStreamError(ctx context.Context, audited *auditingSink, err error) (reason string, message string) {
	if audited.downstreamErr != nil {
		return "stream_aborted_downstream", audited.downstreamErr.Error()
	}
	if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		// The request context died with no failed downstream write to prove
		// who went away: a quietly-disconnected agent and a server shutdown
		// are indistinguishable from here, so the reason stays neutral
		// rather than blaming the downstream for every in-flight call when
		// the process stops.
		return "stream_canceled", err.Error()
	}
	if errors.Is(err, mcp.ErrStreamTimeout) {
		return "stream_timeout", err.Error()
	}
	if errors.Is(err, mcp.ErrStreamSessionRetryRefused) {
		return "stream_session_retry_refused", err.Error()
	}
	if errors.Is(err, mcp.ErrStreamMessageTooLarge) {
		return "stream_message_too_large", err.Error()
	}
	return "transport_error", err.Error()
}
