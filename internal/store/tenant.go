package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/validmind/atryum/pkg/tenant"
)

// OpenPostgres opens a pool whose connections act for the tenant on each
// call's context: before a statement or a BEGIN, a connection set to another
// tenant runs set_config('app.tenant_id', ...), and row-level security (see
// migration 034) shows and accepts only that tenant's rows.
//
// The setting is per session, not per transaction, so it is changed only
// outside a transaction, where a rollback can't undo it. It needs a session
// that stays one connection: a pooler in transaction mode (PgBouncer) would
// hand the setting to someone else.
func OpenPostgres(dsn string) (*sql.DB, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	return sql.OpenDB(tenantConnector{stdlib.GetConnector(*cfg)}), nil
}

type tenantConnector struct{ driver.Connector }

func (c tenantConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &tenantConn{pgxConn: conn.(*stdlib.Conn)}, nil
}

// pgxConn names the embedded connection so it doesn't hide its Conn method.
type pgxConn = stdlib.Conn

// tenantConn is a pgx connection that knows which tenant its session is set
// to. Embedding keeps every interface database/sql looks for (pinging,
// session resets, argument checks) and Conn() for LISTEN.
type tenantConn struct {
	*pgxConn
	tenant string // "" until the session is set
	inTx   bool
}

// requireTenant makes a context that names no tenant an error instead of
// acting for tenant.Default. It is per process, like the tenants a process
// serves: Main sets it, after migrating, when the embedding program resolves
// a tenant per request.
var requireTenant atomic.Bool

// RequireTenant turns requireTenant on.
func RequireTenant() { requireTenant.Store(true) }

// CheckRowSecurityBinds refuses a login that row-level security doesn't
// bind, or that can SET ROLE to one it doesn't: a superuser, a role with
// BYPASSRLS, or a table's owner, directly or through membership (NOINHERIT
// included). Through such a login every tenant reads every other's rows.
func CheckRowSecurityBinds(ctx context.Context, db *sql.DB) error {
	var who string
	var unbound bool
	err := db.QueryRowContext(ctx, `SELECT current_user,
		EXISTS (SELECT 1 FROM pg_roles r WHERE (r.rolsuper OR r.rolbypassrls) AND pg_has_role(current_user, r.oid, 'MEMBER'))
		OR EXISTS (SELECT 1 FROM pg_class c WHERE c.relrowsecurity AND pg_has_role(current_user, c.relowner, 'MEMBER'))`).Scan(&who, &unbound)
	if err != nil {
		return err
	}
	if unbound {
		return fmt.Errorf("login %q bypasses row-level security (a superuser, BYPASSRLS, or an owner of the tables), so tenants would read each other's rows: serve as a login that owns nothing", who)
	}
	return nil
}

// ErrNoTenant is returned for a statement whose context names no tenant once
// RequireTenant is on.
var ErrNoTenant = errors.New("store: the context names no tenant")

func (c *tenantConn) use(ctx context.Context) error {
	id, ok := tenant.Lookup(ctx)
	if !ok {
		if requireTenant.Load() {
			return ErrNoTenant
		}
		id = tenant.Default
	}
	if id == c.tenant {
		return nil
	}
	if c.inTx {
		return fmt.Errorf("store: a transaction for tenant %q can't run a statement for tenant %q", c.tenant, id)
	}
	if _, err := c.pgxConn.Conn().Exec(ctx, "SELECT set_config('app.tenant_id', $1, false)", id); err != nil {
		return fmt.Errorf("store: set tenant: %w", err)
	}
	c.tenant = id
	return nil
}

func (c *tenantConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if err := c.use(ctx); err != nil {
		return nil, err
	}
	return c.pgxConn.ExecContext(ctx, query, args)
}

func (c *tenantConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if err := c.use(ctx); err != nil {
		return nil, err
	}
	return c.pgxConn.QueryContext(ctx, query, args)
}

// PrepareContext wraps the statement so each run sets its own call's tenant:
// pgx's statement runs on the embedded connection and would skip use.
func (c *tenantConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	st, err := c.pgxConn.PrepareContext(ctx, query)
	if err != nil {
		return nil, err
	}
	return tenantStmt{st.(*stdlib.Stmt), c}, nil
}

type tenantStmt struct {
	*stdlib.Stmt
	c *tenantConn
}

func (s tenantStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	if err := s.c.use(ctx); err != nil {
		return nil, err
	}
	return s.Stmt.ExecContext(ctx, args)
}

func (s tenantStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	if err := s.c.use(ctx); err != nil {
		return nil, err
	}
	return s.Stmt.QueryContext(ctx, args)
}

func (c *tenantConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if err := c.use(ctx); err != nil {
		return nil, err
	}
	tx, err := c.pgxConn.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	c.inTx = true
	return tenantTx{tx, c}, nil
}

type tenantTx struct {
	driver.Tx
	c *tenantConn
}

func (t tenantTx) Commit() error   { t.c.inTx = false; return t.Tx.Commit() }
func (t tenantTx) Rollback() error { t.c.inTx = false; return t.Tx.Rollback() }
