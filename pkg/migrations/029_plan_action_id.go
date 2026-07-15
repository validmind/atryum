package migrations

func migration029() Definition {
	return Definition{
		Version: 29,
		Name:    "029_plan_action_id",
		Steps: []Step{
			AddColumnIfMissing("invocations", "plan_action_id", "TEXT", "TEXT"),
		},
	}
}
