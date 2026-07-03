package migrations

func migration026() Definition {
	return Definition{
		Version: 26,
		Name:    "026_external_sessions_expires_at",
		Steps: []Step{
			// Added as a follow-up to 025 rather than editing it in place: any DB
			// that already ran 025 is stamped at version 25 and would never pick up
			// an in-place change. Existing rows backfill to NULL, which
			// lookupSessionForAgent treats as non-expiring (IsZero guard); the
			// service sets a concrete expires_at on every new/touched session.
			RawDialect("add expires_at to external_sessions",
				`ALTER TABLE external_sessions ADD COLUMN expires_at TIMESTAMP`,
				`ALTER TABLE external_sessions ADD COLUMN expires_at TIMESTAMPTZ`,
			),
		},
	}
}
