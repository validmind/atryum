package migrations

func migration007() Definition {
	return Definition{
		Version: 7,
		Name:    "007_invocation_claude_fields.sql",
		Steps: []Step{
			Raw("add claude_session_id to invocations",
				`ALTER TABLE invocations ADD COLUMN claude_session_id TEXT`),
			Raw("add claude_tool_use_event_id to invocations",
				`ALTER TABLE invocations ADD COLUMN claude_tool_use_event_id TEXT`),
		},
	}
}
