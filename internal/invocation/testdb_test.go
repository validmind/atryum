package invocation_test

import (
	"database/sql"
	"testing"

	"atryum/internal/store"
)

func newSQLiteTestDB(t *testing.T) *sql.DB {
	t.Helper()

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })

	if err := store.InitDB(db); err != nil {
		t.Fatal(err)
	}
	return db
}
