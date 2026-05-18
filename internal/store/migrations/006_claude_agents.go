package migrations

func migration006() Definition {
	return Definition{
		Version: 6,
		Name:    "006_claude_agents.sql",
		Steps: []Step{
			RawDialect("create claude_agents table", `
				CREATE TABLE IF NOT EXISTS claude_agents (
					id          TEXT    PRIMARY KEY,
					name        TEXT    NOT NULL UNIQUE,
					agent_id    TEXT    NOT NULL,
					description TEXT,
					enabled     INTEGER NOT NULL DEFAULT 1,
					created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
					updated_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
				)
			`, `
				CREATE TABLE IF NOT EXISTS claude_agents (
					id          TEXT    PRIMARY KEY,
					name        TEXT    NOT NULL UNIQUE,
					agent_id    TEXT    NOT NULL,
					description TEXT,
					enabled     BOOLEAN NOT NULL DEFAULT TRUE,
					created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
					updated_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
				)
			`),
			RawDialect("create claude_agent_sessions table", `
				CREATE TABLE IF NOT EXISTS claude_agent_sessions (
					id                TEXT    PRIMARY KEY,
					agent_id          TEXT    NOT NULL REFERENCES claude_agents(id) ON DELETE CASCADE,
					session_id        TEXT    NOT NULL UNIQUE,
					last_event_cursor TEXT,
					created_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
					updated_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
				)
			`, `
				CREATE TABLE IF NOT EXISTS claude_agent_sessions (
					id                TEXT    PRIMARY KEY,
					agent_id          TEXT    NOT NULL REFERENCES claude_agents(id) ON DELETE CASCADE,
					session_id        TEXT    NOT NULL UNIQUE,
					last_event_cursor TEXT,
					created_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
					updated_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
				)
			`),
			Raw("create claude_agent_sessions agent_id index", `
				CREATE INDEX IF NOT EXISTS idx_claude_agent_sessions_agent_id ON claude_agent_sessions (agent_id)
			`),
		},
	}
}
