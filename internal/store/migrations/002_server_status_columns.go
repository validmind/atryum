package migrations

func migration002() Definition {
	return Definition{
		Version: 2,
		Name:    "002_server_status_columns.sql",
		Steps: []Step{
			Raw("add auth_type", `ALTER TABLE mcp_servers ADD COLUMN auth_type TEXT NOT NULL DEFAULT 'none'`),
			Raw("add connection_status", `ALTER TABLE mcp_servers ADD COLUMN connection_status TEXT NOT NULL DEFAULT 'unknown'`),
			Raw("add auth_status", `ALTER TABLE mcp_servers ADD COLUMN auth_status TEXT NOT NULL DEFAULT 'unknown'`),
			Raw("add reauth_needed", `ALTER TABLE mcp_servers ADD COLUMN reauth_needed INTEGER NOT NULL DEFAULT 0`),
			Raw("add last_checked_at", `ALTER TABLE mcp_servers ADD COLUMN last_checked_at TIMESTAMP`),
			Raw("add last_check_ok", `ALTER TABLE mcp_servers ADD COLUMN last_check_ok INTEGER NOT NULL DEFAULT 0`),
			Raw("add last_error_summary", `ALTER TABLE mcp_servers ADD COLUMN last_error_summary TEXT`),
			Raw("add action_required", `ALTER TABLE mcp_servers ADD COLUMN action_required TEXT`),
		},
	}
}
