package migrations

func migration012() Definition {
	return Definition{
		Version: 12,
		Name:    "012_invocation_summary",
		Steps: []Step{
			Raw("add summary to invocations", `ALTER TABLE invocations ADD COLUMN summary TEXT`),
		},
	}
}
