package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/validmind/atryum/internal/invocation"

	_ "github.com/jackc/pgx/v5/stdlib"
)

const localPostgresTestDSN = "postgresql://postgres:password@127.0.0.1:5432/postgres"

func TestPostgresOptional_InitAndInvocationCRUD(t *testing.T) {
	if os.Getenv("ATRYUM_POSTGRES_TESTS") != "1" {
		t.Skip("set ATRYUM_POSTGRES_TESTS=1 to run against local PostgreSQL")
	}
	dsn := os.Getenv("ATRYUM_POSTGRES_TEST_DSN")
	if dsn == "" {
		dsn = localPostgresTestDSN
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()

	schema := fmt.Sprintf("atryum_test_%d", time.Now().UnixNano())
	if _, err := db.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	defer db.Exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`)
	if _, err := db.Exec(`SET search_path TO ` + schema); err != nil {
		t.Fatalf("set search_path: %v", err)
	}

	if err := InitDBWithDialect(db, DialectPostgres); err != nil {
		t.Fatalf("InitDBWithDialect: %v", err)
	}

	repo := NewInvocationRepoWithDialect(db, DialectPostgres)
	inv := invocation.Invocation{
		InvocationID: "pg-inv-1",
		Tool:         "read_file",
		Upstream:     "github",
		Status:       "pending",
		Input:        []byte(`{"path":"/tmp/test"}`),
		SubmittedAt:  time.Now().UTC(),
	}
	if err := repo.Create(context.Background(), inv); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := repo.Get(context.Background(), inv.InvocationID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.InvocationID != inv.InvocationID || got.Tool != inv.Tool || string(got.Input) != string(inv.Input) {
		t.Fatalf("unexpected invocation: %+v", got)
	}
}
