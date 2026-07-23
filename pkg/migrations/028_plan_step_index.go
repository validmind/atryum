package migrations

func migration028() Definition {
	return Definition{
		Version: 28,
		Name:    "028_plan_step_index",
		Steps: []Step{
			AddColumnIfMissing("invocations", "plan_step_index", "INTEGER", "INTEGER"),
			Raw("create plan execution order index", `
				CREATE INDEX IF NOT EXISTS idx_invocations_plan_step
				ON invocations(plan_id, plan_step_index)
			`),
		},
	}
}
