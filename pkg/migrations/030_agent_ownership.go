package migrations

func migration030() Definition {
	return Definition{
		Version: 30,
		Name:    "030_agent_ownership",
		Steps: []Step{
			AddColumnIfMissing("invocations", "agent_cuid", "TEXT", "TEXT"),
			AddColumnIfMissing("plans", "agent_cuid", "TEXT", "TEXT"),
			AddColumnIfMissing("external_sessions", "agent_cuid", "TEXT", "TEXT"),
			Raw("replace global invocation idempotency index", `
				DROP INDEX IF EXISTS idx_invocations_idempotency_key`),
			Raw("scope invocation idempotency by stable agent", `
				CREATE UNIQUE INDEX IF NOT EXISTS idx_invocations_agent_cuid_idempotency_key
					ON invocations(agent_cuid, idempotency_key)
					WHERE agent_cuid IS NOT NULL AND idempotency_key IS NOT NULL`),
			Raw("preserve idempotency for legacy unowned invocations", `
				CREATE UNIQUE INDEX IF NOT EXISTS idx_invocations_unowned_idempotency_key
					ON invocations(idempotency_key)
					WHERE agent_cuid IS NULL AND idempotency_key IS NOT NULL`),
			Raw("index invocations by stable agent", `
				CREATE INDEX IF NOT EXISTS idx_invocations_agent_cuid
					ON invocations(agent_cuid)`),
			Raw("index plans by stable agent", `
				CREATE INDEX IF NOT EXISTS idx_plans_agent_cuid
					ON plans(agent_cuid)`),
			Raw("index external sessions by stable agent", `
				CREATE INDEX IF NOT EXISTS idx_external_sessions_agent_cuid
					ON external_sessions(agent_cuid)`),
		},
	}
}
