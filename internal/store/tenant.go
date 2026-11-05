package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"

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

func (c *tenantConn) use(ctx context.Context) error {
	id := tenant.From(ctx)
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
