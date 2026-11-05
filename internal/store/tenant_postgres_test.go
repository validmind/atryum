package store

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/validmind/atryum/internal/mcp"
	"github.com/validmind/atryum/pkg/tenant"
)

// TestPostgresTenantIsolation migrates a fresh database, then connects as a
// role that doesn't own the tables (row-level security doesn't bind owners
// or superusers) and checks that two tenants never see each other's rows.
func TestPostgresTenantIsolation(t *testing.T) {
	if os.Getenv("ATRYUM_POSTGRES_TESTS") != "1" {
		t.Skip("set ATRYUM_POSTGRES_TESTS=1 to run against local PostgreSQL")
	}
	dsn := os.Getenv("ATRYUM_POSTGRES_TEST_DSN")
	if dsn == "" {
		dsn = localPostgresTestDSN
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()

	name := fmt.Sprintf("atryum_tenant_%d", time.Now().UnixNano())
	for _, q := range []string{
		`CREATE DATABASE ` + name,
		`CREATE ROLE ` + name + ` LOGIN PASSWORD 'app'`,
	} {
		if _, err := admin.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	t.Cleanup(func() {
		admin.Exec(`DROP DATABASE ` + name + ` WITH (FORCE)`)
		admin.Exec(`DROP ROLE ` + name)
	})

	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	owner, _, err := OpenDatabase(u.String(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	if err := InitDBWithDialect(owner, DialectPostgres); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, q := range []string{
		`GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO ` + name,
		`GRANT USAGE ON ALL SEQUENCES IN SCHEMA public TO ` + name,
	} {
		if _, err := owner.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}

	u.User = url.UserPassword(name, "app")
	db, _, err := OpenDatabase(u.String(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1) // one session, so each call has to switch tenants

	servers := NewServerRepoWithDialect(db, DialectPostgres)
	a := tenant.With(context.Background(), "bank-a")
	b := tenant.With(context.Background(), "bank-b")
	for ctx, base := range map[context.Context]string{a: "https://a.example/mcp", b: "https://b.example/mcp"} {
		if err := servers.CreateServer(ctx, mcp.Upstream{Name: "payments", Mode: mcp.UpstreamModeHTTP, BaseURL: base, Enabled: true}); err != nil {
			t.Fatalf("CreateServer %s: %v", base, err)
		}
	}

	got, err := servers.GetServer(b, "payments")
	if err != nil || got.BaseURL != "https://b.example/mcp" {
		t.Fatalf("bank-b's payments = %q, %v", got.BaseURL, err)
	}
	if err := servers.DeleteServer(a, "payments"); err != nil {
		t.Fatalf("DeleteServer: %v", err)
	}
	if _, err := servers.GetServer(a, "payments"); err == nil {
		t.Fatal("bank-a's payments survived its delete")
	}
	if _, err := servers.GetServer(b, "payments"); err != nil {
		t.Fatalf("bank-a's delete reached bank-b's payments: %v", err)
	}
	if n, err := servers.CountServers(context.Background()); err != nil || n != 0 {
		t.Fatalf("the default tenant sees %d servers, %v; want 0", n, err)
	}

	// A row can't be written into another tenant, even by naming it.
	_, err = db.ExecContext(a, `INSERT INTO mcp_servers (tenant_id, name, endpoint_slug, mode, created_at, updated_at) VALUES ('bank-b', 'sneaky', 'sneaky', 'http', now(), now())`)
	if err == nil || !strings.Contains(err.Error(), "row-level security") {
		t.Fatalf("bank-a inserting a row for bank-b: %v, want a row-level security error", err)
	}

	tx, err := db.BeginTx(a, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(b, `SELECT 1`); err == nil {
		t.Fatal("a transaction begun for bank-a ran a statement for bank-b")
	}
}
