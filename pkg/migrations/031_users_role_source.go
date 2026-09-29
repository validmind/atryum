package migrations

// migration031 records where a user's role came from. UpsertLogin refreshes
// role from the IdP admin claim on every login, which silently undid any
// promotion or demotion an operator made through PATCH /api/v1/users/{id}.
// role_source is 'idp' (the default: the claim stays authoritative) or
// 'manual' (an operator set it; logins leave it alone from then on).
func migration031() Definition {
	return Definition{
		Version: 31,
		Name:    "031_users_role_source",
		Steps: []Step{
			// AddColumnIfMissing for the same reason as 029: this shared
			// sequence has been renumbered by rebases before.
			AddColumnIfMissing("users", "role_source",
				"TEXT NOT NULL DEFAULT 'idp'",
				"TEXT NOT NULL DEFAULT 'idp'",
			),
		},
	}
}
