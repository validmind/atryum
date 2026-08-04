package migrations

func migration019() Definition {
	return Definition{
		Version: 19,
		Name:    "019_settings_summary_llm",
		Steps: []Step{
			Raw("add summary_atryum_llm_config_id column to agent_sync_settings", `
				ALTER TABLE agent_sync_settings ADD COLUMN summary_atryum_llm_config_id TEXT NOT NULL DEFAULT ''
			`),
		},
	}
}
