package api

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/validmind/atryum/internal/mcp"
)

// splitSSELines splits payload data on every line terminator the SSE spec
// recognizes — CRLF, LF, or bare CR — so a raw CR never reaches the wire
// inside a data field, where a compliant parser treats it as a frame break.
// This is reachable, not theoretical: JSON legally allows CR as inter-token
// whitespace, so a stdio upstream can emit one inside an otherwise valid
// message.
func splitSSELines(data []byte) [][]byte {
	normalized := bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	normalized = bytes.ReplaceAll(normalized, []byte("\r"), []byte("\n"))
	return bytes.Split(normalized, []byte("\n"))
}

// writeSSEEvent writes and flushes one SSE frame. Each payload line needs its
// own data field; otherwise a compliant parser drops continuation lines. It
// deliberately omits event IDs because the downstream relay is not resumable.
func writeSSEEvent(w io.Writer, flusher http.Flusher, event string, data []byte) error {
	if event != "" {
		if _, err := fmt.Fprintf(w, "event: %s\n", event); err != nil {
			return err
		}
	}
	for _, line := range splitSSELines(data) {
		if _, err := fmt.Fprintf(w, "data: %s\n", line); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprint(w, "\n"); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

const (
	// sseRelayHeartbeatInterval paces `: ping` comment frames on an open
	// tools/call relay stream. Intermediary proxies and load balancers
	// commonly kill connections with no traffic for ~60s (e.g. the ALB
	// default); an upstream tool that is busy but silent would otherwise
	// have its downstream leg severed mid-call. Matches the cadence the
	// GET keepalive endpoint (handleMCPSSE) already uses.
	sseRelayHeartbeatInterval = 15 * time.Second
	// sseRelayWriteTimeout bounds each individual write to the agent. The
	// server deliberately sets no global WriteTimeout (it would kill every
	// long-lived stream), so without a per-write deadline an agent that
	// stops reading would block the handler goroutine in Write forever —
	// and, because the relay is synchronous, wedge the upstream read loop
	// with it. A deadline turns the stalled agent into a write error, which
	// aborts the relay as stream_aborted_downstream.
	sseRelayWriteTimeout = 30 * time.Second
)

// sseRelaySink turns one agent-facing tools/call response into a live SSE
// response. StreamStarted commits the headers; after that, finishStream must
// write the terminal response and stop the heartbeat before the handler exits.
// The mutex prevents heartbeat and event frames from interleaving.
type sseRelaySink struct {
	w       http.ResponseWriter
	flusher http.Flusher
	rc      *http.ResponseController

	// heartbeatInterval defaults to sseRelayHeartbeatInterval; a test seam.
	heartbeatInterval time.Duration

	mu sync.Mutex // serializes writes to w: events, heartbeats, terminal frame
	// writeErr is the first write failure, sticky. A heartbeat that fails
	// (agent gone) surfaces here so the next Event aborts the relay
	// promptly instead of waiting for its own write to fail.
	writeErr error

	started       bool
	eventCount    int
	heartbeatStop chan struct{}
	heartbeatDone chan struct{}
	stopOnce      sync.Once
}

func newSSERelaySink(w http.ResponseWriter, flusher http.Flusher) *sseRelaySink {
	return &sseRelaySink{
		w:                 w,
		flusher:           flusher,
		rc:                http.NewResponseController(w),
		heartbeatInterval: sseRelayHeartbeatInterval,
	}
}

func (s *sseRelaySink) StreamStarted() {
	s.started = true
	s.w.Header().Set("Content-Type", "text/event-stream")
	s.w.Header().Set("Cache-Control", "no-cache")
	s.w.Header().Set("Connection", "keep-alive")
	s.w.Header().Set("X-Accel-Buffering", "no")
	s.w.WriteHeader(http.StatusOK)
	s.flusher.Flush()

	s.heartbeatStop = make(chan struct{})
	s.heartbeatDone = make(chan struct{})
	go s.heartbeatLoop()
}

func (s *sseRelaySink) heartbeatLoop() {
	defer close(s.heartbeatDone)
	ticker := time.NewTicker(s.heartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.heartbeatStop:
			return
		case <-ticker.C:
			s.mu.Lock()
			if s.writeErr != nil {
				s.mu.Unlock()
				return
			}
			s.setWriteDeadlineLocked()
			if _, err := fmt.Fprint(s.w, ": ping\n\n"); err != nil {
				s.writeErr = err
				s.mu.Unlock()
				return
			}
			s.flusher.Flush()
			s.mu.Unlock()
		}
	}
}

// setWriteDeadlineLocked arms the per-write deadline, best-effort: not every
// ResponseWriter supports it (httptest recorders don't), and an unsupported
// deadline must not break the relay — it just loses the stalled-agent bound.
func (s *sseRelaySink) setWriteDeadlineLocked() {
	_ = s.rc.SetWriteDeadline(time.Now().Add(sseRelayWriteTimeout))
}

func (s *sseRelaySink) Event(evt mcp.StreamEvent) error {
	if evt.ServerRequest {
		// Atryum does not broker server-initiated requests (sampling,
		// elicitation, roots) — it doesn't advertise those capabilities in
		// initialize, and the agent has no channel to answer a request
		// arriving on what it expects to be a tools/call response stream.
		// Audited by the service-layer auditingSink already (server_request
		// flag on the invocation.stream_event row); never written to the
		// agent.
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		// A heartbeat already found the connection dead: abort the relay
		// now rather than waiting for this write to discover it again.
		return s.writeErr
	}
	s.setWriteDeadlineLocked()
	if err := writeSSEEvent(s.w, s.flusher, "", evt.Data); err != nil {
		s.writeErr = err
		return err
	}
	// Incremented only after a successful write: eventCount reports frames
	// delivered to the agent, not attempts.
	s.eventCount++
	return nil
}

// stopHeartbeat stops the heartbeat goroutine (if one was started) without
// writing anything, and waits for it to exit. finishStream calls it before
// the terminal frame; handlers additionally defer it right after building
// the sink so a panic between StreamStarted and finishStream cannot leave
// the goroutine writing to a ResponseWriter whose handler has returned.
// Idempotent and safe to call on a never-started sink.
func (s *sseRelaySink) stopHeartbeat() {
	if s.heartbeatStop == nil {
		return
	}
	s.stopOnce.Do(func() { close(s.heartbeatStop) })
	<-s.heartbeatDone
}

// finishStream stops the heartbeat and writes the terminal frame as the
// stream's final write. Must be called on every handler path once started
// is true — it is what guarantees the heartbeat goroutine cannot write to
// (or race on) the ResponseWriter after the handler returns.
func (s *sseRelaySink) finishStream(terminal []byte) error {
	s.stopHeartbeat()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return s.writeErr
	}
	s.setWriteDeadlineLocked()
	if err := writeSSEEvent(s.w, s.flusher, "", terminal); err != nil {
		s.writeErr = err
		return err
	}
	return nil
}
