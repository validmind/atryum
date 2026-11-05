package migrations

import (
	"database/sql"
	"fmt"
	"regexp"
	"strings"
)

// tenantTables are atryum's tables that hold one tenant's rows. The
// migration bookkeeping tables are shared and stay out.
var tenantTables = []string{
	"agent_members", "agent_sync_settings", "agents", "api_keys", "approval_rules",
	"external_sessions", "invocation_events", "invocations", "llm_configs",
	"managed_agent_bindings", "managed_agent_sessions", "mcp_servers",
	"oauth_connect_sessions", "oauth_credentials", "plan_events", "plans", "users",
}

// sqliteConflictKeys are the keys upserts name in ON CONFLICT, which SQLite
// matches only against a unique index on exactly those columns.
var sqliteConflictKeys = map[string]string{
	"agent_sync_settings":    "id",
	"agents":                 "vm_cuid",
	"external_sessions":      "id",
	"managed_agent_sessions": "session_id",
	"mcp_servers":            "name",
	"oauth_connect_sessions": "state",
	"oauth_credentials":      "server_name",
}

// migration034 gives every row a tenant. On Postgres every primary key,
// unique key and foreign key gains tenant_id as its first column, so names
// are unique per tenant and no row can point at another tenant's, and
// row-level security shows a connection only the rows of the tenant it set
// in app.tenant_id (internal/store sets it from the request's context). A
// connection that set none sees no rows and can write none.
//
// Row-level security binds roles that neither own the tables nor have
// BYPASSRLS: an embedder running more than one tenant connects as such a
// role. SQLite has no row-level security and stays one tenant, Default.
func migration034() Definition {
	steps := make([]Step, 0, len(tenantTables)+1)
	for _, table := range tenantTables {
		steps = append(steps, AddColumnIfMissing(table, "tenant_id", "TEXT NOT NULL DEFAULT 'default'", "TEXT NOT NULL DEFAULT 'default'"))
	}
	steps = append(steps, Custom("scope keys and rows by tenant", func(tx *sql.Tx, usePostgres bool) error {
		if !usePostgres {
			return sqliteTenantKeys(tx)
		}
		return postgresTenantScope(tx)
	}))
	return Definition{Version: 34, Name: "034_tenant_scope", Steps: steps}
}

func sqliteTenantKeys(tx *sql.Tx) error {
	for table, cols := range sqliteConflictKeys {
		q := fmt.Sprintf("CREATE UNIQUE INDEX IF NOT EXISTS idx_%s_tenant_key ON %s(tenant_id, %s)", table, table, cols)
		if _, err := tx.Exec(q); err != nil {
			return fmt.Errorf("%s: %w", table, err)
		}
	}
	return nil
}

// fkDef is a foreign key's definition as pg_get_constraintdef prints it:
// FOREIGN KEY (a) REFERENCES t(b) [ON DELETE ...].
var fkDef = regexp.MustCompile(`^FOREIGN KEY \(([^)]*)\) REFERENCES ([^(]+)\(([^)]*)\)(.*)$`)

type pgConstraint struct{ table, name, def string }

