package migrations

func migration011() Definition {
	return Definition{
		Version: 11,
		Name:    "011_agent_sync_settings",
		Steps: []Step{
		RawDialect("create agent_sync_settings table", `
			CREATE TABLE IF NOT EXISTS agent_sync_settings (
				id                     INTEGER  PRIMARY KEY DEFAULT 1,
				org_cuid               TEXT     NOT NULL DEFAULT '',
				agent_record_type_slug TEXT     NOT NULL DEFAULT '',
				constitution_field_key TEXT     NOT NULL DEFAULT '',
				updated_at             DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
				CHECK (id = 1)
			)
		`, `
			CREATE TABLE IF NOT EXISTS agent_sync_settings (
				id                     INTEGER  PRIMARY KEY DEFAULT 1,
				org_cuid               TEXT     NOT NULL DEFAULT '',
				agent_record_type_slug TEXT     NOT NULL DEFAULT '',
				constitution_field_key TEXT     NOT NULL DEFAULT '',
				updated_at             TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
				CHECK (id = 1)
			)
		`),
		},
	}
}
