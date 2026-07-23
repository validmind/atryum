package migrations

func migration001() Definition {
	return Definition{
		Version: 1,
		Name:    "001_init_schema.sql",
		Steps: []Step{
			Raw("create invocations table", `
				CREATE TABLE IF NOT EXISTS invocations (
					invocation_id TEXT PRIMARY KEY,
					request_id TEXT,
					idempotency_key TEXT,
					tool_name TEXT NOT NULL,
					upstream_name TEXT NOT NULL,
					status TEXT NOT NULL,
					approval_json TEXT,
					request_json TEXT NOT NULL,
					response_json TEXT,
					error_json TEXT,
					submitted_at TIMESTAMP NOT NULL,
					completed_at TIMESTAMP
				)
			`),
			Raw("create invocations idempotency index", `
				CREATE UNIQUE INDEX IF NOT EXISTS idx_invocations_idempotency_key
					ON invocations(idempotency_key) WHERE idempotency_key IS NOT NULL
			`),
			Raw("create invocations submitted_at index", `
				CREATE INDEX IF NOT EXISTS idx_invocations_submitted_at
					ON invocations(submitted_at DESC)
			`),
			Raw("create invocations lookup index", `
				CREATE INDEX IF NOT EXISTS idx_invocations_upstream_tool_status
					ON invocations(upstream_name, tool_name, status)
			`),
			RawDialect("create invocation_events table", `
				CREATE TABLE IF NOT EXISTS invocation_events (
					id INTEGER PRIMARY KEY AUTOINCREMENT,
					invocation_id TEXT NOT NULL,
					event_type TEXT NOT NULL,
					payload_json TEXT NOT NULL,
					created_at TIMESTAMP NOT NULL,
					FOREIGN KEY(invocation_id) REFERENCES invocations(invocation_id)
				)
			`, `
				CREATE TABLE IF NOT EXISTS invocation_events (
					id BIGSERIAL PRIMARY KEY,
					invocation_id TEXT NOT NULL,
					event_type TEXT NOT NULL,
					payload_json TEXT NOT NULL,
					created_at TIMESTAMP NOT NULL,
					FOREIGN KEY(invocation_id) REFERENCES invocations(invocation_id)
				)
			`),
			Raw("create invocation_events lookup index", `
				CREATE INDEX IF NOT EXISTS idx_invocation_events_lookup
					ON invocation_events(invocation_id, id)
			`),
			Raw("create mcp_servers table", `
				CREATE TABLE IF NOT EXISTS mcp_servers (
					name TEXT PRIMARY KEY,
					mode TEXT NOT NULL,
					base_url TEXT,
					auth_token TEXT,
					timeout_seconds INTEGER NOT NULL DEFAULT 30,
					command TEXT,
					args_json TEXT NOT NULL DEFAULT '[]',
					env_json TEXT NOT NULL DEFAULT '{}',
					enabled INTEGER NOT NULL DEFAULT 1,
					created_at TIMESTAMP NOT NULL,
					updated_at TIMESTAMP NOT NULL
				)
			`),
			Raw("create mcp_servers enabled index", `
				CREATE INDEX IF NOT EXISTS idx_mcp_servers_enabled_name
					ON mcp_servers(enabled, name)
			`),
		},
	}
}
