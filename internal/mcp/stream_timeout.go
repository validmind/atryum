package mcp

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// callTimeoutGuard implements InvokeStream's setup/idle/max-duration timeout
// scheme by canceling one shared context. The setup timer covers HTTP response
// headers or the stdio initialize handshake. After setup, callers replace it
// with idle and maximum-duration timers for response reading.
type callTimeoutGuard struct {
	ctx    context.Context
	cancel context.CancelFunc

	// mu guards trippedWhy, stopped, the timer fields, and idleTimeout.
	// The timer fields need it because a time.AfterFunc callback starts
	// its clock before the assignment of the returned *Timer completes:
	// checkIdle (running on the timer's goroutine) could otherwise read
	// g.idleTimer before/while armBodyTimeouts writes it — a data race by
	// the memory model even if the window is nanoseconds in practice.
	mu          sync.Mutex
	trippedWhy  string
	stopped     bool
	setupTimer  *time.Timer
	idleTimer   *time.Timer
	maxTimer    *time.Timer
	idleTimeout time.Duration

	// lastActivity (unix nanoseconds) is updated by resetIdle and read by
	// checkIdle. It exists so the idle timer's firing can be verified
	// rather than trusted outright — see checkIdle. Atomic, not mu-guarded:
	// resetIdle runs once per relayed event on the hot path and must not
	// contend with the timer goroutine.
	lastActivity atomic.Int64
}

func newCallTimeoutGuard(parent context.Context) *callTimeoutGuard {
	ctx, cancel := context.WithCancel(parent)
	return &callTimeoutGuard{ctx: ctx, cancel: cancel}
}

func (g *callTimeoutGuard) trip(why string) {
	g.mu.Lock()
	if g.trippedWhy == "" {
		g.trippedWhy = why
	}
	g.mu.Unlock()
	g.cancel()
}

func (g *callTimeoutGuard) armSetupTimeout(d time.Duration) {
	if d <= 0 {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.stopped {
		return
	}
	g.setupTimer = time.AfterFunc(d, func() { g.trip("stream setup timeout exceeded") })
}

func (g *callTimeoutGuard) disarmSetupTimeout() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.setupTimer != nil {
		g.setupTimer.Stop()
	}
}

func (g *callTimeoutGuard) armBodyTimeouts(idle, max time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.stopped {
		return
	}
	g.idleTimeout = idle
	if idle > 0 {
		g.lastActivity.Store(time.Now().UnixNano())
		g.idleTimer = time.AfterFunc(idle, g.checkIdle)
	}
	if max > 0 {
		g.maxTimer = time.AfterFunc(max, func() { g.trip("max stream duration exceeded") })
	}
}

// checkIdle is the idle timer's callback. It does not trust "the timer
// fired" to mean "genuinely idle": time.Timer.Reset called concurrently
// with a timer's own firing is explicitly documented as racy (the AfterFunc
// callback may already be running by the time Reset takes effect), so
// resetIdle deliberately never calls Reset at all — it only records the
// latest activity timestamp. checkIdle re-derives the real elapsed time
// from that timestamp and either trips (elapsed genuinely exceeds the
// bound) or reschedules for the remaining time (an event arrived
// concurrently with this firing). This makes the idle bound correct
// regardless of how resetIdle and the timer callback interleave. The
// stopped check makes a firing that lost the race with stop() a no-op
// instead of re-arming a timer the guard's owner believes is dead.
func (g *callTimeoutGuard) checkIdle() {
	g.mu.Lock()
	if g.stopped {
		g.mu.Unlock()
		return
	}
	idleTimeout := g.idleTimeout
	elapsed := time.Duration(time.Now().UnixNano() - g.lastActivity.Load())
	if elapsed < idleTimeout {
		if g.idleTimer != nil {
			g.idleTimer.Reset(idleTimeout - elapsed)
		}
		g.mu.Unlock()
		return
	}
	g.mu.Unlock()
	// trip acquires g.mu itself; called outside the lock.
	g.trip("idle timeout waiting for the next stream event")
}

func (g *callTimeoutGuard) resetIdle() {
	g.lastActivity.Store(time.Now().UnixNano())
}

// stop is idempotent: the guard's owners defer it both at guard creation
// (covering early-error returns) and inside subprocess-cleanup defers that
// must cancel the context before waiting on the process.
func (g *callTimeoutGuard) stop() {
	g.mu.Lock()
	g.stopped = true
	if g.setupTimer != nil {
		g.setupTimer.Stop()
	}
	if g.idleTimer != nil {
		g.idleTimer.Stop()
	}
	if g.maxTimer != nil {
		g.maxTimer.Stop()
	}
	g.mu.Unlock()
	g.cancel()
}

func (g *callTimeoutGuard) reason() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.trippedWhy
}

// timeoutErr distinguishes "the guard's own bound fired" from "the transport
// failed on its own", a check every streaming read path needs after a read
// error: if the guard tripped, it wraps upstreamName and the trip reason
// (plus situation, a short phrase like "while resuming" — or "" for none) as
// ErrStreamTimeout; otherwise it returns fallback unchanged, letting the
// caller pass nil when it still has its own fallback logic to run.
func (g *callTimeoutGuard) timeoutErr(upstreamName, situation string, fallback error) error {
	reason := g.reason()
	if reason == "" {
		return fallback
	}
	if situation == "" {
		return fmt.Errorf("upstream %q: %s: %w", upstreamName, reason, ErrStreamTimeout)
	}
	return fmt.Errorf("upstream %q %s %s: %w", upstreamName, reason, situation, ErrStreamTimeout)
}
