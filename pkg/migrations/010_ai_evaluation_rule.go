package migrations

func migration010() Definition {
	return Definition{
		Version: 10,
		Name:    "010_ai_evaluation_rule",
		Steps: []Step{
			Raw("add model_config_cuid column to approval_rules", `
				ALTER TABLE approval_rules ADD COLUMN model_config_cuid TEXT
			`),
			RawDialect("add agent_cuids column to approval_rules", `
				ALTER TABLE approval_rules ADD COLUMN agent_cuids TEXT NOT NULL DEFAULT '[]'
			`, `
				ALTER TABLE approval_rules ADD COLUMN agent_cuids JSONB NOT NULL DEFAULT '[]'
			`),
		},
	}
}
