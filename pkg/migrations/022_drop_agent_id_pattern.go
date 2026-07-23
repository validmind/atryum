package migrations

func migration022() Definition {
	return Definition{
		Version: 22,
		Name:    "022_drop_agent_id_pattern",
		Steps: []Step{
			Raw("drop approval_rules.agent_id_pattern column",
				`ALTER TABLE approval_rules DROP COLUMN agent_id_pattern`),
		},
	}
}
