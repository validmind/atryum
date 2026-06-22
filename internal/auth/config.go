// Package auth implements inbound OAuth bearer-token authentication for
// agent-facing routes. It validates JWTs using OIDC discovery / JWKS and
// extracts an agent identity that the rest of the system uses for policy
// decisions and audit.
package auth

import "strings"

// DefaultAgentIDClaim is the JWT claim consulted first when extracting the
// agent identity. Falls back to client_id, then azp, then sub.
const DefaultAgentIDClaim = "client_id"

const (
	DefaultAdminProvider   = "auth0"
	DefaultAdminScopes     = "openid profile email offline_access"
	DefaultAdminClaim      = "atryum_admin"
	DefaultAdminClaimValue = "true"
)

// Config describes one configured authorization server (e.g. one Keycloak
// realm or one Auth0 tenant). Multiple configs are supported.
type Config struct {
	Enabled         bool   `toml:"enabled"`
	Issuer          string `toml:"issuer"`
	Audience        string `toml:"audience"`
	JWKSURL         string `toml:"jwks_url"`
	RequiredScope   string `toml:"required_scope"`
	AgentIDClaim    string `toml:"agent_id_claim"`
	AdminEnabled    bool   `toml:"admin_enabled"`
	AdminProvider   string `toml:"admin_provider"`
	AdminClientID   string `toml:"admin_client_id"`
	AdminScopes     string `toml:"admin_scopes"`
	AdminClaim      string `toml:"admin_claim"`
	AdminClaimValue string `toml:"admin_claim_value"`
}

// Normalized returns a copy with whitespace trimmed and defaults applied.
// Empty RequiredScope intentionally means no scope claim is required.
func (c Config) Normalized() Config {
	c.Issuer = strings.TrimRight(strings.TrimSpace(c.Issuer), "/")
	c.Audience = strings.TrimSpace(c.Audience)
	c.JWKSURL = strings.TrimSpace(c.JWKSURL)
	c.RequiredScope = strings.TrimSpace(c.RequiredScope)
	c.AgentIDClaim = strings.TrimSpace(c.AgentIDClaim)
	if c.AgentIDClaim == "" {
		c.AgentIDClaim = DefaultAgentIDClaim
	}
	c.AdminProvider = strings.TrimSpace(c.AdminProvider)
	if c.AdminProvider == "" {
		c.AdminProvider = DefaultAdminProvider
	}
	c.AdminClientID = strings.TrimSpace(c.AdminClientID)
	c.AdminScopes = strings.TrimSpace(c.AdminScopes)
	if c.AdminScopes == "" {
		c.AdminScopes = DefaultAdminScopes
	}
	c.AdminClaim = strings.TrimSpace(c.AdminClaim)
	if c.AdminClaim == "" {
		c.AdminClaim = DefaultAdminClaim
	}
	c.AdminClaimValue = strings.TrimSpace(c.AdminClaimValue)
	if c.AdminClaimValue == "" {
		c.AdminClaimValue = DefaultAdminClaimValue
	}
	return c
}
