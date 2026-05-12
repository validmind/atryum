package migrations

func migration005() Definition {
	return Definition{
		Version: 5,
		Name:    "005_matched_rule_id.sql",
		Steps: []Step{
			Raw("add matched_rule_id to invocations", `ALTER TABLE invocations ADD COLUMN matched_rule_id TEXT REFERENCES approval_rules(id) ON DELETE SET NULL`),
		},
	}
}
