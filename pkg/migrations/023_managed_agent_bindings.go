package migrations

func migration023() Definition {
	return Definition{
		Version: 23,
		Name:    "023_managed_agent_bindings",
		Steps: []Step{
			RawDialect("create managed_agent_bindings table", `
				CREATE TABLE IF NOT EXISTS managed_agent_bindings (
					id                   TEXT      PRIMARY KEY,
					agent_cuid           TEXT      NOT NULL,
					account              TEXT      NOT NULL DEFAULT 'default',
					claude_agent_id      TEXT      NOT NULL,
					claude_agent_name    TEXT      NOT NULL DEFAULT '',
					claude_agent_model   TEXT      NOT NULL DEFAULT '',
					claude_agent_version INTEGER   NOT NULL DEFAULT 0,
					created_at           TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
					updated_at           TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
					UNIQUE(agent_cuid, account, claude_agent_id),
					UNIQUE(account, claude_agent_id),
					FOREIGN KEY(agent_cuid) REFERENCES agents(id) ON DELETE CASCADE
				)
			`, `
				CREATE TABLE IF NOT EXISTS managed_agent_bindings (
					id                   TEXT        PRIMARY KEY,
					agent_cuid           TEXT        NOT NULL,
					account              TEXT        NOT NULL DEFAULT 'default',
					claude_agent_id      TEXT        NOT NULL,
					claude_agent_name    TEXT        NOT NULL DEFAULT '',
					claude_agent_model   TEXT        NOT NULL DEFAULT '',
					claude_agent_version INTEGER     NOT NULL DEFAULT 0,
					created_at           TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
					updated_at           TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
					UNIQUE(agent_cuid, account, claude_agent_id),
					UNIQUE(account, claude_agent_id),
					FOREIGN KEY(agent_cuid) REFERENCES agents(id) ON DELETE CASCADE
				)
			`),
		},
	}
}
