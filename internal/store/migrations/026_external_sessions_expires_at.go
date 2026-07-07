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
			//
			// AddColumnIfMissing rather than a bare ADD COLUMN: this project has
			// a live example of why. A rebase inserted main's
			// 024_server_endpoint_slug ahead of this branch's session
			// migrations, shifting them from 024/025 to 025/026. A dev database
			// stamped under the old numbering had already run this ALTER as
			// part of what was then "025", then re-ran it as "026" under the
			// new numbering and hit "column already exists" (Postgres
			// SQLSTATE 42701). Guarding on the column's actual presence makes
			// the step safe regardless of which version number it runs under.
			AddColumnIfMissing("external_sessions", "expires_at", "TIMESTAMP", "TIMESTAMPTZ"),
		},
	}
}
