package migrations

// migration033 records how the caller of each invocation authenticated:
// issuer, method (jwt, api_key or no_auth) and the API key's id.
func migration033() Definition {
	return Definition{
		Version: 33,
		Name:    "033_invocation_auth",
		Steps: []Step{
			AddColumnIfMissing("invocations", "auth_issuer", "TEXT", "TEXT"),
			AddColumnIfMissing("invocations", "auth_method", "TEXT", "TEXT"),
			AddColumnIfMissing("invocations", "auth_key_id", "TEXT", "TEXT"),
		},
	}
}
