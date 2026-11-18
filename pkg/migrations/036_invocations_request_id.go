package migrations

// migration036 indexes invocations by request_id. On the gateway's LLM path
// the request ID is the model's own ID for a tool call (toolu_…, call_…), and
// the tool's result, which arrives in the agent's next request, is attached
// to its call by that ID: one lookup per tool result.
func migration036() Definition {
	return Definition{
		Version: 36,
		Name:    "036_invocations_request_id",
		Steps: []Step{
			RawDialect("index invocations by request ID",
				`CREATE INDEX IF NOT EXISTS idx_invocations_request_id ON invocations(request_id) WHERE request_id IS NOT NULL`,
				`CREATE INDEX IF NOT EXISTS idx_invocations_request_id ON invocations(request_id) WHERE request_id IS NOT NULL`),
		},
	}
}
