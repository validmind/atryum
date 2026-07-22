package migrations

func migration008() Definition {
	return Definition{
		Version: 8,
		Name:    "008_oauth_client_registration.sql",
		Steps: []Step{
			Raw("add oauth_client_registration", `ALTER TABLE mcp_servers ADD COLUMN oauth_client_registration TEXT`),
		},
	}
}
