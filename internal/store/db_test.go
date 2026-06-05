package store

import "testing"

func TestResolveDBTarget_DefaultsToSQLitePath(t *testing.T) {
	target, err := ResolveDBTarget("", "./custom.db")
	if err != nil {
		t.Fatalf("ResolveDBTarget: %v", err)
	}
	if target.DriverName != "sqlite" || target.Dialect != DialectSQLite || target.DSN != "./custom.db" {
		t.Fatalf("unexpected target: %+v", target)
	}
}

func TestResolveDBTarget_SelectsPostgresForPostgresSchemes(t *testing.T) {
	for _, dsn := range []string{
		"postgres://postgres:password@127.0.0.1:5432/postgres",
		"postgresql://postgres:password@127.0.0.1:5432/postgres",
	} {
		target, err := ResolveDBTarget(dsn, "./ignored.db")
		if err != nil {
			t.Fatalf("ResolveDBTarget(%q): %v", dsn, err)
		}
		if target.DriverName != "pgx" || target.Dialect != DialectPostgres || target.DSN != dsn {
			t.Fatalf("unexpected target for %q: %+v", dsn, target)
		}
	}
}

func TestResolveDBTarget_SelectsSQLiteForSQLiteFileAndBarePaths(t *testing.T) {
	cases := map[string]string{
		"sqlite:///tmp/atryum.db": "/tmp/atryum.db",
		"sqlite://local.db":       "local.db",
		"file:local.db":           "file:local.db",
		"./local.db":              "./local.db",
	}
	for input, wantDSN := range cases {
		target, err := ResolveDBTarget(input, "./ignored.db")
		if err != nil {
			t.Fatalf("ResolveDBTarget(%q): %v", input, err)
		}
		if target.DriverName != "sqlite" || target.Dialect != DialectSQLite || target.DSN != wantDSN {
			t.Fatalf("unexpected target for %q: %+v", input, target)
		}
	}
}

func TestMigrationRegistryPreservesExistingVersionsAndNames(t *testing.T) {
	if len(migrations) != 15 {
		t.Fatalf("migration count = %d", len(migrations))
	}
	want := []struct {
		version int
		name    string
	}{
		{1, "001_init_schema.sql"},
		{2, "002_server_status_columns.sql"},
		{3, "003_oauth_tables.sql"},
		{4, "004_rules_table.sql"},
		{5, "005_matched_rule_id.sql"},
		{6, "006_invocation_agent_id.sql"},
		{7, "007_rename_user_pattern.sql"},
		{8, "008_oauth_client_registration.sql"},
		{9, "009_agents_table"},
		{10, "010_ai_evaluation_rule"},
		{11, "011_agent_sync_settings"},
		{12, "012_invocation_summary"},
		{13, "013_summary_model_config"},
		{14, "014_invocation_client_info.sql"},
		{15, "015_default_agent_record"},
	}
	for i, w := range want {
		if migrations[i].Version != w.version || migrations[i].Name != w.name {
			t.Fatalf("migration[%d] = (%d, %q), want (%d, %q)", i, migrations[i].Version, migrations[i].Name, w.version, w.name)
		}
	}
}

func TestGetPendingMigrationsUsesRegistryOrder(t *testing.T) {
	pending := getPendingMigrations(map[int]bool{1: true})
	if len(pending) != 14 {
		t.Fatalf("pending count = %d", len(pending))
	}
	wantVersions := []int{2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}
	for i, want := range wantVersions {
		if pending[i].Version != want {
			t.Fatalf("pending[%d].Version = %d, want %d", i, pending[i].Version, want)
		}
	}
}

func TestMigrationRegistryBuildsDialectSpecificDDL(t *testing.T) {
	step := migrations[0].Steps[4]
	sqliteSQL, _, err := step.Build(DialectSQLite)
	if err != nil {
		t.Fatalf("sqlite build: %v", err)
	}
	postgresSQL, _, err := step.Build(DialectPostgres)
	if err != nil {
		t.Fatalf("postgres build: %v", err)
	}
	if !contains(sqliteSQL, "AUTOINCREMENT") {
		t.Fatalf("sqlite SQL should use AUTOINCREMENT: %s", sqliteSQL)
	}
	if !contains(postgresSQL, "BIGSERIAL") || contains(postgresSQL, "AUTOINCREMENT") {
		t.Fatalf("postgres SQL should use BIGSERIAL only: %s", postgresSQL)
	}
}

func TestStatementBuilderForDialectUsesDialectPlaceholders(t *testing.T) {
	sqliteSQL, _, err := statementBuilderForDialect(DialectSQLite).Select("*").From("mcp_servers").Where("name = ?", "shortcut").ToSql()
	if err != nil {
		t.Fatalf("sqlite ToSql: %v", err)
	}
	if sqliteSQL != "SELECT * FROM mcp_servers WHERE name = ?" {
		t.Fatalf("sqlite SQL = %q", sqliteSQL)
	}

	postgresSQL, _, err := statementBuilderForDialect(DialectPostgres).Select("*").From("mcp_servers").Where("name = ?", "shortcut").ToSql()
	if err != nil {
		t.Fatalf("postgres ToSql: %v", err)
	}
	if postgresSQL != "SELECT * FROM mcp_servers WHERE name = $1" {
		t.Fatalf("postgres SQL = %q", postgresSQL)
	}
}

func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
