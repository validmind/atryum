package migrations

// migration035 lets an MCP gateway vouch for the human (and sub-agent) a tool
// call is made for, and records what it vouched for.
//
//   - api_keys.capabilities: a comma-separated list of extra rights a key
//     holds. "asserter" lets the key call /api/v1/gateway/* and assert who a
//     call is for. Plain TEXT (not an array) so SQLite and Postgres agree.
//   - api_keys.gateway_vendor: which gateway the key belongs to (truefoundry,
//     kuadrant, generic). Display only.
//   - invocations.asserted_*: the identity a gateway asserted, and which
//     gateway (agent) asserted it. NULL on every other path.
//   - invocations.identity_basis: "asserted" for gateway calls; NULL means the
//     caller's own credential was the only identity (verified).
//   - invocations.gateway_check_key: hash of (tenant, gateway agent, user,
//     server, tool, canonical arguments), so a repeat of a held call finds
//     the open review instead of opening another.
func migration035() Definition {
	return Definition{
		Version: 35,
		Name:    "035_gateway_asserter",
		Steps: []Step{
			AddColumnIfMissing("api_keys", "capabilities", "TEXT NOT NULL DEFAULT ''", "TEXT NOT NULL DEFAULT ''"),
			AddColumnIfMissing("api_keys", "gateway_vendor", "TEXT", "TEXT"),
			AddColumnIfMissing("invocations", "asserted_user_id", "TEXT", "TEXT"),
			AddColumnIfMissing("invocations", "asserted_user_type", "TEXT", "TEXT"),
			AddColumnIfMissing("invocations", "asserted_user_display", "TEXT", "TEXT"),
			AddColumnIfMissing("invocations", "asserted_agent_id", "TEXT", "TEXT"),
			AddColumnIfMissing("invocations", "asserted_session_id", "TEXT", "TEXT"),
			AddColumnIfMissing("invocations", "asserted_by", "TEXT", "TEXT"),
			AddColumnIfMissing("invocations", "identity_basis", "TEXT", "TEXT"),
			AddColumnIfMissing("invocations", "gateway_check_key", "TEXT", "TEXT"),
			RawDialect("index invocations by gateway check key",
				`CREATE INDEX IF NOT EXISTS idx_invocations_gateway_check_key ON invocations(gateway_check_key, status)`,
				`CREATE INDEX IF NOT EXISTS idx_invocations_gateway_check_key ON invocations(gateway_check_key, status)`),
		},
	}
}
