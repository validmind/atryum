package store

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"sync"
	"sync/atomic"
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
//
// The connection must reach Postgres in session mode: PgBouncer (or another
// pooler) in transaction mode accepts LISTEN but never delivers
// notifications, which leaves held calls on the two-second fallback poll
// (server.database_listen = false turns the listener off).
type DecisionListener struct {
	connected atomic.Bool
	pid       atomic.Uint32 // backend PID of the listening connection

	// pingEvery bounds how long a dead connection goes unnoticed: after this
	// long without a notification the connection is pinged.
	pingEvery    time.Duration
	retryEvery   time.Duration
	wakeAllDelay time.Duration // spread of the re-check after a reconnect

	mu       sync.Mutex
	waiters  map[string]map[chan struct{}]struct{}
	lastWarn time.Time
}

// ListenForDecisions listens on dsn until ctx ends, reconnecting after errors.
func ListenForDecisions(ctx context.Context, dsn string) *DecisionListener {
	return listenForDecisions(ctx, dsn, 10*time.Second, 5*time.Second, time.Second)
}

func listenForDecisions(ctx context.Context, dsn string, pingEvery, retryEvery, wakeAllDelay time.Duration) *DecisionListener {
	l := &DecisionListener{
		pingEvery:    pingEvery,
		retryEvery:   retryEvery,
		wakeAllDelay: wakeAllDelay,
		waiters:      map[string]map[chan struct{}]struct{}{},
	}
	go l.run(ctx, dsn)
	return l
}

func (l *DecisionListener) run(ctx context.Context, dsn string) {
	for {
		err := l.listen(ctx, dsn)
		l.connected.Store(false)
		if ctx.Err() != nil {
			return
		}
		l.warn(err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(l.retryEvery):
		}
	}
}

// warn logs a listener failure at most once a minute.
func (l *DecisionListener) warn(err error) {
	l.mu.Lock()
	quiet := time.Since(l.lastWarn) < time.Minute
	if !quiet {
		l.lastWarn = time.Now()
	}
	l.mu.Unlock()
	if !quiet {
		slog.Warn("decision listener disconnected; held calls fall back to polling until it reconnects", "error", err)
	}
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
	l.pid.Store(conn.PgConn().PID())
	l.connected.Store(true)
	l.wakeAll() // re-check anything decided while we were not listening
	for {
		waitCtx, cancel := context.WithTimeout(ctx, l.pingEvery)
		n, err := conn.WaitForNotification(waitCtx)
		cancel()
		if err == nil {
			l.wake(n.Payload)
			continue
		}
		if ctx.Err() != nil || waitCtx.Err() == nil {
			return err
		}
		// Quiet for pingEvery: make sure the connection is still alive.
		pingCtx, cancel := context.WithTimeout(ctx, l.pingEvery/2)
		err = conn.Ping(pingCtx)
		cancel()
		if err != nil {
			return err
		}
	}
}

// Connected reports whether the listener is LISTENing right now.
func (l *DecisionListener) Connected() bool { return l.connected.Load() }

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

// wakeAll signals every waiter, each after a random delay up to
// wakeAllDelay, so a reconnect does not send every held call to the
// database at once.
func (l *DecisionListener) wakeAll() {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, chs := range l.waiters {
		for ch := range chs {
			time.AfterFunc(rand.N(l.wakeAllDelay+1), func() { signal(ch) })
		}
	}
}

func signal(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default: // already signalled
	}
}
