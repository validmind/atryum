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
	defer admin.Close()
	schema := fmt.Sprintf("atryum_wake_%d", time.Now().UnixNano())
	if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`)
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	dsn += sep + "search_path=" + schema

	upstream, calls := fakeMCP(t)
	replica := func() (*invocation.Service, *sql.DB) {
		db, err := sql.Open("pgx", dsn)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		if err := store.InitDBWithDialect(db, store.DialectPostgres); err != nil {
			t.Fatal(err)
		}
		resolver := mcp.NewResolver(store.NewServerRepoWithDialect(db, store.DialectPostgres), config.Config{
			Upstreams: []config.UpstreamConfig{{Name: "bank", Mode: "http", BaseURL: upstream.URL, Enabled: true, TimeoutSeconds: 5}},
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
	holder.SetDecisionWake(store.ListenForDecisions(ctx, dsn).Wait)
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
}
