package invocation_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/validmind/atryum/internal/config"
	"github.com/validmind/atryum/internal/invocation"
	"github.com/validmind/atryum/internal/invocation/policy"
	"github.com/validmind/atryum/internal/mcp"
	"github.com/validmind/atryum/internal/store"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// Two service instances on one Postgres database stand in for two replicas:
// the one holding a call wakes on the other's decision through LISTEN/NOTIFY,
// long before its own (hour-long) poll would notice. The decision is made the
// way an embedder makes it: ResolvePending inside the embedder's own
// transaction, which the event hook joins and the embedder commits.
func TestPostgresHeldCallWakesOnAnotherReplica(t *testing.T) {
	upstream, calls := fakeMCP(t)
	dsn, replica := postgresReplicas(t, upstream.URL)
	holder, _ := replica()
	other, otherDB := replica()
	var hookTx *sql.Tx
	other.SetEventHook(func(_ context.Context, tx *sql.Tx, c invocation.Change) error {
		if c.Type == "invocation.approved" {
			hookTx = tx
		}
		return nil
	})
	holder.SetDecider(verdictDecider(invocation.VerdictPending))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	listener := store.ListenForDecisions(ctx, dsn)
	holder.SetDecisionWaker(listener)
	invocation.SetApprovalPoll(holder, time.Hour) // only a notification can wake it

	type result struct {
		resp invocation.InvocationResponse
		err  error
	}
	done := make(chan result, 1)
	go func() {
		resp, err := holder.Invoke(ctx, invocation.CreateInvocationRequest{Server: "bank", Tool: "pay", Input: map[string]any{}})
		done <- result{resp, err}
	}()

	// Approve from the other replica as soon as the call is pending and the
	// listener has had a moment to LISTEN.
	var id string
	for id == "" {
		select {
		case <-ctx.Done():
			t.Fatal("call never became pending")
		case <-time.After(50 * time.Millisecond):
		}
		list, err := other.List(ctx, invocation.InvocationListFilter{Limit: 1})
		if err == nil && len(list.Items) == 1 && list.Items[0].Status == invocation.StatusPendingApproval {
			id = list.Items[0].InvocationID
		}
	}
	time.Sleep(500 * time.Millisecond)
	tx, err := otherDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := other.ResolvePending(store.WithTx(ctx, tx), id, true, "manager@bank", "looks right"); err != nil {
		t.Fatal(err)
	}
	if hookTx != tx {
		t.Fatal("the event hook did not run in the embedder's transaction")
	}
	decided := time.Now()
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	r := <-done
	if r.err != nil {
		t.Fatal(r.err)
	}
	if r.resp.Status != invocation.StatusSucceeded || calls.Load() != 1 {
		t.Fatalf("status=%s calls=%d, want succeeded and 1", r.resp.Status, calls.Load())
	}
	if waited := time.Since(decided); waited > 3*time.Second {
		t.Fatalf("woke %s after the decision; want a notification, not the poll", waited)
	}
	if err := other.ResolvePending(ctx, id, false, "someone", ""); !errors.Is(err, invocation.ErrNotPending) {
		t.Fatalf("second resolve: %v, want ErrNotPending", err)
	}

	// An executor cancelling a pending submit-path call also announces it.
	other.SetDecider(verdictDecider(invocation.VerdictPending))
	sub, err := other.Submit(ctx, invocation.ExternalSubmitRequest{Tool: "pay", Source: "harness", Input: map[string]any{}})
	if err != nil || sub.Status != invocation.StatusPendingApproval {
		t.Fatalf("submit: %v %s", err, sub.Status)
	}
	woken, stop := listener.Wait(sub.InvocationID)
	defer stop()
	if _, err := other.RecordExecution(ctx, sub.InvocationID, invocation.ExternalExecutionUpdate{ExecutionStatus: "cancelled"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-woken:
	case <-time.After(3 * time.Second):
		t.Fatal("cancelling a pending call sent no notification")
	}

	// A REPEATABLE READ resolve that loses the race to another resolve reports
	// ErrNotPending (Postgres raises a serialization failure on the update).
	race, err := other.Submit(ctx, invocation.ExternalSubmitRequest{Tool: "pay", Source: "harness", Input: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	slow, err := otherDB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	defer slow.Rollback()
	var n int
	if err := slow.QueryRow(`SELECT COUNT(*) FROM invocations`).Scan(&n); err != nil { // take the snapshot
		t.Fatal(err)
	}
	if err := other.ResolvePending(ctx, race.InvocationID, false, "fast", "no"); err != nil {
		t.Fatal(err)
	}
	if err := other.ResolvePending(store.WithTx(ctx, slow), race.InvocationID, true, "slow", ""); !errors.Is(err, invocation.ErrNotPending) {
		t.Fatalf("losing REPEATABLE READ resolve: %v, want ErrNotPending", err)
	}
}

// postgresReplicas skips the test unless ATRYUM_POSTGRES_TESTS=1, creates a
// throwaway schema, and returns its DSN and a constructor for service
// instances ("replicas") that each open their own pool on it.
func postgresReplicas(t *testing.T, upstreamURL string) (string, func() (*invocation.Service, *sql.DB)) {
	t.Helper()
	if os.Getenv("ATRYUM_POSTGRES_TESTS") != "1" {
		t.Skip("set ATRYUM_POSTGRES_TESTS=1 to run against local PostgreSQL")
	}
	dsn := os.Getenv("ATRYUM_POSTGRES_TEST_DSN")
	if dsn == "" {
		dsn = "postgresql://postgres:password@127.0.0.1:5432/postgres"
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("atryum_test_%d", time.Now().UnixNano())
	if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		admin.Exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`)
		admin.Close()
	})
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	dsn += sep + "search_path=" + schema
	return dsn, func() (*invocation.Service, *sql.DB) {
		db, err := sql.Open("pgx", dsn)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		if err := store.InitDBWithDialect(db, store.DialectPostgres); err != nil {
			t.Fatal(err)
		}
		resolver := mcp.NewResolver(store.NewServerRepoWithDialect(db, store.DialectPostgres), config.Config{
			Upstreams: []config.UpstreamConfig{{Name: "bank", Mode: "http", BaseURL: upstreamURL, Enabled: true, TimeoutSeconds: 5}},
		})
		if err := resolver.BootstrapIfEmpty(context.Background()); err != nil {
			t.Fatal(err)
		}
		svc := invocation.NewService(store.NewInvocationRepoWithDialect(db, store.DialectPostgres), store.NewEventRepoWithDialect(db, store.DialectPostgres), resolver, mcp.NewHTTPClient(), policy.AlwaysApproveProvider{}, 5*time.Second, nil, nil, nil, nil)
		svc.SetTx(func(ctx context.Context, fn func(context.Context, *sql.Tx) error) error {
			return store.InTx(ctx, db, fn)
		})
		return svc, db
	}
}

// Concurrent identical submits by one agent on two replicas make one
// invocation: the losers of the insert race get the winner's.
func TestPostgresConcurrentIdenticalSubmits(t *testing.T) {
	upstream, _ := fakeMCP(t)
	_, replica := postgresReplicas(t, upstream.URL)
	a, _ := replica()
	b, _ := replica()
	checkConcurrentIdenticalSubmits(t, a, b)
}
