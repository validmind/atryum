package migrations

func migration025() Definition {
	return Definition{
		Version: 25,
		Name:    "025_external_sessions",
		Steps: []Step{
			Raw("add session_id to invocations", `ALTER TABLE invocations ADD COLUMN session_id TEXT`),
			RawDialect("create external_sessions table", `
				CREATE TABLE IF NOT EXISTS external_sessions (
					id                TEXT      PRIMARY KEY,
					agent_id          TEXT      NOT NULL DEFAULT '',
					harness           TEXT      NOT NULL DEFAULT '',
					client_session_id TEXT      NOT NULL DEFAULT '',
					created_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
					last_seen_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
				)
			`, `
				CREATE TABLE IF NOT EXISTS external_sessions (
					id                TEXT        PRIMARY KEY,
					agent_id          TEXT        NOT NULL DEFAULT '',
					harness           TEXT        NOT NULL DEFAULT '',
					client_session_id TEXT        NOT NULL DEFAULT '',
					created_at        TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
					last_seen_at      TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
				)
			`),
			Raw("index invocations by session_id", `CREATE INDEX IF NOT EXISTS idx_invocations_session_id ON invocations (session_id)`),
		},
	}
}
