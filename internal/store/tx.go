package store

import (
	"context"
	"database/sql"
)

type txKey struct{}

// InTx runs fn in one transaction. Repository writes made with the ctx fn
// receives join it; fn also gets the tx for its own statements.
func InTx(ctx context.Context, db *sql.DB, fn func(ctx context.Context, tx *sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(context.WithValue(ctx, txKey{}, tx), tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// conn returns the transaction InTx put in ctx, else db.
func conn(ctx context.Context, db *sql.DB) execer {
	if tx, ok := ctx.Value(txKey{}).(*sql.Tx); ok {
		return tx
	}
	return db
}
