package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/validmind/atryum/internal/invocation"
	"github.com/validmind/atryum/internal/mcp"
	"github.com/validmind/atryum/pkg/tenant"
)

// TestPostgresTenantIsolation migrates a fresh database, then connects as a
// role that doesn't own the tables (row-level security doesn't bind owners
// or superusers) and checks that two tenants never see each other's rows.
func TestPostgresTenantIsolation(t *testing.T) {
	db := openTenantTestDB(t)

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

// Every unique index leads with tenant_id, including one whose first key is
// an expression: two tenants' agents can reuse an idempotency key.
func TestPostgresTenantIdempotencyKeysPerTenant(t *testing.T) {
	db := openTenantTestDB(t)
	repo := NewInvocationRepoWithDialect(db, DialectPostgres)
	key := "retry-1"
	for i, id := range []string{"bank-a", "bank-b"} {
		inv := invocation.Invocation{InvocationID: fmt.Sprintf("inv-%d", i), IdempotencyKey: &key, Tool: "read_file", Upstream: "github", Status: "pending", Input: []byte(`{}`), SubmittedAt: time.Now().UTC()}
		if err := repo.Create(tenant.With(context.Background(), id), inv); err != nil {
			t.Fatalf("%s reusing idempotency key %q: %v", id, key, err)
		}
	}
}

// One application's end users may reuse an idempotency key: the key is
// unique per tenant, agent and end user, and redaction stays in its tenant.
func TestPostgresEndUserIdempotencyAndRedaction(t *testing.T) {
	db := openTenantTestDB(t)
	repo := NewInvocationRepoWithDialect(db, DialectPostgres)
	key := "turn-1"
	a := tenant.With(context.Background(), "bank-a")
	b := tenant.With(context.Background(), "bank-b")
	create := func(ctx context.Context, id, user string) error {
		t := "t-a"
		asserted := (&invocation.OnBehalfOf{EndUser: &invocation.EndUser{ID: user, Tenant: &t}}).Asserted("app-bank")
		inv := invocation.Invocation{InvocationID: id, IdempotencyKey: &key, AgentID: strPtr("app-bank"), Tool: "transfer", Status: "pending", Input: []byte(`{}`), SubmittedAt: time.Now().UTC(), Asserted: asserted}
		return repo.Create(ctx, inv)
	}
	if err := create(a, "inv-1", "u_1"); err != nil {
		t.Fatal(err)
	}
	if err := create(a, "inv-2", "u_2"); err != nil {
		t.Fatalf("another end user reusing the key: %v", err)
	}
	if err := create(b, "inv-3", "u_1"); err != nil {
		t.Fatalf("another tenant's end user reusing the key: %v", err)
	}
	if err := create(a, "inv-4", "u_1"); err == nil {
		t.Fatal("the same end user reused a key for a second invocation")
	}
	userKey := invocation.EndUserKey("app-bank", "t-a", true, "u_1")
	if n, err := RedactAssertedIdentity(a, db, DialectPostgres, "app-bank", userKey); err != nil || n != 1 {
		t.Fatalf("redacted %d, %v; want bank-a's one row", n, err)
	}
	got, err := repo.Get(b, "inv-3")
	if err != nil || got.Asserted == nil || got.Asserted.UserID != "u_1" {
		t.Fatalf("bank-a's redaction reached bank-b: %+v %v", got.Asserted, err)
	}
}

// A prepared statement runs for the tenant on its call's context, not the
// one its connection was last set to.
func TestPostgresTenantPreparedStatement(t *testing.T) {
	db := openTenantTestDB(t)
	a := tenant.With(context.Background(), "bank-a")
	if _, err := db.ExecContext(a, `SELECT 1`); err != nil { // the one session now acts for bank-a
		t.Fatal(err)
	}
	stmt, err := db.PrepareContext(a, `SELECT current_setting('app.tenant_id')`)
	if err != nil {
		t.Fatal(err)
	}
	defer stmt.Close()
	var got string
	if err := stmt.QueryRowContext(tenant.With(context.Background(), "bank-b")).Scan(&got); err != nil || got != "bank-b" {
		t.Fatalf("prepared statement for bank-b ran as %q, %v", got, err)
	}
}

func openTenantTestDB(t *testing.T) *sql.DB {
	t.Helper()
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
	t.Cleanup(func() { admin.Close() })

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
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1) // one session, so each call has to switch tenants
	return db
}

// With RequireTenant on, a statement whose context names no tenant fails
// instead of acting for the default tenant.
func TestPostgresRequireTenant(t *testing.T) {
	db := openTenantTestDB(t)
	RequireTenant()
	t.Cleanup(func() { requireTenant.Store(false) })
	servers := NewServerRepoWithDialect(db, DialectPostgres)
	if _, err := servers.CountServers(context.Background()); !errors.Is(err, ErrNoTenant) {
		t.Fatalf("no tenant: %v, want ErrNoTenant", err)
	}
	if _, err := servers.CountServers(tenant.With(context.Background(), tenant.Default)); err != nil {
		t.Fatalf("the default tenant, by name: %v", err)
	}
}

// Main refuses WithRequestTenant through a login row-level security doesn't
// bind: the test's admin (a superuser) is refused, the tenant login isn't.
func TestPostgresCheckRowSecurityBinds(t *testing.T) {
	app := openTenantTestDB(t)
	if err := CheckRowSecurityBinds(context.Background(), app); err != nil {
		t.Fatalf("the tenant login: %v, want accepted", err)
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
	if err := CheckRowSecurityBinds(context.Background(), admin); err == nil || !strings.Contains(err.Error(), "bypasses row-level security") {
		t.Fatalf("a superuser: %v, want refused", err)
	}

	// Logins that aren't superusers: a member of a role that owns a table,
	// and one with BYPASSRLS. Roles are cluster-wide, so they get a random
	// password and are dropped before the test's connection closes.
	var dbName string
	if err := app.QueryRow(`SELECT current_database()`).Scan(&dbName); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + dbName
	onDB, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer onDB.Close()
	owner, member, bypass, pw := dbName+"_owner", dbName+"_owner_member", dbName+"_bypass", rand.Text()
	defer func() {
		for _, q := range []string{`ALTER TABLE mcp_servers OWNER TO CURRENT_USER`, `DROP ROLE IF EXISTS ` + member + `, ` + bypass + `, ` + owner} {
			if _, err := onDB.Exec(q); err != nil {
				t.Errorf("%s: %v", q, err)
			}
		}
	}()
	for _, q := range []string{
		`CREATE ROLE ` + owner + ` NOLOGIN NOSUPERUSER`,
		`ALTER TABLE mcp_servers OWNER TO ` + owner,
		`CREATE ROLE ` + member + ` LOGIN PASSWORD '` + pw + `' IN ROLE ` + owner,
		`CREATE ROLE ` + bypass + ` LOGIN BYPASSRLS PASSWORD '` + pw + `'`,
	} {
		if _, err := onDB.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	for name, login := range map[string]string{"a member of a table's owner": member, "a BYPASSRLS login": bypass} {
		u.User = url.UserPassword(login, pw)
		db, err := sql.Open("pgx", u.String())
		if err != nil {
			t.Fatal(err)
		}
		err = CheckRowSecurityBinds(context.Background(), db)
		db.Close()
		if err == nil || !strings.Contains(err.Error(), "bypasses row-level security") {
			t.Errorf("%s: %v, want refused", name, err)
		}
	}
}
