// Package auth implements inbound OAuth bearer-token authentication for
// agent-facing routes. It validates JWTs using OIDC discovery / JWKS and
// extracts an agent identity that the rest of the system uses for policy
// decisions and audit.
package auth

import "strings"

// DefaultRequiredScope is the OAuth scope required on incoming MCP requests
// when [auth].required_scope is not configured.
const DefaultRequiredScope = "atryum:mcp"

// DefaultAgentIDClaim is the JWT claim consulted first when extracting the
// agent identity. Falls back to client_id, then azp, then sub.
const DefaultAgentIDClaim = "client_id"

// Config describes one configured authorization server (e.g. one Keycloak
// realm or one Auth0 tenant). Multiple configs are supported.
type Config struct {
	Enabled       bool   `toml:"enabled"`
	Issuer        string `toml:"issuer"`
	Audience      string `toml:"audience"`
	JWKSURL       string `toml:"jwks_url"`
	RequiredScope string `toml:"required_scope"`
	AgentIDClaim  string `toml:"agent_id_claim"`
}

// Normalized returns a copy with whitespace trimmed and defaults applied.
func (c Config) Normalized() Config {
	c.Issuer = strings.TrimRight(strings.TrimSpace(c.Issuer), "/")
	c.Audience = strings.TrimSpace(c.Audience)
	c.JWKSURL = strings.TrimSpace(c.JWKSURL)
	c.RequiredScope = strings.TrimSpace(c.RequiredScope)
	c.AgentIDClaim = strings.TrimSpace(c.AgentIDClaim)
	if c.RequiredScope == "" {
		c.RequiredScope = DefaultRequiredScope
	}
	if c.AgentIDClaim == "" {
		c.AgentIDClaim = DefaultAgentIDClaim
	}
	return c
}
