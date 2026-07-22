package invocation

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"atryum/internal/mcp"
)

const (
	streamAuditQueueCapacity = 128
	streamAuditWriteTimeout  = 500 * time.Millisecond
	streamAuditFlushTimeout  = 2 * time.Second
)

// auditingSink wraps a caller-supplied mcp.StreamSink so every relayed event
// and the call's outcome are recorded as invocation_events audit rows,
// correlating the downstream request (requestID), the upstream server
// (upstreamName), and this invocation (invocationID) — regardless of
// whether the downstream write later fails. Writes happen here, in the
// service layer, not the handler, precisely so they occur even if the
// handler's write to the agent fails mid-stream.
//
// Audit writes run through a bounded background queue rather than on the
// relay's hot path. A stalled audit repository must neither delay an event
// reaching the agent nor defeat the stream's idle/max-duration bounds. Each
// write and the final queue drain are time-bounded; failures and queue drops
// are summarized in invocation.stream_completed.
type auditingSink struct {
	inner        mcp.StreamSink // may be nil: audit-only, no relay
	events       eventRepo
	invocationID string
	requestID    *string
	upstreamName string
	limits       StreamAuditLimits
	auditCtx     context.Context
	cancelAudit  context.CancelFunc
	auditQueue   chan Event
	auditDone    chan struct{}
	closeOnce    sync.Once
	persisted    atomic.Int64
	failed       atomic.Int64
	dropped      atomic.Int64

	seq int
	// downstreamErr is set once inner.Event returns an error — the
	// downstream (agent) connection itself failed, as opposed to a
	// transport-level or timeout failure from the upstream client.
	downstreamErr error
}

func newAuditingSink(inner mcp.StreamSink, events eventRepo, invocationID string, requestID *string, upstreamName string, limits StreamAuditLimits) *auditingSink {
	a := &auditingSink{
		inner:        inner,
		events:       events,
		invocationID: invocationID,
		requestID:    requestID,
		upstreamName: upstreamName,
		limits:       limits,
	}
	if events != nil {
		a.auditCtx, a.cancelAudit = context.WithCancel(context.Background())
		a.auditQueue = make(chan Event, streamAuditQueueCapacity)
		a.auditDone = make(chan struct{})
		go a.runAuditWriter()
	}
	return a
}

func (a *auditingSink) StreamStarted() {
	if a.inner != nil {
		a.inner.StreamStarted()
	}
}

func (a *auditingSink) Event(evt mcp.StreamEvent) error {
	a.seq++
	a.recordEvent(evt)
	if a.inner == nil {
		return nil
	}
	if err := a.inner.Event(evt); err != nil {
		a.downstreamErr = err
		return err
	}
	return nil
}

func (a *auditingSink) recordEvent(evt mcp.StreamEvent) {
	if a.auditQueue == nil {
		return
	}
	if a.limits.MaxEvents > 0 && a.seq > a.limits.MaxEvents {
		return // over the cap: still counted via seq, just not persisted
	}
	data := evt.Data
	truncated := false
	if a.limits.MaxEventBytes > 0 && len(data) > a.limits.MaxEventBytes {
		data = data[:a.limits.MaxEventBytes]
		truncated = true
	}
	payload := map[string]any{
		"seq":                   a.seq,
		"upstream_name":         a.upstreamName,
		"downstream_request_id": a.requestID,
		"server_request":        evt.ServerRequest,
		"bytes":                 len(evt.Data),
		// data is a plain string, not embedded raw JSON: a truncated payload
		// is no longer valid JSON, and a string field survives that safely
		// (encoding/json escapes it) where json.RawMessage would corrupt
		// the whole audit row.
		"data":      string(data),
		"truncated": truncated,
	}
	record := Event{
		InvocationID: a.invocationID,
		EventType:    "invocation.stream_event",
		Payload:      mustJSON(payload),
		CreatedAt:    time.Now().UTC(),
	}
	select {
	case a.auditQueue <- record:
	default:
		a.dropped.Add(1)
	}
}

func (a *auditingSink) runAuditWriter() {
	defer close(a.auditDone)
	for evt := range a.auditQueue {
		writeCtx, cancel := context.WithTimeout(a.auditCtx, streamAuditWriteTimeout)
		err := a.events.Create(writeCtx, evt)
		cancel()
		if err != nil {
			a.failed.Add(1)
			continue
		}
		a.persisted.Add(1)
	}
}

func (a *auditingSink) stopAuditWriter() {
	if a.auditQueue == nil {
		return
	}
	a.closeOnce.Do(func() { close(a.auditQueue) })
	timer := time.NewTimer(streamAuditFlushTimeout)
	defer timer.Stop()
	select {
	case <-a.auditDone:
		a.cancelAudit()
	case <-timer.C:
		// Cancel the in-flight write and make every queued write fail fast.
		// Do not wait indefinitely if an eventRepo violates context
		// cancellation; terminal invocation persistence must remain bounded.
		a.cancelAudit()
		select {
		case <-a.auditDone:
		case <-time.After(streamAuditWriteTimeout):
		}
	}
}

// finish records the invocation.stream_completed totals row. terminal is
// "succeeded", "failed", or "aborted".
func (a *auditingSink) finish(completed time.Time, terminal string) {
	if a.events == nil {
		return
	}
	a.stopAuditWriter()
	payload := map[string]any{
		"events_total":         a.seq,
		"events_persisted":     a.persisted.Load(),
		"audit_write_failures": a.failed.Load(),
		"audit_queue_dropped":  a.dropped.Load(),
		"terminal":             terminal,
	}
	writeCtx, cancel := context.WithTimeout(context.Background(), streamAuditWriteTimeout)
	defer cancel()
	_ = a.events.Create(writeCtx, Event{
		InvocationID: a.invocationID,
		EventType:    "invocation.stream_completed",
		Payload:      mustJSON(payload),
		CreatedAt:    completed,
	})
}
