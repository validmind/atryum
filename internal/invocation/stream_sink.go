package invocation

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/validmind/atryum/internal/mcp"
)

const (
	streamAuditQueueCapacity = 512
	streamAuditWorkerCount   = 8
	streamAuditWriteTimeout  = 500 * time.Millisecond
	streamAuditFlushTimeout  = 2 * time.Second
)

type streamAuditWrite struct {
	repo  eventRepo
	owner *auditingSink
	event Event
}

type streamAuditDispatcher struct {
	queues    []chan streamAuditWrite
	nextShard atomic.Uint64
}

func newStreamAuditDispatcher() *streamAuditDispatcher {
	d := &streamAuditDispatcher{queues: make([]chan streamAuditWrite, streamAuditWorkerCount)}
	// max(1, ...) guards a future non-multiple edit of the constants: an
	// unbuffered queue would make enqueue drop nearly every write.
	perWorkerCapacity := max(1, streamAuditQueueCapacity/streamAuditWorkerCount)
	for i := range d.queues {
		d.queues[i] = make(chan streamAuditWrite, perWorkerCapacity)
		go d.runWorker(d.queues[i])
	}
	return d
}

func (d *streamAuditDispatcher) assignShard() int {
	// Modulo in uint64 before converting: a wrapped counter converted to a
	// negative int would panic on the queue index.
	return int((d.nextShard.Add(1) - 1) % uint64(len(d.queues)))
}

func (d *streamAuditDispatcher) enqueue(shard int, write streamAuditWrite) bool {
	select {
	case d.queues[shard] <- write:
		return true
	default:
		return false
	}
}

func (d *streamAuditDispatcher) runWorker(queue <-chan streamAuditWrite) {
	for write := range queue {
		writeCtx, cancel := context.WithTimeout(context.Background(), streamAuditWriteTimeout)
		err := write.repo.Create(writeCtx, write.event)
		cancel()
		write.owner.completeAuditWrite(err)
	}
}

var sharedStreamAuditDispatcher = newStreamAuditDispatcher()

// auditingSink wraps a caller-supplied mcp.StreamSink so every relayed event
// and the call's outcome are recorded as invocation_events audit rows,
// correlating the downstream request (requestID), the upstream server
// (upstreamName), and this invocation (invocationID) — regardless of
// whether the downstream write later fails. Writes happen here, in the
// service layer, not the handler, precisely so they occur even if the
// handler's write to the agent fails mid-stream.
//
// Audit writes run through one process-wide bounded worker pool rather than
// creating a queue and goroutine for every invocation. A stalled repository
// therefore cannot create unbounded relay goroutines or delay live delivery.
type auditingSink struct {
	inner             mcp.StreamSink // may be nil: audit-only, no relay
	events            eventRepo
	invocationID      string
	requestID         *string
	upstreamName      string
	limits            StreamAuditLimits
	auditShard        int
	persisted         atomic.Int64
	failed            atomic.Int64
	dropped           atomic.Int64
	standaloneDropped atomic.Int64
	pendingMu         sync.Mutex
	pending           int
	closing           bool
	drained           chan struct{}
	drainOnce         sync.Once

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
		auditShard:   sharedStreamAuditDispatcher.assignShard(),
		drained:      make(chan struct{}),
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

func (a *auditingSink) StreamStats(stats mcp.StreamStats) {
	a.standaloneDropped.Add(stats.StandaloneEventsDropped)
}

func (a *auditingSink) recordEvent(evt mcp.StreamEvent) {
	if a.events == nil {
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
	a.pendingMu.Lock()
	a.pending++
	a.pendingMu.Unlock()
	if !sharedStreamAuditDispatcher.enqueue(a.auditShard, streamAuditWrite{repo: a.events, owner: a, event: record}) {
		a.dropped.Add(1)
		a.completePending()
	}
}

func (a *auditingSink) completeAuditWrite(err error) {
	if err != nil {
		a.failed.Add(1)
	} else {
		a.persisted.Add(1)
	}
	a.completePending()
}

func (a *auditingSink) completePending() {
	a.pendingMu.Lock()
	a.pending--
	drained := a.closing && a.pending == 0
	a.pendingMu.Unlock()
	if drained {
		a.drainOnce.Do(func() { close(a.drained) })
	}
}

func (a *auditingSink) waitForAuditWrites() bool {
	a.pendingMu.Lock()
	a.closing = true
	drained := a.pending == 0
	a.pendingMu.Unlock()
	if drained {
		a.drainOnce.Do(func() { close(a.drained) })
	}

	timer := time.NewTimer(streamAuditFlushTimeout)
	defer timer.Stop()
	select {
	case <-a.drained:
		return true
	case <-timer.C:
		return false
	}
}

// finish records the invocation.stream_completed totals row. terminal is
// "succeeded", "failed", or "persistence_failed".
//
// finish blocks its caller — and therefore the agent's terminal frame, which
// the handler writes only after InvokeStreaming returns — for up to
// streamAuditFlushTimeout + streamAuditWriteTimeout when the audit store is
// stalled. That is deliberate: the flush wait is what makes the persisted/
// failed/dropped totals truthful, and writing stream_completed synchronously
// here keeps it ordered before the invocation-level failed/succeeded event
// (see finishExecutionStreaming's narrative-order comment). With a healthy
// store the cost is a few milliseconds; with a stalled store the invocation
// is already paying UpdateResult's own 5s bound, so the added tail is
// accepted rather than trading away audit ordering.
func (a *auditingSink) finish(completed time.Time, terminal string) {
	if a.events == nil {
		return
	}
	auditFlushed := a.waitForAuditWrites()
	payload := map[string]any{
		"events_total":              a.seq,
		"events_persisted":          a.persisted.Load(),
		"audit_write_failures":      a.failed.Load(),
		"audit_queue_dropped":       a.dropped.Load(),
		"standalone_events_dropped": a.standaloneDropped.Load(),
		"audit_flush_timed_out":     !auditFlushed,
		"terminal":                  terminal,
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
