package migrations

func migration009() Definition {
	return Definition{
		Version: 9,
		Name:    "009_agent_clients.sql",
		Steps: []Step{
			RawDialect(
				"create agent_clients table",
				`CREATE TABLE IF NOT EXISTS agent_clients (
					agent_id        TEXT PRIMARY KEY,
					client_name     TEXT,
					client_version  TEXT,
					subject         TEXT,
					issuer          TEXT,
					last_seen_at    TIMESTAMP NOT NULL
				)`,
				`CREATE TABLE IF NOT EXISTS agent_clients (
					agent_id        TEXT PRIMARY KEY,
					client_name     TEXT,
					client_version  TEXT,
					subject         TEXT,
					issuer          TEXT,
					last_seen_at    TIMESTAMPTZ NOT NULL
				)`,
			),
		},
	}
}
