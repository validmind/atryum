package store

import (
	"database/sql"
	"embed"
	"fmt"
	"sort"
	"strings"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// migrationRecord tracks applied migrations.
type migrationRecord struct {
	Version int
	Name    string
}

// InitDB runs all pending migrations in order. It creates a schema_migrations
// table to track which migrations have been applied.
func InitDB(db *sql.DB) error {
	if _, err := db.Exec(`PRAGMA foreign_keys = ON;`); err != nil {
		return fmt.Errorf("enable foreign keys: %w", err)
	}

	if err := ensureMigrationsTable(db); err != nil {
		return fmt.Errorf("ensure migrations table: %w", err)
	}

	applied, err := getAppliedMigrations(db)
	if err != nil {
		return fmt.Errorf("get applied migrations: %w", err)
	}

	pending, err := getPendingMigrations(applied)
	if err != nil {
		return fmt.Errorf("get pending migrations: %w", err)
	}

	for _, m := range pending {
		if err := applyMigration(db, m); err != nil {
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

func getAppliedMigrations(db *sql.DB) (map[int]bool, error) {
	rows, err := db.Query(`SELECT version FROM schema_migrations`)
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

type migrationFile struct {
	Version int
	Name    string
	Content string
}

func getPendingMigrations(applied map[int]bool) ([]migrationFile, error) {
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return nil, err
	}

	var pending []migrationFile
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}

		// Extract version number from filename prefix (e.g. "001" from "001_init_schema.sql")
		underscoreIdx := strings.Index(entry.Name(), "_")
		if underscoreIdx < 0 {
			continue
		}
		var version int
		if _, err := fmt.Sscanf(entry.Name()[:underscoreIdx], "%d", &version); err != nil {
			continue
		}
		name := entry.Name()

		if applied[version] {
			continue
		}

		content, err := migrationFS.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", entry.Name(), err)
		}

		pending = append(pending, migrationFile{
			Version: version,
			Name:    name,
			Content: string(content),
		})
	}

	sort.Slice(pending, func(i, j int) bool {
		return pending[i].Version < pending[j].Version
	})

	return pending, nil
}

func applyMigration(db *sql.DB, m migrationFile) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(m.Content); err != nil {
		return fmt.Errorf("exec migration: %w", err)
	}

	if _, err := tx.Exec(
		`INSERT INTO schema_migrations (version, name) VALUES (?, ?)`,
		m.Version, m.Name,
	); err != nil {
		return fmt.Errorf("record migration: %w", err)
	}

	return tx.Commit()
}
