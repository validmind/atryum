package migrations

// migration032 makes idempotency keys unique per agent instead of globally,
// so one agent's key can never find, or collide with, another agent's
// invocation. A NULL agent_id counts as the empty agent.
//
// ponytail: on Postgres the index is built without CONCURRENTLY (migrations
// run in a transaction), so writes to invocations wait until the build
// finishes. Build it CONCURRENTLY by hand before upgrading if the table is
// large; IF NOT EXISTS then skips this step.
func migration032() Definition {
	return Definition{
		Version: 32,
		Name:    "032_idempotency_per_agent",
		Steps: []Step{
			Raw("create per-agent idempotency index", `
				CREATE UNIQUE INDEX IF NOT EXISTS idx_invocations_agent_idempotency_key
					ON invocations ((COALESCE(agent_id, '')), idempotency_key)
					WHERE idempotency_key IS NOT NULL
			`),
			Raw("drop global idempotency index", `DROP INDEX IF EXISTS idx_invocations_idempotency_key`),
		},
	}
}
