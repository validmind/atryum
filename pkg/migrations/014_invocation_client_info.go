package migrations

func migration014() Definition {
	return Definition{
		Version: 14,
		Name:    "014_invocation_client_info.sql",
		Steps: []Step{
			Raw("add client_name to invocations", `ALTER TABLE invocations ADD COLUMN client_name TEXT`),
			Raw("add client_version to invocations", `ALTER TABLE invocations ADD COLUMN client_version TEXT`),
		},
	}
}
