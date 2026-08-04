package migrations

func migration021() Definition {
	return Definition{
		Version: 21,
		Name:    "021_rename_constitution_to_charter",
		Steps: []Step{
			Raw("rename agents.constitution to agents.charter",
				`ALTER TABLE agents RENAME COLUMN constitution TO charter`),
			Raw("rename agent_sync_settings.constitution_field_key to agent_sync_settings.charter_field_key",
				`ALTER TABLE agent_sync_settings RENAME COLUMN constitution_field_key TO charter_field_key`),
		},
	}
}
