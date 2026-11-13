package migrations

// migration036 records the end user an application (a credential with the
// "app" capability) makes a call for, on top of 035's asserted identity.
//
//   - invocations.asserted_user_email, asserted_roles (a JSON array, as TEXT
//     so SQLite and Postgres agree), asserted_eu_tenant (the end user's
//     organization inside the application), asserted_app_agent and
//     asserted_kind ("gateway" or "app").
//   - invocations.asserted_user_key: sha256 of (asserting credential's agent,
//     end-user tenant, end-user id). It scopes idempotency keys to one end
//     user, so two end users of one application never collide, and finds an
//     end user's rows when they are redacted.
//
// The per-agent idempotency index (032, scoped by tenant in 034) becomes per
// tenant, agent and end user. A
// row without an asserted user has an empty key, so the old behaviour is
// unchanged for every other caller.
//
// ponytail: like 032, the index is built without CONCURRENTLY; build it by
// hand first on a large table (IF NOT EXISTS then skips the step).
func migration036() Definition {
	return Definition{
		Version: 36,
		Name:    "036_app_end_user",
		Steps: []Step{
			AddColumnIfMissing("invocations", "asserted_user_email", "TEXT", "TEXT"),
			AddColumnIfMissing("invocations", "asserted_roles", "TEXT", "TEXT"),
			AddColumnIfMissing("invocations", "asserted_eu_tenant", "TEXT", "TEXT"),
			AddColumnIfMissing("invocations", "asserted_app_agent", "TEXT", "TEXT"),
			AddColumnIfMissing("invocations", "asserted_kind", "TEXT", "TEXT"),
			AddColumnIfMissing("invocations", "asserted_user_key", "TEXT", "TEXT"),
			Raw("create per-agent, per-end-user idempotency index", `
				CREATE UNIQUE INDEX IF NOT EXISTS idx_invocations_agent_user_idempotency_key
					ON invocations (tenant_id, (COALESCE(agent_id, '')), (COALESCE(asserted_user_key, '')), idempotency_key)
					WHERE idempotency_key IS NOT NULL
			`),
			Raw("drop per-agent idempotency index", `DROP INDEX IF EXISTS idx_invocations_agent_idempotency_key`),
			Raw("index invocations by asserted user key", `
				CREATE INDEX IF NOT EXISTS idx_invocations_asserted_user_key
					ON invocations (tenant_id, asserted_user_key)
					WHERE asserted_user_key IS NOT NULL
			`),
		},
	}
}
