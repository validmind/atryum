package migrations

func migration015() Definition {
	return Definition{
		Version: 15,
		Name:    "015_default_agent_record",
		Steps: []Step{
			Raw("add default agent record to sync settings", `
				ALTER TABLE agent_sync_settings ADD COLUMN default_agent_vm_cuid TEXT NOT NULL DEFAULT ''
			`),
		},
	}
}
