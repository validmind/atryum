package migrations

func migration025() Definition {
	return Definition{
		Version: 25,
		Name:    "025_external_sessions",
		Steps: []Step{
			// AddColumnIfMissing rather than a bare ADD COLUMN: migration
			// numbering can shift across a rebase (see AddColumnIfMissing's doc
			// comment), so a database already stamped past this version under a
			// different numbering must not crash re-running this ALTER.
			AddColumnIfMissing("invocations", "session_id", "TEXT", "TEXT"),
			RawDialect("create external_sessions table", `
				CREATE TABLE IF NOT EXISTS external_sessions (
					id                TEXT      PRIMARY KEY,
					agent_id          TEXT      NOT NULL DEFAULT '',
					harness           TEXT      NOT NULL DEFAULT '',
					client_session_id TEXT      NOT NULL DEFAULT '',
					created_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
					last_seen_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
					expires_at        TIMESTAMP
				)
			`, `
				CREATE TABLE IF NOT EXISTS external_sessions (
					id                TEXT        PRIMARY KEY,
					agent_id          TEXT        NOT NULL DEFAULT '',
					harness           TEXT        NOT NULL DEFAULT '',
					client_session_id TEXT        NOT NULL DEFAULT '',
					created_at        TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
					last_seen_at      TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
					expires_at        TIMESTAMPTZ
				)
			`),
			// Databases that created external_sessions under an earlier numbering
			// of this migration predate the expires_at column above; add it for
			// them. Rows backfill to NULL, which lookupSessionForAgent treats as
			// non-expiring (IsZero guard); the service sets a concrete expires_at
			// on every new/touched session.
			AddColumnIfMissing("external_sessions", "expires_at", "TIMESTAMP", "TIMESTAMPTZ"),
			Raw("index invocations by session_id", `CREATE INDEX IF NOT EXISTS idx_invocations_session_id ON invocations (session_id)`),
		},
	}
}
