package migrations

func migration009() Definition {
	return Definition{
		Version: 9,
		Name:    "009_agents_table",
		Steps: []Step{
			RawDialect("create agents table", `
				CREATE TABLE IF NOT EXISTS agents (
					id                   TEXT      PRIMARY KEY,
					vm_organization_cuid TEXT      NOT NULL,
					vm_organization_name TEXT      NOT NULL,
					vm_cuid              TEXT      NOT NULL UNIQUE,
					vm_name              TEXT      NOT NULL,
					vm_description       TEXT,
					agent_ids            TEXT      NOT NULL DEFAULT '[]',
					enabled              INTEGER   NOT NULL DEFAULT 1,
					synced_at            TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
				)
			`, `
				CREATE TABLE IF NOT EXISTS agents (
					id                   TEXT      PRIMARY KEY,
					vm_organization_cuid TEXT      NOT NULL,
					vm_organization_name TEXT      NOT NULL,
					vm_cuid              TEXT      NOT NULL UNIQUE,
					vm_name              TEXT      NOT NULL,
					vm_description       TEXT,
					agent_ids            JSONB     NOT NULL DEFAULT '[]',
					enabled              BOOLEAN   NOT NULL DEFAULT TRUE,
					synced_at            TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
				)
			`),
			Raw("create agents vm_cuid index", `
				CREATE INDEX IF NOT EXISTS idx_agents_vm_cuid ON agents (vm_cuid)
			`),
			Raw("create agents vm_organization_cuid index", `
				CREATE INDEX IF NOT EXISTS idx_agents_vm_organization_cuid ON agents (vm_organization_cuid)
			`),
		},
	}
}
