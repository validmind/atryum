package migrations

func migration020() Definition {
	return Definition{
		Version: 20,
		Name:    "020_managed_agent_sessions",
		Steps: []Step{
			RawDialect("create managed_agent_sessions table", `
				CREATE TABLE IF NOT EXISTS managed_agent_sessions (
					session_id    TEXT      PRIMARY KEY,
					account       TEXT      NOT NULL DEFAULT 'default',
					agent_id      TEXT      NOT NULL DEFAULT '',
					description   TEXT      NOT NULL DEFAULT '',
					last_event_id TEXT      NOT NULL DEFAULT '',
					created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
					updated_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
				)
			`, `
				CREATE TABLE IF NOT EXISTS managed_agent_sessions (
					session_id    TEXT        PRIMARY KEY,
					account       TEXT        NOT NULL DEFAULT 'default',
					agent_id      TEXT        NOT NULL DEFAULT '',
					description   TEXT        NOT NULL DEFAULT '',
					last_event_id TEXT        NOT NULL DEFAULT '',
					created_at    TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
					updated_at    TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
				)
			`),
		},
	}
}
