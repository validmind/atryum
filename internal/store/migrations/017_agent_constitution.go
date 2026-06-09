package migrations

func migration017() Definition {
	return Definition{
		Version: 17,
		Name:    "017_agent_constitution",
		Steps: []Step{
			Raw("add constitution column to agents", `
				ALTER TABLE agents ADD COLUMN constitution TEXT NOT NULL DEFAULT ''
			`),
		},
	}
}
