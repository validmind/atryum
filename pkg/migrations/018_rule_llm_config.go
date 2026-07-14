package migrations

func migration018() Definition {
	return Definition{
		Version: 18,
		Name:    "018_rule_llm_config",
		Steps: []Step{
			Raw("add atryum_llm_config_id column to approval_rules", `
				ALTER TABLE approval_rules ADD COLUMN atryum_llm_config_id TEXT NOT NULL DEFAULT ''
			`),
		},
	}
}
