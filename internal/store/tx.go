package store

import (
	"context"
	"database/sql"
)

type txKey struct{}

// InTx runs fn in one transaction. Repository writes made with the ctx fn
// receives join it; fn also gets the tx for its own statements. When ctx
// already carries a transaction (see WithTx), fn joins it and the caller
// commits or rolls back.
func InTx(ctx context.Context, db *sql.DB, fn func(ctx context.Context, tx *sql.Tx) error) error {
	return InTxOpts(ctx, db, nil, fn)
}

// InTxOpts is InTx, beginning a new transaction with opts (nil for the
// server's defaults). A transaction already in ctx is joined as it is.
func InTxOpts(ctx context.Context, db *sql.DB, opts *sql.TxOptions, fn func(ctx context.Context, tx *sql.Tx) error) error {
	if tx, ok := ctx.Value(txKey{}).(*sql.Tx); ok {
		return fn(ctx, tx)
	}
	tx, err := db.BeginTx(ctx, opts)
	if err != nil {
		return err
	}
	if err := fn(WithTx(ctx, tx), tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// WithTx returns ctx carrying tx, so repository reads and writes made with it,
// and InTx, use the caller's transaction.
func WithTx(ctx context.Context, tx *sql.Tx) context.Context {
	return context.WithValue(ctx, txKey{}, tx)
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// conn returns the transaction InTx put in ctx, else db.
func conn(ctx context.Context, db *sql.DB) execer {
	if tx, ok := ctx.Value(txKey{}).(*sql.Tx); ok {
		return tx
	}
	return db
}
