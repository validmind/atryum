package migrations

func migration025() Definition {
	return Definition{
		Version: 25,
		Name:    "025_external_sessions",
		Steps: []Step{
			// AddColumnIfMissing rather than a bare ADD COLUMN: migration
			// numbering can shift across a rebase (see 026's comment and
			// AddColumnIfMissing's doc comment), so a database already
			// stamped past this version under a different numbering must
			// not crash re-running this ALTER.
			AddColumnIfMissing("invocations", "session_id", "TEXT", "TEXT"),
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
