package store

import (
	"database/sql"
	"fmt"
)

// migrationRecord tracks applied migrations.
type migrationRecord struct {
	Version int
	Name    string
}

type migration struct {
	Version int
	Name    string
	Steps   []migrationStep
}

type migrationStep struct {
	Description string
	Build       func(dialect Dialect) (string, []any, error)
}

var migrations = []migration{
	{
		Version: 1,
		Name:    "001_init_schema.sql",
		Steps: []migrationStep{
			rawMigrationStep("create invocations table", `
				CREATE TABLE IF NOT EXISTS invocations (
					invocation_id TEXT PRIMARY KEY,
					request_id TEXT,
					idempotency_key TEXT,
					tool_name TEXT NOT NULL,
					upstream_name TEXT NOT NULL,
					status TEXT NOT NULL,
					approval_json TEXT,
					request_json TEXT NOT NULL,
					response_json TEXT,
					error_json TEXT,
					submitted_at TIMESTAMP NOT NULL,
					completed_at TIMESTAMP
				)
			`),
			rawMigrationStep("create invocations idempotency index", `
				CREATE UNIQUE INDEX IF NOT EXISTS idx_invocations_idempotency_key
					ON invocations(idempotency_key) WHERE idempotency_key IS NOT NULL
			`),
			rawMigrationStep("create invocations submitted_at index", `
				CREATE INDEX IF NOT EXISTS idx_invocations_submitted_at
					ON invocations(submitted_at DESC)
			`),
			rawMigrationStep("create invocations lookup index", `
				CREATE INDEX IF NOT EXISTS idx_invocations_upstream_tool_status
					ON invocations(upstream_name, tool_name, status)
			`),
			rawDialectMigrationStep("create invocation_events table", `
				CREATE TABLE IF NOT EXISTS invocation_events (
					id INTEGER PRIMARY KEY AUTOINCREMENT,
					invocation_id TEXT NOT NULL,
					event_type TEXT NOT NULL,
					payload_json TEXT NOT NULL,
					created_at TIMESTAMP NOT NULL,
					FOREIGN KEY(invocation_id) REFERENCES invocations(invocation_id)
				)
			`, `
				CREATE TABLE IF NOT EXISTS invocation_events (
					id BIGSERIAL PRIMARY KEY,
					invocation_id TEXT NOT NULL,
					event_type TEXT NOT NULL,
					payload_json TEXT NOT NULL,
					created_at TIMESTAMP NOT NULL,
					FOREIGN KEY(invocation_id) REFERENCES invocations(invocation_id)
				)
			`),
			rawMigrationStep("create invocation_events lookup index", `
				CREATE INDEX IF NOT EXISTS idx_invocation_events_lookup
					ON invocation_events(invocation_id, id)
			`),
			rawMigrationStep("create mcp_servers table", `
				CREATE TABLE IF NOT EXISTS mcp_servers (
					name TEXT PRIMARY KEY,
					mode TEXT NOT NULL,
					base_url TEXT,
					auth_token TEXT,
					timeout_seconds INTEGER NOT NULL DEFAULT 30,
					command TEXT,
					args_json TEXT NOT NULL DEFAULT '[]',
					env_json TEXT NOT NULL DEFAULT '{}',
					enabled INTEGER NOT NULL DEFAULT 1,
					created_at TIMESTAMP NOT NULL,
					updated_at TIMESTAMP NOT NULL
				)
			`),
			rawMigrationStep("create mcp_servers enabled index", `
				CREATE INDEX IF NOT EXISTS idx_mcp_servers_enabled_name
					ON mcp_servers(enabled, name)
			`),
		},
	},
	{
		Version: 2,
		Name:    "002_server_status_columns.sql",
		Steps: []migrationStep{
			rawMigrationStep("add auth_type", `ALTER TABLE mcp_servers ADD COLUMN auth_type TEXT NOT NULL DEFAULT 'none'`),
			rawMigrationStep("add connection_status", `ALTER TABLE mcp_servers ADD COLUMN connection_status TEXT NOT NULL DEFAULT 'unknown'`),
			rawMigrationStep("add auth_status", `ALTER TABLE mcp_servers ADD COLUMN auth_status TEXT NOT NULL DEFAULT 'unknown'`),
			rawMigrationStep("add reauth_needed", `ALTER TABLE mcp_servers ADD COLUMN reauth_needed INTEGER NOT NULL DEFAULT 0`),
			rawMigrationStep("add last_checked_at", `ALTER TABLE mcp_servers ADD COLUMN last_checked_at TIMESTAMP`),
			rawMigrationStep("add last_check_ok", `ALTER TABLE mcp_servers ADD COLUMN last_check_ok INTEGER NOT NULL DEFAULT 0`),
			rawMigrationStep("add last_error_summary", `ALTER TABLE mcp_servers ADD COLUMN last_error_summary TEXT`),
			rawMigrationStep("add action_required", `ALTER TABLE mcp_servers ADD COLUMN action_required TEXT`),
		},
	},
	{
		Version: 3,
		Name:    "003_oauth_tables.sql",
		Steps: []migrationStep{
			rawMigrationStep("add oauth_provider_id", `ALTER TABLE mcp_servers ADD COLUMN oauth_provider_id TEXT`),
			rawMigrationStep("add oauth_provider_label", `ALTER TABLE mcp_servers ADD COLUMN oauth_provider_label TEXT`),
			rawMigrationStep("add oauth_authorize_url", `ALTER TABLE mcp_servers ADD COLUMN oauth_authorize_url TEXT`),
			rawMigrationStep("add oauth_token_url", `ALTER TABLE mcp_servers ADD COLUMN oauth_token_url TEXT`),
			rawMigrationStep("add oauth_client_id", `ALTER TABLE mcp_servers ADD COLUMN oauth_client_id TEXT`),
			rawMigrationStep("add oauth_client_secret", `ALTER TABLE mcp_servers ADD COLUMN oauth_client_secret TEXT`),
			rawMigrationStep("add oauth_scopes", `ALTER TABLE mcp_servers ADD COLUMN oauth_scopes TEXT`),
			rawMigrationStep("create oauth_credentials table", `
				CREATE TABLE IF NOT EXISTS oauth_credentials (
					server_name TEXT PRIMARY KEY,
					access_token TEXT NOT NULL,
					refresh_token TEXT,
					token_type TEXT,
					scope TEXT,
					expires_at TIMESTAMP,
					created_at TIMESTAMP NOT NULL,
					updated_at TIMESTAMP NOT NULL,
					FOREIGN KEY(server_name) REFERENCES mcp_servers(name) ON DELETE CASCADE
				)
			`),
			rawMigrationStep("create oauth_connect_sessions table", `
				CREATE TABLE IF NOT EXISTS oauth_connect_sessions (
					state TEXT PRIMARY KEY,
					server_name TEXT NOT NULL,
					status TEXT NOT NULL,
					code_verifier TEXT,
					redirect_uri TEXT NOT NULL,
					started_at TIMESTAMP NOT NULL,
					completed_at TIMESTAMP,
					error_message TEXT,
					FOREIGN KEY(server_name) REFERENCES mcp_servers(name) ON DELETE CASCADE
				)
			`),
			rawMigrationStep("create oauth connect sessions index", `
				CREATE INDEX IF NOT EXISTS idx_oauth_connect_sessions_server
					ON oauth_connect_sessions(server_name, started_at DESC)
			`),
		},
	},
}

