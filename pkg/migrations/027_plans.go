package migrations

func migration027() Definition {
	return Definition{
		Version: 27,
		Name:    "027_plans",
		Steps: []Step{
			RawDialect("create plans table", `
				CREATE TABLE IF NOT EXISTS plans (
					plan_id         TEXT      PRIMARY KEY,
					agent_id        TEXT      NOT NULL,
					source          TEXT      NOT NULL DEFAULT '',
					thread_id       TEXT      NOT NULL DEFAULT '',
					goal            TEXT      NOT NULL,
					rationale       TEXT      NOT NULL DEFAULT '',
					actions_json    TEXT      NOT NULL DEFAULT '[]',
					status          TEXT      NOT NULL,
					approval_json   TEXT,
					matched_rule_id TEXT,
					feedback        TEXT      NOT NULL DEFAULT '',
					parent_plan_id  TEXT,
					revision        INTEGER   NOT NULL DEFAULT 1,
					ttl_seconds     INTEGER   NOT NULL DEFAULT 3600,
					client_name     TEXT,
					client_version  TEXT,
					expires_at      TIMESTAMP,
					submitted_at    TIMESTAMP NOT NULL,
					decided_at      TIMESTAMP,
					created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
					updated_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
				)
			`, `
				CREATE TABLE IF NOT EXISTS plans (
					plan_id         TEXT        PRIMARY KEY,
					agent_id        TEXT        NOT NULL,
					source          TEXT        NOT NULL DEFAULT '',
					thread_id       TEXT        NOT NULL DEFAULT '',
					goal            TEXT        NOT NULL,
					rationale       TEXT        NOT NULL DEFAULT '',
					actions_json    TEXT        NOT NULL DEFAULT '[]',
					status          TEXT        NOT NULL,
					approval_json   TEXT,
					matched_rule_id TEXT,
					feedback        TEXT        NOT NULL DEFAULT '',
					parent_plan_id  TEXT,
					revision        INTEGER     NOT NULL DEFAULT 1,
					ttl_seconds     INTEGER     NOT NULL DEFAULT 3600,
					client_name     TEXT,
					client_version  TEXT,
					expires_at      TIMESTAMPTZ,
					submitted_at    TIMESTAMPTZ NOT NULL,
					decided_at      TIMESTAMPTZ,
					created_at      TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
					updated_at      TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
				)
			`),
			Raw("create plans agent/status index", `
				CREATE INDEX IF NOT EXISTS idx_plans_agent_status ON plans(agent_id, status)
			`),
			Raw("create plans parent index", `
				CREATE INDEX IF NOT EXISTS idx_plans_parent ON plans(parent_plan_id)
			`),
			RawDialect("create plan_events table", `
				CREATE TABLE IF NOT EXISTS plan_events (
					id INTEGER PRIMARY KEY AUTOINCREMENT,
					plan_id TEXT NOT NULL,
					event_type TEXT NOT NULL,
					payload_json TEXT NOT NULL,
					created_at TIMESTAMP NOT NULL,
					FOREIGN KEY(plan_id) REFERENCES plans(plan_id)
				)
			`, `
				CREATE TABLE IF NOT EXISTS plan_events (
					id BIGSERIAL PRIMARY KEY,
					plan_id TEXT NOT NULL,
					event_type TEXT NOT NULL,
					payload_json TEXT NOT NULL,
					created_at TIMESTAMPTZ NOT NULL,
					FOREIGN KEY(plan_id) REFERENCES plans(plan_id)
				)
			`),
			Raw("create plan_events lookup index", `
				CREATE INDEX IF NOT EXISTS idx_plan_events_lookup ON plan_events(plan_id, id)
			`),
			AddColumnIfMissing("invocations", "plan_id", "TEXT", "TEXT"),
			AddColumnIfMissing("approval_rules", "applies_to", "TEXT NOT NULL DEFAULT 'invocation'", "TEXT NOT NULL DEFAULT 'invocation'"),
		},
	}
}