func postgresTenantScope(tx *sql.Tx) error {
	for _, table := range tenantTables {
		if _, err := tx.Exec(fmt.Sprintf(`ALTER TABLE %s ALTER COLUMN tenant_id SET DEFAULT current_setting('app.tenant_id')`, table)); err != nil {
			return fmt.Errorf("%s: tenant_id default: %w", table, err)
		}
	}

	// Foreign keys go first: they hang on the keys rebuilt below.
	fks, err := pgConstraints(tx, "f")
	if err != nil {
		return err
	}
	for _, fk := range fks {
		if _, err := tx.Exec(fmt.Sprintf(`ALTER TABLE %s DROP CONSTRAINT %s`, fk.table, fk.name)); err != nil {
			return fmt.Errorf("%s: drop %s: %w", fk.table, fk.name, err)
		}
	}

	keys, err := pgConstraints(tx, "p", "u")
	if err != nil {
		return err
	}
	for _, k := range keys {
		def := strings.Replace(k.def, "(", "(tenant_id, ", 1)
		if _, err := tx.Exec(fmt.Sprintf(`ALTER TABLE %s DROP CONSTRAINT %s, ADD CONSTRAINT %s %s`, k.table, k.name, k.name, def)); err != nil {
			return fmt.Errorf("%s: rebuild %s: %w", k.table, k.name, err)
		}
	}

	indexes, err := pgUniqueIndexes(tx)
	if err != nil {
		return err
	}
	for name, def := range indexes {
		def = strings.Replace(def, "USING btree (", "USING btree (tenant_id, ", 1)
		if _, err := tx.Exec(`DROP INDEX ` + name); err != nil {
			return fmt.Errorf("drop index %s: %w", name, err)
		}
		if _, err := tx.Exec(def); err != nil {
			return fmt.Errorf("rebuild index %s: %w", name, err)
		}
	}

	for _, fk := range fks {
		m := fkDef.FindStringSubmatch(fk.def)
		if m == nil {
			return fmt.Errorf("%s: can't read foreign key %s: %s", fk.table, fk.name, fk.def)
		}
		// Setting the whole key to NULL would null tenant_id too.
		rest := strings.Replace(m[4], "ON DELETE SET NULL", "ON DELETE SET NULL ("+m[1]+")", 1)
		def := fmt.Sprintf("FOREIGN KEY (tenant_id, %s) REFERENCES %s(tenant_id, %s)%s", m[1], m[2], m[3], rest)
		if _, err := tx.Exec(fmt.Sprintf(`ALTER TABLE %s ADD CONSTRAINT %s %s`, fk.table, fk.name, def)); err != nil {
			return fmt.Errorf("%s: rebuild %s: %w", fk.table, fk.name, err)
		}
	}

	for _, table := range tenantTables {
		if _, err := tx.Exec(fmt.Sprintf(`ALTER TABLE %s ENABLE ROW LEVEL SECURITY`, table)); err != nil {
			return fmt.Errorf("%s: enable row-level security: %w", table, err)
		}
		for _, q := range []string{
			fmt.Sprintf(`DROP POLICY IF EXISTS tenant ON %s`, table),
			fmt.Sprintf(`CREATE POLICY tenant ON %s USING (tenant_id = current_setting('app.tenant_id', true)) WITH CHECK (tenant_id = current_setting('app.tenant_id', true))`, table),
		} {
			if _, err := tx.Exec(q); err != nil {
				return fmt.Errorf("%s: tenant policy: %w", table, err)
			}
		}
	}
	return nil
}

// pgConstraints lists the constraints of the given types on tenantTables
// that don't already lead with tenant_id.
func pgConstraints(tx *sql.Tx, types ...string) ([]pgConstraint, error) {
	rows, err := tx.Query(`
		SELECT c.conrelid::regclass::text, c.conname, pg_get_constraintdef(c.oid)
		FROM pg_constraint c
		JOIN pg_class t ON t.oid = c.conrelid
		WHERE t.relnamespace = current_schema()::regnamespace
		  AND t.relname = ANY($1) AND c.contype::text = ANY($2)
		  AND (SELECT attname FROM pg_attribute WHERE attrelid = c.conrelid AND attnum = c.conkey[1]) <> 'tenant_id'
		ORDER BY 1, 2`, tenantTables, types)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []pgConstraint
	for rows.Next() {
		var c pgConstraint
		if err := rows.Scan(&c.table, &c.name, &c.def); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// pgUniqueIndexes maps the unique indexes on tenantTables that back no
// constraint and don't lead with tenant_id to their definitions. An index
// that leads with an expression has no column at indkey[0] (attnum 0), so
// the comparison is IS DISTINCT FROM, which counts that NULL as different.
func pgUniqueIndexes(tx *sql.Tx) (map[string]string, error) {
	rows, err := tx.Query(`
		SELECT ic.relname, pg_get_indexdef(i.indexrelid)
		FROM pg_index i
		JOIN pg_class t ON t.oid = i.indrelid
		JOIN pg_class ic ON ic.oid = i.indexrelid
		WHERE t.relnamespace = current_schema()::regnamespace
		  AND t.relname = ANY($1) AND i.indisunique
		  AND NOT EXISTS (SELECT 1 FROM pg_constraint c WHERE c.conindid = i.indexrelid)
		  AND (SELECT attname FROM pg_attribute WHERE attrelid = i.indrelid AND attnum = i.indkey[0]) IS DISTINCT FROM 'tenant_id'`, tenantTables)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var name, def string
		if err := rows.Scan(&name, &def); err != nil {
			return nil, err
		}
		out[name] = def
	}
	return out, rows.Err()
}
