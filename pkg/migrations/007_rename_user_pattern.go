package migrations

func migration007() Definition {
	return Definition{
		Version: 7,
		Name:    "007_rename_user_pattern.sql",
		Steps: []Step{
			// Rename approval_rules.user_pattern -> agent_id_pattern.
			// SQLite supports RENAME COLUMN since 3.25.0; Postgres natively.
			Raw("rename approval_rules.user_pattern to agent_id_pattern",
				`ALTER TABLE approval_rules RENAME COLUMN user_pattern TO agent_id_pattern`),
		},
	}
}
