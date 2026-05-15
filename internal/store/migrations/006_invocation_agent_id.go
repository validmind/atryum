package migrations

func migration006() Definition {
	return Definition{
		Version: 6,
		Name:    "006_invocation_agent_id.sql",
		Steps: []Step{
			Raw("add agent_id to invocations", `ALTER TABLE invocations ADD COLUMN agent_id TEXT`),
			Raw("create invocations agent_id index", `CREATE INDEX IF NOT EXISTS idx_invocations_agent_id ON invocations(agent_id)`),
		},
	}
}
