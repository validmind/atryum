package migrations

import "fmt"

func migration030() Definition {
	return Definition{
		Version: 30,
		Name:    "030_agent_ownership",
		Steps: []Step{
			AddColumnIfMissing("invocations", "agent_cuid", "TEXT", "TEXT"),
			AddColumnIfMissing("plans", "agent_cuid", "TEXT", "TEXT"),
			AddColumnIfMissing("external_sessions", "agent_cuid", "TEXT", "TEXT"),
			backfillAgentCUIDFromAgents("invocations"),
			backfillAgentCUIDFromAgents("plans"),
			backfillAgentCUIDFromAgents("external_sessions"),
			Raw("replace global invocation idempotency index", `
				DROP INDEX IF EXISTS idx_invocations_idempotency_key`),
			Raw("scope invocation idempotency by stable agent", `
				CREATE UNIQUE INDEX IF NOT EXISTS idx_invocations_agent_cuid_idempotency_key
					ON invocations(agent_cuid, idempotency_key)
					WHERE agent_cuid IS NOT NULL AND idempotency_key IS NOT NULL`),
			Raw("preserve idempotency for legacy unowned invocations", `
				CREATE UNIQUE INDEX IF NOT EXISTS idx_invocations_unowned_idempotency_key
					ON invocations(idempotency_key)
					WHERE agent_cuid IS NULL AND idempotency_key IS NOT NULL`),
			Raw("index invocations by stable agent", `
				CREATE INDEX IF NOT EXISTS idx_invocations_agent_cuid
					ON invocations(agent_cuid)`),
			Raw("index plans by stable agent", `
				CREATE INDEX IF NOT EXISTS idx_plans_agent_cuid
					ON plans(agent_cuid)`),
			Raw("index external sessions by stable agent", `
				CREATE INDEX IF NOT EXISTS idx_external_sessions_agent_cuid
					ON external_sessions(agent_cuid)`),
		},
	}
}

// backfillAgentCUIDFromAgents populates the new agent_cuid column for rows
// written before this migration, using the same membership test
// AgentsRepo.GetByAgentID uses against agents.agent_ids. Without this,
// pre-existing plans/invocations/sessions would look "unowned" under the new
// column — e.g. matchApprovedPlan's stable-CUID lookup would find nothing for
// an already-approved plan and silently fall through to normal gating.
//
// table is a compile-time constant supplied by call sites below (not user
// input), so building the UPDATE with fmt.Sprintf is safe — same rationale as
// AddColumnIfMissing.
func backfillAgentCUIDFromAgents(table string) Step {
	sqliteQuery := fmt.Sprintf(`
		UPDATE %s SET agent_cuid = (
			SELECT a.vm_cuid FROM agents a
			WHERE a.vm_cuid IS NOT NULL AND a.vm_cuid <> ''
				AND EXISTS (
					SELECT 1 FROM json_each(a.agent_ids) je WHERE je.value = %s.agent_id
				)
			LIMIT 1
		)
		WHERE agent_cuid IS NULL AND agent_id IS NOT NULL AND agent_id <> ''`, table, table)
	postgresQuery := fmt.Sprintf(`
		UPDATE %s AS t SET agent_cuid = a.vm_cuid
		FROM agents a
		WHERE t.agent_cuid IS NULL
			AND t.agent_id IS NOT NULL AND t.agent_id <> ''
			AND a.vm_cuid IS NOT NULL AND a.vm_cuid <> ''
			AND a.agent_ids @> to_jsonb(t.agent_id::text)::jsonb`, table)
	return RawDialect(fmt.Sprintf("backfill %s.agent_cuid from agents", table), sqliteQuery, postgresQuery)
}
