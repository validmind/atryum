package migrations

func migration003() Definition {
	return Definition{
		Version: 3,
		Name:    "003_oauth_tables.sql",
		Steps: []Step{
			Raw("add oauth_provider_id", `ALTER TABLE mcp_servers ADD COLUMN oauth_provider_id TEXT`),
			Raw("add oauth_provider_label", `ALTER TABLE mcp_servers ADD COLUMN oauth_provider_label TEXT`),
			Raw("add oauth_authorize_url", `ALTER TABLE mcp_servers ADD COLUMN oauth_authorize_url TEXT`),
			Raw("add oauth_token_url", `ALTER TABLE mcp_servers ADD COLUMN oauth_token_url TEXT`),
			Raw("add oauth_client_id", `ALTER TABLE mcp_servers ADD COLUMN oauth_client_id TEXT`),
			Raw("add oauth_client_secret", `ALTER TABLE mcp_servers ADD COLUMN oauth_client_secret TEXT`),
			Raw("add oauth_scopes", `ALTER TABLE mcp_servers ADD COLUMN oauth_scopes TEXT`),
			Raw("create oauth_credentials table", `
				CREATE TABLE IF NOT EXISTS oauth_credentials (
					server_name TEXT PRIMARY KEY,
					access_token TEXT NOT NULL,
					refresh_token TEXT,
					token_type TEXT,
					scope TEXT,
					expires_at TIMESTAMP,
					created_at TIMESTAMP NOT NULL,
					updated_at TIMESTAMP NOT NULL,
					FOREIGN KEY(server_name) REFERENCES mcp_servers(name) ON DELETE CASCADE
				)
			`),
			Raw("create oauth_connect_sessions table", `
				CREATE TABLE IF NOT EXISTS oauth_connect_sessions (
					state TEXT PRIMARY KEY,
					server_name TEXT NOT NULL,
					status TEXT NOT NULL,
					code_verifier TEXT,
					redirect_uri TEXT NOT NULL,
					started_at TIMESTAMP NOT NULL,
					completed_at TIMESTAMP,
					error_message TEXT,
					FOREIGN KEY(server_name) REFERENCES mcp_servers(name) ON DELETE CASCADE
				)
			`),
			Raw("create oauth connect sessions index", `
				CREATE INDEX IF NOT EXISTS idx_oauth_connect_sessions_server
					ON oauth_connect_sessions(server_name, started_at DESC)
			`),
		},
	}
}
