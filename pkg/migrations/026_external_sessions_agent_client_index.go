package migrations

func migration026() Definition {
	return Definition{
		Version: 26,
		Name:    "026_external_sessions_agent_client_index",
		Steps: []Step{
			// Backs Service.getOrCreateSession's lookup on (agent binding,
			// client_session_id). CREATE INDEX IF NOT EXISTS is idempotent, so
			// this step is safe to re-run under a shifted migration number (see
			// AddColumnIfMissing's doc comment for why that matters here).
			Raw("index external_sessions by (agent_id, client_session_id)",
				`CREATE INDEX IF NOT EXISTS idx_external_sessions_agent_client ON external_sessions (agent_id, client_session_id)`),
		},
	}
}
