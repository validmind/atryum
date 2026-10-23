package store

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"
)

// The listener survives quiet periods (pinging, not reconnecting), notices a
// killed connection, reconnects, and re-wakes its waiters after reconnecting.
func TestDecisionListenerPingAndReconnect(t *testing.T) {
	if os.Getenv("ATRYUM_POSTGRES_TESTS") != "1" {
		t.Skip("set ATRYUM_POSTGRES_TESTS=1 to run against local PostgreSQL")
	}
	dsn := os.Getenv("ATRYUM_POSTGRES_TEST_DSN")
	if dsn == "" {
		dsn = localPostgresTestDSN
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	l := listenForDecisions(ctx, dsn, 100*time.Millisecond, 100*time.Millisecond, 200*time.Millisecond)

	eventually := func(what string, ok func() bool) {
		t.Helper()
		for deadline := time.Now().Add(5 * time.Second); !ok(); {
			if time.Now().After(deadline) {
				t.Fatal(what)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	got := func(ch <-chan struct{}, within time.Duration) bool {
		select {
		case <-ch:
			return true
		case <-time.After(within):
			return false
		}
	}
	eventually("never connected", l.Connected)
	pid := l.pid.Load()

	woken, stop := l.Wait("inv_listener_test")
	defer stop()
	time.Sleep(500 * time.Millisecond) // several ping cycles
	if !l.Connected() || l.pid.Load() != pid {
		t.Fatal("quiet connection was dropped instead of pinged")
	}
	if _, err := db.Exec(`SELECT pg_notify($1, $2)`, DecisionChannel, "inv_listener_test"); err != nil {
		t.Fatal(err)
	}
	if !got(woken, 2*time.Second) {
		t.Fatal("no wake after a quiet period")
	}

	if _, err := db.Exec(`SELECT pg_terminate_backend($1)`, int(pid)); err != nil {
		t.Fatal(err)
	}
	eventually("never reconnected", func() bool { return l.Connected() && l.pid.Load() != pid })
	if !got(woken, 2*time.Second) {
		t.Fatal("waiters not re-woken after reconnecting")
	}
}
