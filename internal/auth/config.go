// Package auth implements inbound OAuth bearer-token authentication for
// agent-facing routes. It validates JWTs using OIDC discovery / JWKS and
// extracts an agent identity that the rest of the system uses for policy
// decisions and audit.
package auth

import "strings"

const (
	// AuthTypeOAuthJWT validates bearer tokens as OAuth/OIDC JWTs.
	AuthTypeOAuthJWT = "oauth_jwt"
	// AuthTypeBearerAgentID accepts a UUID bearer token and uses it as the
	// authenticated agent identity. Intended for clients that cannot perform
	// OAuth but can send a static bearer value.
	AuthTypeBearerAgentID = "bearer_agent_id"
)

// DefaultAgentIDClaim is the JWT claim consulted first when extracting the
// agent identity. Falls back to client_id, then azp, then sub.
const DefaultAgentIDClaim = "client_id"

// Config describes one configured authorization server (e.g. one Keycloak
// realm or one Auth0 tenant). Multiple configs are supported.
type Config struct {
	Enabled       bool   `toml:"enabled"`
	Type          string `toml:"type"`
	Issuer        string `toml:"issuer"`
	Audience      string `toml:"audience"`
	JWKSURL       string `toml:"jwks_url"`
	RequiredScope string `toml:"required_scope"`
	AgentIDClaim  string `toml:"agent_id_claim"`
}

// Normalized returns a copy with whitespace trimmed and defaults applied.
// Empty RequiredScope intentionally means no scope claim is required.
func (c Config) Normalized() Config {
	c.Type = strings.ToLower(strings.TrimSpace(c.Type))
	if c.Type == "" {
		c.Type = AuthTypeOAuthJWT
	}
	c.Issuer = strings.TrimRight(strings.TrimSpace(c.Issuer), "/")
	c.Audience = strings.TrimSpace(c.Audience)
	c.JWKSURL = strings.TrimSpace(c.JWKSURL)
	c.RequiredScope = strings.TrimSpace(c.RequiredScope)
	c.AgentIDClaim = strings.TrimSpace(c.AgentIDClaim)
	if c.AgentIDClaim == "" {
		c.AgentIDClaim = DefaultAgentIDClaim
	}
	return c
}
