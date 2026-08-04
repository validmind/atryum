package store

import (
	"database/sql"
	"path/filepath"
	"testing"

	storemigrations "github.com/validmind/atryum/pkg/migrations"

	_ "modernc.org/sqlite"
)

func TestRunExtensionMigrations(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	if err := InitDBWithDialect(db, DialectSQLite); err != nil {
		t.Fatalf("init db: %v", err)
	}

	definitions := []storemigrations.Definition{
		{
			Version: 1,
			Name:    "001_widgets",
			Steps: []storemigrations.Step{
				storemigrations.Raw("create widgets", `CREATE TABLE widgets (id TEXT PRIMARY KEY, name TEXT NOT NULL)`),
				storemigrations.Raw("seed widgets", `INSERT INTO widgets (id, name) VALUES ('w1', 'first')`),
			},
		},
	}

	if err := RunExtensionMigrationsWithDialect(db, DialectSQLite, "example", definitions); err != nil {
		t.Fatalf("run extension migrations: %v", err)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM widgets`).Scan(&count); err != nil {
		t.Fatalf("query widgets: %v", err)
	}
	if count != 1 {
		t.Fatalf("widgets count = %d, want 1", count)
	}

	t.Run("already applied migrations are skipped on rerun", func(t *testing.T) {
		if err := RunExtensionMigrationsWithDialect(db, DialectSQLite, "example", definitions); err != nil {
			t.Fatalf("rerun extension migrations: %v", err)
		}
		if err := db.QueryRow(`SELECT COUNT(*) FROM widgets`).Scan(&count); err != nil {
			t.Fatalf("query widgets: %v", err)
		}
		if count != 1 {
			t.Fatalf("widgets count after rerun = %d, want 1", count)
		}
	})

	t.Run("same version in another namespace applies independently", func(t *testing.T) {
		other := []storemigrations.Definition{
			{
				Version: 1,
				Name:    "001_gadgets",
				Steps: []storemigrations.Step{
					storemigrations.Raw("create gadgets", `CREATE TABLE gadgets (id TEXT PRIMARY KEY)`),
				},
			},
		}
		if err := RunExtensionMigrationsWithDialect(db, DialectSQLite, "other", other); err != nil {
			t.Fatalf("run other namespace migrations: %v", err)
		}
		if err := db.QueryRow(`SELECT COUNT(*) FROM extension_schema_migrations`).Scan(&count); err != nil {
			t.Fatalf("query extension_schema_migrations: %v", err)
		}
		if count != 2 {
			t.Fatalf("extension migration records = %d, want 2", count)
		}
	})

	t.Run("empty namespace is rejected", func(t *testing.T) {
		if err := RunExtensionMigrationsWithDialect(db, DialectSQLite, "", definitions); err == nil {
			t.Fatal("expected error for empty namespace")
		}
	})
}
