package migrations

func migration016() Definition {
	return Definition{
		Version: 16,
		Name:    "016_llm_configs",
		Steps: []Step{
			RawDialect("create llm_configs table", `
				CREATE TABLE IF NOT EXISTS llm_configs (
					id         TEXT PRIMARY KEY,
					name       TEXT NOT NULL,
					provider   TEXT NOT NULL,
					model      TEXT NOT NULL,
					api_key    TEXT NOT NULL DEFAULT '',
					base_url   TEXT NOT NULL DEFAULT '',
					enabled    INTEGER NOT NULL DEFAULT 1,
					created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
				)
			`, `
				CREATE TABLE IF NOT EXISTS llm_configs (
					id         TEXT PRIMARY KEY,
					name       TEXT NOT NULL,
					provider   TEXT NOT NULL,
					model      TEXT NOT NULL,
					api_key    TEXT NOT NULL DEFAULT '',
					base_url   TEXT NOT NULL DEFAULT '',
					enabled    BOOLEAN NOT NULL DEFAULT TRUE,
					created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
				)
			`),
		},
	}
}
