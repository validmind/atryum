package store

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

// DecisionChannel is the Postgres NOTIFY channel InvocationRepo signals, with
// the invocation ID as payload, when a pending_approval call is decided.
const DecisionChannel = "atryum_invocation_decided"

// DecisionListener wakes calls held on this process when any process records
// their decision, over Postgres LISTEN on one dedicated connection. Held
// calls keep polling their row as the fallback, so a dropped connection or a
// notification sent while reconnecting only delays a wake-up.
type DecisionListener struct {
	mu      sync.Mutex
	waiters map[string]map[chan struct{}]struct{}
}

// ListenForDecisions listens on dsn until ctx ends, reconnecting after errors.
func ListenForDecisions(ctx context.Context, dsn string) *DecisionListener {
	l := &DecisionListener{waiters: map[string]map[chan struct{}]struct{}{}}
	go func() {
		for {
			err := l.listen(ctx, dsn)
			if ctx.Err() != nil {
				return
			}
			slog.Warn("decision listener disconnected; held calls fall back to polling", "error", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
		}
	}()
	return l
}

func (l *DecisionListener) listen(ctx context.Context, dsn string) error {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return err
	}
	defer conn.Close(context.WithoutCancel(ctx))
	if _, err := conn.Exec(ctx, "LISTEN "+DecisionChannel); err != nil {
		return err
	}
	l.wakeAll() // re-check anything decided while we were not listening
	for {
		n, err := conn.WaitForNotification(ctx)
		if err != nil {
			return err
		}
		l.wake(n.Payload)
	}
}

// Wait returns a channel that receives when invocationID may have been
// decided, and a func that stops waiting.
func (l *DecisionListener) Wait(invocationID string) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	l.mu.Lock()
	if l.waiters[invocationID] == nil {
		l.waiters[invocationID] = map[chan struct{}]struct{}{}
	}
	l.waiters[invocationID][ch] = struct{}{}
	l.mu.Unlock()
	return ch, func() {
		l.mu.Lock()
		delete(l.waiters[invocationID], ch)
		if len(l.waiters[invocationID]) == 0 {
			delete(l.waiters, invocationID)
		}
		l.mu.Unlock()
	}
}

func (l *DecisionListener) wake(invocationID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for ch := range l.waiters[invocationID] {
		signal(ch)
	}
}

func (l *DecisionListener) wakeAll() {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, chs := range l.waiters {
		for ch := range chs {
			signal(ch)
		}
	}
}

func signal(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default: // already signalled
	}
}
