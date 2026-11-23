package migrations

// migration035 records the working directory a harness reported for an
// externally submitted call.
func migration035() Definition {
	return Definition{
		Version: 35,
		Name:    "035_invocation_cwd",
		Steps: []Step{
			AddColumnIfMissing("invocations", "cwd", "TEXT", "TEXT"),
		},
	}
}
