// Package auth implements inbound OAuth bearer-token authentication for
// agent-facing routes. It validates JWTs using OIDC discovery / JWKS and
// extracts an agent identity that the rest of the system uses for policy
// decisions and audit.
package auth

import (
	"fmt"
	"strings"
)

// DefaultAgentIDClaim is the JWT claim consulted first when extracting the
// agent identity. Falls back to client_id, then azp, then sub.
const DefaultAgentIDClaim = "client_id"

const (
	DefaultAdminProvider   = "keycloak"
	DefaultAdminScopes     = "openid profile email offline_access"
	DefaultAdminClaim      = "atryum_admin"
	DefaultAdminClaimValue = "true"
)

// Config describes one configured authorization server (e.g. one Keycloak
// realm or one Auth0 tenant). Multiple configs are supported.
type Config struct {
	Enabled       bool   `toml:"enabled"`
	Issuer        string `toml:"issuer"`
	Audience      string `toml:"audience"`
	JWKSURL       string `toml:"jwks_url"`
	RequiredScope string `toml:"required_scope"`
	AgentIDClaim  string `toml:"agent_id_claim"`
	AdminEnabled  bool   `toml:"admin_enabled"`
	AdminProvider string `toml:"admin_provider"`
	AdminClientID string `toml:"admin_client_id"`
	// CLIClientID is the OAuth client the `atryum` CLI uses for the device
	// authorization grant. Optional: defaults to AdminClientID, which works
	// for IdPs that allow the device grant on a public browser client
	// (Keycloak). Auth0 only permits the device grant on Native applications,
	// so there it must be a separate client.
	CLIClientID string `toml:"cli_client_id"`
	// EmailClaims and NameClaims are consulted in order to fill the user's
	// display fields from a verified token. Defaults cover the common IdPs
	// (standard OIDC claims, Entra's upn/unique_name/preferred_username); add
	// a namespaced claim here for IdPs whose access tokens omit them (Auth0
	// with an Action such as "https://atryum.dev/email").
	EmailClaims []string `toml:"email_claims"`
	NameClaims  []string `toml:"name_claims"`
	// UserInfo, when true (the default), lets Atryum call the issuer's OIDC
	// userinfo endpoint with the presented token to fill in email/name that
	// the token itself lacks. Set false for IdPs whose userinfo endpoint does
	// not accept API-audience tokens (Entra) or to avoid the extra call.
	UserInfo        *bool      `toml:"userinfo"`
	AdminScopes     string     `toml:"admin_scopes"`
	AdminClaim      string     `toml:"admin_claim"`
	AdminClaimValue ClaimValue `toml:"admin_claim_value"`
}

type ClaimValue string

func (v *ClaimValue) UnmarshalTOML(value any) error {
	switch t := value.(type) {
	case string:
		*v = ClaimValue(strings.TrimSpace(t))
	case bool:
		*v = ClaimValue(fmt.Sprintf("%t", t))
	case int64:
		*v = ClaimValue(fmt.Sprintf("%d", t))
	default:
		return fmt.Errorf("admin_claim_value must be a string, bool, or integer")
	}
	return nil
}

// DefaultEmailClaims and DefaultNameClaims are the claim lookup orders used
// when a [[auth]] block does not override them.
var (
	DefaultEmailClaims = []string{"email", "upn", "unique_name", "preferred_username"}
	DefaultNameClaims  = []string{"name", "preferred_username", "nickname"}
)

// UserInfoEnabled reports whether the userinfo fallback is on (default true).
func (c Config) UserInfoEnabled() bool {
	return c.UserInfo == nil || *c.UserInfo
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
	c.CLIClientID = strings.TrimSpace(c.CLIClientID)
	if c.CLIClientID == "" {
		c.CLIClientID = c.AdminClientID
	}
	c.EmailClaims = normalizeClaimList(c.EmailClaims, DefaultEmailClaims)
	c.NameClaims = normalizeClaimList(c.NameClaims, DefaultNameClaims)
	c.AdminScopes = strings.TrimSpace(c.AdminScopes)
	if c.AdminScopes == "" {
		c.AdminScopes = DefaultAdminScopes
	}
	c.AdminClaim = strings.TrimSpace(c.AdminClaim)
	if c.AdminClaim == "" {
		c.AdminClaim = DefaultAdminClaim
	}
	c.AdminClaimValue = ClaimValue(strings.TrimSpace(string(c.AdminClaimValue)))
	if c.AdminClaimValue == "" {
		c.AdminClaimValue = DefaultAdminClaimValue
	}
	return c
}

func normalizeClaimList(in, defaults []string) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return append([]string(nil), defaults...)
	}
	return out
}