// InitDB runs all pending SQLite migrations in order. It creates a schema_migrations
// table to track which migrations have been applied.
func InitDB(db *sql.DB) error {
	return InitDBWithDialect(db, DialectSQLite)
}

func InitDBWithDialect(db *sql.DB, dialect Dialect) error {
	if dialect == DialectSQLite {
		if _, err := db.Exec(`PRAGMA foreign_keys = ON;`); err != nil {
			return fmt.Errorf("enable foreign keys: %w", err)
		}
	}

	if err := ensureMigrationsTable(db); err != nil {
		return fmt.Errorf("ensure migrations table: %w", err)
	}

	applied, err := getAppliedMigrations(db, dialect)
	if err != nil {
		return fmt.Errorf("get applied migrations: %w", err)
	}

	pending := getPendingMigrations(applied)

	for _, m := range pending {
		if err := applyMigration(db, m, dialect); err != nil {
			return fmt.Errorf("apply migration %s: %w", m.Name, err)
		}
	}

	return nil
}

func ensureMigrationsTable(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			name    TEXT NOT NULL,
			applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
	`)
	return err
}

func getAppliedMigrations(db *sql.DB, dialect Dialect) (map[int]bool, error) {
	query, args, err := statementBuilderForDialect(dialect).Select("version").From("schema_migrations").ToSql()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	applied := make(map[int]bool)
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		applied[v] = true
	}
	return applied, rows.Err()
}

func getPendingMigrations(applied map[int]bool) []migration {
	var pending []migration
	for _, m := range migrations {
		if applied[m.Version] {
			continue
		}
		pending = append(pending, m)
	}
	return pending
}

func applyMigration(db *sql.DB, m migration, dialect Dialect) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, step := range m.Steps {
		query, args, err := step.Build(dialect)
		if err != nil {
			return fmt.Errorf("build step %q: %w", step.Description, err)
		}
		if _, err := tx.Exec(query, args...); err != nil {
			return fmt.Errorf("exec step %q: %w", step.Description, err)
		}
	}

	recordSQL, args, err := statementBuilderForDialect(dialect).
		Insert("schema_migrations").
		Columns("version", "name").
		Values(m.Version, m.Name).
		ToSql()
	if err != nil {
		return fmt.Errorf("build migration record insert: %w", err)
	}
	if _, err := tx.Exec(recordSQL, args...); err != nil {
		return fmt.Errorf("record migration: %w", err)
	}

	return tx.Commit()
}

func rawMigrationStep(description, query string) migrationStep {
	return migrationStep{
		Description: description,
		Build: func(Dialect) (string, []any, error) {
			return query, nil, nil
		},
	}
}

func rawDialectMigrationStep(description, sqliteQuery, postgresQuery string) migrationStep {
	return migrationStep{
		Description: description,
		Build: func(dialect Dialect) (string, []any, error) {
			if dialect == DialectPostgres {
				return postgresQuery, nil, nil
			}
			return sqliteQuery, nil, nil
		},
	}
}
