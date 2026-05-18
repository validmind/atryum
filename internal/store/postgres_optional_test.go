package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"atryum/internal/invocation"

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

// TestPostgresOptional_ClaudeAgentsBoolEncoding exercises the claude_agents
// table on Postgres specifically to lock in that the BOOLEAN `enabled` column
// works end-to-end (insert, update, scan, and the watcher's WHERE filter).
// This regressed once when the repo passed int 1/0 against a BOOLEAN column
// and pgx returned: "unable to encode 1 into binary format for bool".
func TestPostgresOptional_ClaudeAgentsBoolEncoding(t *testing.T) {
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

	schema := fmt.Sprintf("atryum_ca_test_%d", time.Now().UnixNano())
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

	repo := NewClaudeAgentsRepoWithDialect(db, DialectPostgres)
	ctx := context.Background()

	enabled := ClaudeAgent{ID: "cagent_1", Name: "PR Reviewer", AgentID: "agent_abc", Enabled: true}
	disabled := ClaudeAgent{ID: "cagent_2", Name: "Calendar", AgentID: "agent_xyz", Enabled: false}
	if err := repo.CreateAgent(ctx, enabled); err != nil {
		t.Fatalf("create enabled agent: %v", err)
	}
	if err := repo.CreateAgent(ctx, disabled); err != nil {
		t.Fatalf("create disabled agent: %v", err)
	}

	gotEnabled, err := repo.GetAgent(ctx, enabled.ID)
	if err != nil || !gotEnabled.Enabled {
		t.Fatalf("get enabled agent: enabled=%v err=%v", gotEnabled.Enabled, err)
	}
	gotDisabled, err := repo.GetAgent(ctx, disabled.ID)
	if err != nil || gotDisabled.Enabled {
		t.Fatalf("get disabled agent: enabled=%v err=%v", gotDisabled.Enabled, err)
	}

	if err := repo.CreateSession(ctx, ClaudeAgentSession{ID: "s1", AgentID: enabled.ID, SessionID: "sess_a"}); err != nil {
		t.Fatalf("create session for enabled: %v", err)
	}
	if err := repo.CreateSession(ctx, ClaudeAgentSession{ID: "s2", AgentID: disabled.ID, SessionID: "sess_b"}); err != nil {
		t.Fatalf("create session for disabled: %v", err)
	}

	watchable, err := repo.ListWatchableSessions(ctx)
	if err != nil {
		t.Fatalf("ListWatchableSessions: %v", err)
	}
	if len(watchable) != 1 || watchable[0].SessionID != "sess_a" {
		t.Fatalf("expected only sess_a watchable; got %+v", watchable)
	}

	// Toggle the enabled agent off and confirm the join filter excludes it.
	enabled.Enabled = false
	if err := repo.UpdateAgent(ctx, enabled); err != nil {
		t.Fatalf("update agent: %v", err)
	}
	watchable, err = repo.ListWatchableSessions(ctx)
	if err != nil {
		t.Fatalf("ListWatchableSessions after disable: %v", err)
	}
	if len(watchable) != 0 {
		t.Fatalf("expected no watchable sessions after disable; got %+v", watchable)
	}
}
