// Package migrations defines atryum's schema migrations and the Definition
// and Step building blocks they are written with. It is public so embedding
// programs (see pkg/atryum) can express their own migrations with the same
// vocabulary and pass them to WithMigrations; extension migrations are
// versioned per namespace, separately from the built-in sequence in All.
package migrations

import (
	"database/sql"
	"fmt"
)

type Definition struct {
	Version int
	Name    string
	Steps   []Step
}

type Step struct {
	Description string
	Build       func(usePostgres bool) (string, []any, error)
	Run         func(tx *sql.Tx, usePostgres bool) error
}

func Raw(description, query string) Step {
	return Step{
		Description: description,
		Build: func(bool) (string, []any, error) {
			return query, nil, nil
		},
	}
}

func RawDialect(description, sqliteQuery, postgresQuery string) Step {
	return Step{
		Description: description,
		Build: func(usePostgres bool) (string, []any, error) {
			if usePostgres {
				return postgresQuery, nil, nil
			}
			return sqliteQuery, nil, nil
		},
	}
}

func Custom(description string, run func(tx *sql.Tx, usePostgres bool) error) Step {
	return Step{
		Description: description,
		Run:         run,
	}
}

// AddColumnIfMissing builds a step that adds a column to a table only if the
// column doesn't already exist.
//
// Why this exists: migrations are stamped by integer version in
// schema_migrations, and a plain "ALTER TABLE ... ADD COLUMN" is not
// idempotent — running it twice against the same physical schema fails
// (e.g. Postgres SQLSTATE 42701, "column already exists"). That collision
// is not hypothetical: this codebase has already been renumbered once by a
// rebase (main's 024_server_endpoint_slug was inserted ahead of this
// branch's session migrations, shifting them from 024/025 to 025/026), and
// a long-lived dev database stamped under the old numbering ran the same
// ALTER twice under two different version numbers. Renumbering is a normal
// consequence of parallel development landing on a shared migration
// sequence, so every bare ADD COLUMN step must be able to survive running
// against a database that already has the column, regardless of which
// version number it was stamped under. Guarding the ALTER on the column's
// actual presence (rather than trusting the version stamp) makes the step
// safe to run in that situation.
//
// table and column are compile-time constants supplied by call sites (not
// user input), so building the ALTER/PRAGMA/information_schema SQL with
// fmt.Sprintf is safe.
func AddColumnIfMissing(table, column, sqliteDef, postgresDef string) Step {
	description := fmt.Sprintf("add %s.%s if missing", table, column)
	return Custom(description, func(tx *sql.Tx, usePostgres bool) error {
		exists, err := columnExists(tx, table, column, usePostgres)
		if err != nil {
			return fmt.Errorf("check column %s.%s: %w", table, column, err)
		}
		if exists {
			return nil
		}
		def := sqliteDef
		if usePostgres {
			def = postgresDef
		}
		alter := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, def)
		if _, err := tx.Exec(alter); err != nil {
			return fmt.Errorf("add column %s.%s: %w", table, column, err)
		}
		return nil
	})
}

// columnExists reports whether the given column is already present on
// table. Postgres supports bound parameters against information_schema;
// SQLite's PRAGMA statements do not accept bound parameters at all, so the
// table name is inlined there (see AddColumnIfMissing for why that's safe).
func columnExists(tx *sql.Tx, table, column string, usePostgres bool) (bool, error) {
	if usePostgres {
		var name string
		err := tx.QueryRow(
			`SELECT column_name FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = $1 AND column_name = $2`,
			table, column,
		).Scan(&name)
		if err == sql.ErrNoRows {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return true, nil
	}

	// PRAGMA statements don't accept bound parameters, so the table name is
	// inlined via fmt.Sprintf (safe: it's a compile-time constant, not user
	// input). table_info's column shape (cid, name, type, notnull,
	// dflt_value, pk) is stable across SQLite versions.
	rows, err := tx.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return false, err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			cid        int
			name       string
			colType    string
			notNull    int
			defaultVal sql.NullString
			pk         int
		)
		if err := rows.Scan(&cid, &name, &colType, &notNull, &defaultVal, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

func All() []Definition {
	return []Definition{
		migration001(),
		migration002(),
		migration003(),
		migration004(),
		migration005(),
		migration006(),
		migration007(),
		migration008(),
		migration009(),
		migration010(),
		migration011(),
		migration012(),
		migration013(),
		migration014(),
		migration015(),
		migration016(),
		migration017(),
		migration018(),
		migration019(),
		migration020(),
		migration021(),
		migration022(),
		migration023(),
		migration024(),
		migration025(),
		migration026(),
		migration027(),
		migration028(),
	}
}
