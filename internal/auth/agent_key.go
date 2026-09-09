package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"
)

// AgentKeyPrefix marks a first-party Atryum API key. The runtime middleware
// dispatches on it: tokens with this prefix are looked up in api_keys, every
// other bearer is treated as an IdP JWT. The prefix also lets secret scanners
// recognise leaked keys.
const AgentKeyPrefix = "atr_"

// agentKeyRandomBytes is the entropy per key (256 bits). Keys are random, so a
// plain SHA-256 (not a slow password hash) is sufficient at rest.
const agentKeyRandomBytes = 32

// agentKeyDisplayLen is how much of the token is kept in plaintext for display
// ("atr_" plus 8 characters).
const agentKeyDisplayLen = len(AgentKeyPrefix) + 8

// GenerateAgentKey returns a new random token, its hash for storage, and the
// short prefix to show in listings. The token is shown to the user once.
func GenerateAgentKey() (token, hash, prefix string, err error) {
	buf := make([]byte, agentKeyRandomBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", "", "", err
	}
	token = AgentKeyPrefix + base64.RawURLEncoding.EncodeToString(buf)
	return token, HashAgentKey(token), AgentKeyDisplayPrefix(token), nil
}

// HashAgentKey returns the hex SHA-256 of a token; this is what api_keys stores.
func HashAgentKey(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(sum[:])
}

// AgentKeyDisplayPrefix returns the non-secret leading part of a token.
func AgentKeyDisplayPrefix(token string) string {
	if len(token) <= agentKeyDisplayLen {
		return token
	}
	return token[:agentKeyDisplayLen]
}

// IsAgentKey reports whether a bearer token is an Atryum API key rather than
// an IdP-issued JWT.
func IsAgentKey(token string) bool {
	return strings.HasPrefix(strings.TrimSpace(token), AgentKeyPrefix)
}

// AgentKeyResolver turns a presented API key into an agent Identity. It is
// implemented over the api_keys store (see pkg/atryum) and returns an error
// for unknown, revoked, expired, or creator-disabled keys.
type AgentKeyResolver interface {
	ResolveAgentKey(ctx context.Context, token string) (Identity, error)
}

// Identity.Method values.
const (
	IdentityMethodJWT    = "jwt"
	IdentityMethodAPIKey = "api_key"
	IdentityMethodNoAuth = "no_auth"
)

// APIKeyIssuer is the Identity.Issuer used for key-authenticated agents.
const APIKeyIssuer = "atryum:api-key"
