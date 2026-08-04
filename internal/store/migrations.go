package store

import (
	"database/sql"
	"fmt"

	storemigrations "github.com/validmind/atryum/pkg/migrations"
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
	Run         func(tx *sql.Tx, dialect Dialect) error
}

var migrations = convertDefinitions(storemigrations.All())

func convertDefinitions(definitions []storemigrations.Definition) []migration {
	loaded := make([]migration, 0, len(definitions))
	for _, definition := range definitions {
		steps := make([]migrationStep, 0, len(definition.Steps))
		for _, step := range definition.Steps {
			build := step.Build
			run := step.Run
			loadedStep := migrationStep{
				Description: step.Description,
			}
			if build != nil {
				loadedStep.Build = func(dialect Dialect) (string, []any, error) {
					return build(dialect == DialectPostgres)
				}
			}
			if run != nil {
				loadedStep.Run = func(tx *sql.Tx, dialect Dialect) error {
					return run(tx, dialect == DialectPostgres)
				}
			}
			steps = append(steps, loadedStep)
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
	return applyMigrationRecorded(db, m, dialect, func(tx *sql.Tx) error {
		recordSQL, args, err := statementBuilderForDialect(dialect).
			Insert("schema_migrations").
			Columns("version", "name").
			Values(m.Version, m.Name).
			ToSql()
		if err != nil {
			return fmt.Errorf("build migration record insert: %w", err)
		}
		_, err = tx.Exec(recordSQL, args...)
		return err
	})
}

func applyMigrationRecorded(db *sql.DB, m migration, dialect Dialect, record func(tx *sql.Tx) error) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, step := range m.Steps {
		if step.Run != nil {
			if err := step.Run(tx, dialect); err != nil {
				return fmt.Errorf("run step %q: %w", step.Description, err)
			}
			continue
		}
		query, args, err := step.Build(dialect)
		if err != nil {
			return fmt.Errorf("build step %q: %w", step.Description, err)
		}
		if _, err := tx.Exec(query, args...); err != nil {
			return fmt.Errorf("exec step %q: %w", step.Description, err)
		}
	}

	if err := record(tx); err != nil {
		return fmt.Errorf("record migration: %w", err)
	}

	return tx.Commit()
}

// RunExtensionMigrationsWithDialect applies an embedding program's migrations
// (see pkg/atryum WithMigrations). It always runs after InitDBWithDialect, so
// the built-in schema is fully migrated first. Extension migrations are
// tracked in extension_schema_migrations keyed by (namespace, version) — a
// sequence separate from the built-in schema_migrations one, so upstream
// adding migrations can never collide with an extension's version numbers.
func RunExtensionMigrationsWithDialect(db *sql.DB, dialect Dialect, namespace string, definitions []storemigrations.Definition) error {
	if namespace == "" {
		return fmt.Errorf("extension migrations require a non-empty namespace")
	}

	if err := ensureExtensionMigrationsTable(db); err != nil {
		return fmt.Errorf("ensure extension migrations table: %w", err)
	}

	applied, err := getAppliedExtensionMigrations(db, dialect, namespace)
	if err != nil {
		return fmt.Errorf("get applied extension migrations: %w", err)
	}

	for _, m := range convertDefinitions(definitions) {
		if applied[m.Version] {
			continue
		}
		record := func(tx *sql.Tx) error {
			recordSQL, args, err := statementBuilderForDialect(dialect).
				Insert("extension_schema_migrations").
				Columns("namespace", "version", "name").
				Values(namespace, m.Version, m.Name).
				ToSql()
			if err != nil {
				return fmt.Errorf("build migration record insert: %w", err)
			}
			_, err = tx.Exec(recordSQL, args...)
			return err
		}
		if err := applyMigrationRecorded(db, m, dialect, record); err != nil {
			return fmt.Errorf("apply extension migration %s/%s: %w", namespace, m.Name, err)
		}
	}

	return nil
}

func ensureExtensionMigrationsTable(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS extension_schema_migrations (
			namespace  TEXT NOT NULL,
			version    INTEGER NOT NULL,
			name       TEXT NOT NULL,
			applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (namespace, version)
		)
	`)
	return err
}

func getAppliedExtensionMigrations(db *sql.DB, dialect Dialect, namespace string) (map[int]bool, error) {
	query, args, err := statementBuilderForDialect(dialect).
		Select("version").
		From("extension_schema_migrations").
		Where("namespace = ?", namespace).
		ToSql()
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
