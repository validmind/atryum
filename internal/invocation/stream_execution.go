package invocation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/validmind/atryum/internal/mcp"
)

// finishExecutionStreaming is finishExecution's live-relay path: it wraps
// the caller's sink in an auditing decorator (so every relayed event and
// the call's outcome are recorded as invocation_events rows regardless of
// whether the downstream write later fails) and calls InvokeStream with
// s.streamOptions instead of the fixed s.defaultTimeout.
func (s *Service) finishExecutionStreaming(ctx context.Context, inv Invocation, upstream mcp.Upstream, req CreateInvocationRequest, sink mcp.StreamSink) (InvocationResponse, error) {
	audited := newAuditingSink(sink, s.events, inv.InvocationID, req.RequestID, upstream.Name, s.streamAuditLimits)
	result, err := s.client.InvokeStream(ctx, upstream, req.Tool, req.Input, req.RequestID, req.Meta, audited, s.streamOptions)
	completed := time.Now().UTC()
	inv.CompletedAt = &completed

	if err != nil {
		inv.Status = StatusFailed
		reason, message := classifyStreamError(audited, err)
		inv.Error = mustJSON(map[string]any{"message": message})
		audited.finish(completed, "failed")
		persistCtx, cancelPersist := context.WithTimeout(context.WithoutCancel(ctx), terminalPersistenceTimeout)
		defer cancelPersist()
		if updateErr := s.invocations.UpdateResult(persistCtx, inv); updateErr != nil {
			return InvocationResponse{}, fmt.Errorf("persist streaming invocation failure: %w", updateErr)
		}
		// stream_completed (the summary of what happened during the relay)
		// is written before the invocation-level failed/succeeded event, so
		// an audit trail read chronologically sees "here's what the stream
		// did" before "here's how the invocation ended" — the natural
		// narrative order, even though both share the same timestamp.
		_ = s.events.Create(persistCtx, Event{
			InvocationID: inv.InvocationID, EventType: "invocation.failed",
			Payload:   mustJSON(map[string]any{"reason": reason, "message": message, "events_relayed": audited.seq}),
			CreatedAt: completed,
		})
		return s.toResponse(inv), nil
	}
	var terminalEvent Event
	if result.Failed {
		inv.Status = StatusFailed
		inv.Error = result.Body
		audited.finish(completed, "failed")
		terminalEvent = Event{
			InvocationID: inv.InvocationID, EventType: "invocation.failed",
			Payload:   mustJSON(map[string]any{"request_id": req.RequestID, "input": json.RawMessage(inv.Input), "arguments": json.RawMessage(inv.Input), "body": json.RawMessage(result.Body)}),
			CreatedAt: completed,
		}
	} else {
		inv.Status = StatusSucceeded
		inv.Response = result.Body
		audited.finish(completed, "succeeded")
		terminalEvent = Event{
			InvocationID: inv.InvocationID, EventType: "invocation.succeeded",
			Payload:   mustJSON(map[string]any{"request_id": req.RequestID, "input": json.RawMessage(inv.Input), "arguments": json.RawMessage(inv.Input), "body": json.RawMessage(result.Body)}),
			CreatedAt: completed,
		}
	}
	persistCtx, cancelPersist := context.WithTimeout(context.WithoutCancel(ctx), terminalPersistenceTimeout)
	defer cancelPersist()
	if err := s.invocations.UpdateResult(persistCtx, inv); err != nil {
		return InvocationResponse{}, err
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
func classifyStreamError(audited *auditingSink, err error) (reason string, message string) {
	if audited.downstreamErr != nil {
		return "stream_aborted_downstream", audited.downstreamErr.Error()
	}
	if errors.Is(err, mcp.ErrStreamTimeout) {
		return "stream_timeout", err.Error()
	}
	if errors.Is(err, mcp.ErrStreamSessionRetryRefused) {
		return "stream_session_retry_refused", err.Error()
	}
	return "transport_error", err.Error()
}
