package migrations

func migration013() Definition {
	return Definition{
		Version: 13,
		Name:    "013_summary_model_config",
		Steps: []Step{
			Raw(
				"add summary_model_config_cuid to agent_sync_settings",
				`ALTER TABLE agent_sync_settings ADD COLUMN summary_model_config_cuid TEXT NOT NULL DEFAULT ''`,
			),
		},
	}
}
