package store

import (
	storemigrations "atryum/internal/store/migrations"
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

var migrations = loadMigrations()

func loadMigrations() []migration {
	definitions := storemigrations.All()
	loaded := make([]migration, 0, len(definitions))
	for _, definition := range definitions {
		steps := make([]migrationStep, 0, len(definition.Steps))
		for _, step := range definition.Steps {
			build := step.Build
			steps = append(steps, migrationStep{
				Description: step.Description,
				Build: func(dialect Dialect) (string, []any, error) {
					return build(dialect == DialectPostgres)
				},
			})
		}
		loaded = append(loaded, migration{
			Version: definition.Version,
			Name:    definition.Name,
			Steps:   steps,
		})
	}
	return loaded
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
