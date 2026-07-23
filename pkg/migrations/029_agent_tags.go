package migrations

func migration029() Definition {
	return Definition{
		Version: 29,
		Name:    "029_agent_tags",
		Steps: []Step{
			// AddColumnIfMissing (not a bare ADD COLUMN): migrations on this
			// shared sequence have been renumbered by rebases before, so a
			// long-lived database can hit this ALTER a second time under a
			// different version stamp. See AddColumnIfMissing's doc comment.
			// tags is a JSON-encoded array of strings ("[]" default), managed
			// by Atryum and preserved across VM syncs (see AgentsRepo.Upsert).
			AddColumnIfMissing("agents", "tags",
				"TEXT NOT NULL DEFAULT '[]'",
				"TEXT NOT NULL DEFAULT '[]'",
			),
		},
	}
}
