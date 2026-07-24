package mcp

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestCallTimeoutGuardIdleAndMaxDuration(t *testing.T) {
	idle := newCallTimeoutGuard(context.Background())
	defer idle.stop()
	idle.armBodyTimeouts(10*time.Millisecond, 0)
	<-idle.ctx.Done()
	if reason := idle.reason(); !strings.Contains(reason, "idle timeout") {
		t.Fatalf("expected idle timeout reason, got %q", reason)
	}

	max := newCallTimeoutGuard(context.Background())
	defer max.stop()
	max.armBodyTimeouts(0, 10*time.Millisecond)
	<-max.ctx.Done()
	if reason := max.reason(); !strings.Contains(reason, "max stream duration") {
		t.Fatalf("expected max duration reason, got %q", reason)
	}
}

func TestCallTimeoutGuardSetupTimeoutCanBeDisarmed(t *testing.T) {
	g := newCallTimeoutGuard(context.Background())
	defer g.stop()
	g.armSetupTimeout(10 * time.Millisecond)
	g.disarmSetupTimeout()
	time.Sleep(30 * time.Millisecond)
	if reason := g.reason(); reason != "" {
		t.Fatalf("expected a disarmed setup timeout not to fire, got %q", reason)
	}
}

// TestCallTimeoutGuardCheckIdleReschedulesOnRecentActivity is a
// deterministic regression test for the idle-timer reset race: time.Timer's
// docs explicitly warn that Reset racing with the timer's own firing is
// unsafe to reason about naively (the AfterFunc callback may already be
// running by the time Reset takes effect). checkIdle closes that race by
// re-deriving real elapsed time from lastActivity instead of trusting that
// "the timer fired" means "genuinely idle". This calls checkIdle directly
// with a lastActivity timestamp from a moment ago — simulating the timer
// firing at the exact instant resetIdle recorded fresh activity — and
// verifies it reschedules rather than tripping.
func TestCallTimeoutGuardCheckIdleReschedulesOnRecentActivity(t *testing.T) {
	g := newCallTimeoutGuard(context.Background())
	defer g.stop()
	g.idleTimeout = 100 * time.Millisecond
	g.lastActivity.Store(time.Now().UnixNano())
	g.idleTimer = time.NewTimer(time.Hour) // dummy target for checkIdle's Reset call

	g.checkIdle()

	if reason := g.reason(); reason != "" {
		t.Fatalf("expected checkIdle to reschedule (not trip) when real elapsed time is well under idleTimeout, got %q", reason)
	}
}

func TestCallTimeoutGuardCheckIdleTripsWhenElapsedExceedsTimeout(t *testing.T) {
	g := newCallTimeoutGuard(context.Background())
	defer g.stop()
	g.idleTimeout = 10 * time.Millisecond
	g.lastActivity.Store(time.Now().Add(-time.Hour).UnixNano())

	g.checkIdle()

	if reason := g.reason(); !strings.Contains(reason, "idle timeout") {
		t.Fatalf("expected checkIdle to trip when elapsed time genuinely exceeds idleTimeout, got %q", reason)
	}
}

// TestCallTimeoutGuardCheckIdleIsNoOpAfterStop is a regression test for the
// stop/checkIdle race: a checkIdle firing that loses the race with stop()
// must neither trip the guard nor re-arm the timer. Simulated directly by
// calling checkIdle after stop() with an ancient lastActivity — without
// the stopped check it would trip.
func TestCallTimeoutGuardCheckIdleIsNoOpAfterStop(t *testing.T) {
	g := newCallTimeoutGuard(context.Background())
	g.armBodyTimeouts(time.Hour, 0)
	g.lastActivity.Store(time.Now().Add(-2 * time.Hour).UnixNano())
	g.stop()

	g.checkIdle()

	if reason := g.reason(); reason != "" {
		t.Fatalf("expected checkIdle after stop to be a no-op, got %q", reason)
	}
}

// TestCallTimeoutGuardSurvivesContinuousResetIdlePressure is a stress test:
// hammering resetIdle from a tight loop must never spuriously trip the
// idle timer, even though the timer's own firing schedule and the reset
// calls are running on different goroutines with no shared lock between
// them (by design — resetIdle only writes an atomic timestamp).
func TestCallTimeoutGuardSurvivesContinuousResetIdlePressure(t *testing.T) {
	g := newCallTimeoutGuard(context.Background())
	defer g.stop()
	g.armBodyTimeouts(5*time.Millisecond, 0)

	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		g.resetIdle()
	}

	if reason := g.reason(); reason != "" {
		t.Fatalf("expected the idle timer never to trip while resetIdle is called continuously, got %q", reason)
	}
}
