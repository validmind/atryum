package migrations

func migration015() Definition {
	return Definition{
		Version: 15,
		Name:    "015_anonymous_agent_record_cuid",
		Steps: []Step{
			Raw(
				"add anonymous_agent_record_cuid to agent_sync_settings",
				`ALTER TABLE agent_sync_settings ADD COLUMN anonymous_agent_record_cuid TEXT NOT NULL DEFAULT ''`,
			),
		},
	}
}
