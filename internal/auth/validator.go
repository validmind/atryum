package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	jwt "github.com/golang-jwt/jwt/v5"
)

// Validator validates incoming bearer tokens against one or more configured
// authorization servers (e.g. Keycloak, Auth0). It is safe for concurrent use.
type Validator struct {
	configs []Config
	caches  map[string]*keyCache // by issuer
	client  *http.Client
	mu      sync.Mutex
	now     func() time.Time
}

// Identity is the authenticated agent extracted from a valid token.
type Identity struct {
	AgentID string
	Issuer  string
	Subject string
	Scope   string
}

// Result classifies a validation outcome.
type Result int

const (
	ResultOK           Result = iota
	ResultMissing             // No Authorization header
	ResultInvalid             // Bad signature, malformed, wrong issuer/audience, expired, etc.
	ResultMissingScope        // Token is otherwise valid but lacks required scope
)

// ValidationError carries a Result plus a human-readable description that
// callers may include in WWW-Authenticate / response bodies. RequiredScope
// is set when the validator matched the token to a specific [[auth]] config
// and that config has a required_scope; middleware advertises it in the
// WWW-Authenticate `scope=` parameter.
type ValidationError struct {
	Result        Result
	Description   string
	RequiredScope string
}

func (e *ValidationError) Error() string { return e.Description }

// NewValidator returns a Validator for the given enabled configs. Empty input
// or all-disabled input returns a nil validator (callers may treat that as
// "auth disabled").
func NewValidator(configs []Config, client *http.Client) (*Validator, error) {
	enabled := make([]Config, 0, len(configs))
	for _, c := range configs {
		c = c.Normalized()
		if !c.Enabled {
			continue
		}
		if c.Issuer == "" {
			return nil, fmt.Errorf("auth: issuer is required")
		}
		if c.Audience == "" {
			return nil, fmt.Errorf("auth: audience is required (issuer=%s)", c.Issuer)
		}
		enabled = append(enabled, c)
	}
	if len(enabled) == 0 {
		return nil, nil
	}
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &Validator{configs: enabled, caches: map[string]*keyCache{}, client: client, now: time.Now}, nil
}

// Configs returns the active validator configs (read-only view).
func (v *Validator) Configs() []Config {
	if v == nil {
		return nil
	}
	out := make([]Config, len(v.configs))
	copy(out, v.configs)
	return out
}

// DebugIdentity parses a bearer JWT without verifying signature, issuer,
// audience, expiry, or scope. It exists only for explicit local debugging.
func (v *Validator) DebugIdentity(bearer string) (Identity, error) {
	bearer = strings.TrimSpace(bearer)
	if bearer == "" {
		return Identity{}, &ValidationError{Result: ResultMissing, Description: "missing bearer token"}
	}
	parser := jwt.NewParser(jwt.WithoutClaimsValidation())
	unverified, _, err := parser.ParseUnverified(bearer, jwt.MapClaims{})
	if err != nil {
		return Identity{}, &ValidationError{Result: ResultInvalid, Description: "malformed token"}
	}
	claims, ok := unverified.Claims.(jwt.MapClaims)
	if !ok {
		return Identity{}, &ValidationError{Result: ResultInvalid, Description: "malformed claims"}
	}
	agentIDClaim := DefaultAgentIDClaim
	if v != nil {
		for _, c := range v.configs {
			if c.AgentIDClaim != "" {
				agentIDClaim = c.AgentIDClaim
				break
			}
		}
	}
	identity := Identity{
		AgentID: extractAgentID(claims, agentIDClaim),
		Issuer:  strings.TrimRight(strings.TrimSpace(stringClaim(claims, "iss")), "/"),
		Subject: stringClaim(claims, "sub"),
		Scope:   tokenScope(claims),
	}
	if identity.AgentID == "" {
		return Identity{}, &ValidationError{Result: ResultInvalid, Description: "no usable agent identity claim"}
	}
	return identity, nil
}

// Validate parses, verifies, and inspects the supplied bearer token. It
// returns the extracted Identity on success. On failure the returned error is
// always a *ValidationError so middleware can map it to 401/403.
func (v *Validator) Validate(ctx context.Context, bearer string) (Identity, error) {
	bearer = strings.TrimSpace(bearer)
	if bearer == "" {
		return Identity{}, &ValidationError{Result: ResultMissing, Description: "missing bearer token"}
	}

	// Pre-parse (unverified) just to determine which configured issuer this
	// token claims to come from. Signature is still verified below.
	parser := jwt.NewParser(jwt.WithoutClaimsValidation())
	unverified, _, err := parser.ParseUnverified(bearer, jwt.MapClaims{})
	if err != nil {
		return Identity{}, &ValidationError{Result: ResultInvalid, Description: "malformed token"}
	}
	claims, ok := unverified.Claims.(jwt.MapClaims)
	if !ok {
		return Identity{}, &ValidationError{Result: ResultInvalid, Description: "malformed claims"}
	}
	iss, _ := claims["iss"].(string)
	iss = strings.TrimRight(strings.TrimSpace(iss), "/")
	cfg, ok := v.matchConfig(iss, claims["aud"])
	if !ok {
		return Identity{}, &ValidationError{Result: ResultInvalid, Description: "issuer/audience not allowed"}
	}

	cache, err := v.cacheFor(ctx, cfg)
	if err != nil {
		return Identity{}, &ValidationError{Result: ResultInvalid, Description: "cannot fetch signing keys"}
	}

	// Issuer is already verified via matchConfig (after trailing-slash
	// normalization), so we don't pass jwt.WithIssuer here — different IdPs
	// disagree about the trailing slash and the strict comparator would
	// reject otherwise-valid tokens. Audience, expiry, and not-before are
	// validated by the JWT library below.
	verified, err := jwt.ParseWithClaims(bearer, jwt.MapClaims{}, func(t *jwt.Token) (any, error) {
		if _, isRSA := t.Method.(*jwt.SigningMethodRSA); !isRSA {
			return nil, fmt.Errorf("unexpected signing method %v", t.Header["alg"])
		}
		kid, _ := t.Header["kid"].(string)
		if kid == "" {
			return nil, fmt.Errorf("missing kid")
		}
		return cache.Get(ctx, kid)
	}, jwt.WithValidMethods([]string{"RS256", "RS384", "RS512"}), jwt.WithAudience(cfg.Audience), jwt.WithExpirationRequired(), jwt.WithTimeFunc(v.now))
	if err != nil {
		switch {
		case errors.Is(err, jwt.ErrTokenExpired):
			return Identity{}, &ValidationError{Result: ResultInvalid, Description: "token expired"}
		case errors.Is(err, jwt.ErrTokenNotValidYet):
			return Identity{}, &ValidationError{Result: ResultInvalid, Description: "token not yet valid"}
		case errors.Is(err, jwt.ErrTokenInvalidIssuer):
			return Identity{}, &ValidationError{Result: ResultInvalid, Description: "invalid issuer"}
		case errors.Is(err, jwt.ErrTokenInvalidAudience):
			return Identity{}, &ValidationError{Result: ResultInvalid, Description: "invalid audience"}
		case errors.Is(err, jwt.ErrTokenSignatureInvalid):
			return Identity{}, &ValidationError{Result: ResultInvalid, Description: "invalid signature"}
		default:
			return Identity{}, &ValidationError{Result: ResultInvalid, Description: "invalid token"}
		}
	}
	verifiedClaims, _ := verified.Claims.(jwt.MapClaims)

	scope := tokenScope(verifiedClaims)
	if !scopeContains(scope, cfg.RequiredScope) {
		return Identity{}, &ValidationError{Result: ResultMissingScope, Description: "missing required scope", RequiredScope: cfg.RequiredScope}
	}

	identity := Identity{
		AgentID: extractAgentID(verifiedClaims, cfg.AgentIDClaim),
		Issuer:  cfg.Issuer,
		Subject: stringClaim(verifiedClaims, "sub"),
		Scope:   scope,
	}
	if identity.AgentID == "" {
		return Identity{}, &ValidationError{Result: ResultInvalid, Description: "no usable agent identity claim"}
	}
	return identity, nil
}

func (v *Validator) matchConfig(issuer string, audienceClaim any) (Config, bool) {
	for _, c := range v.configs {
		if c.Issuer == issuer && audienceMatches(audienceClaim, c.Audience) {
			return c, true
		}
	}
	return Config{}, false
}

func audienceMatches(claim any, audience string) bool {
	switch v := claim.(type) {
	case string:
		return v == audience
	case []string:
		for _, item := range v {
			if item == audience {
				return true
			}
		}
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok && s == audience {
				return true
			}
		}
	}
	return false
}

func (v *Validator) cacheFor(ctx context.Context, cfg Config) (*keyCache, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if c, ok := v.caches[cfg.Issuer]; ok {
		return c, nil
	}
	jwksURL := cfg.JWKSURL
	if jwksURL == "" {
		discovered, err := discoverJWKSURL(ctx, v.client, cfg.Issuer)
		if err != nil {
			return nil, err
		}
		jwksURL = discovered
	}
	c := newKeyCache(jwksURL, v.client, 10*time.Minute)
	v.caches[cfg.Issuer] = c
	return c, nil
}

// discoverJWKSURL fetches the OIDC discovery document and returns jwks_uri.
func discoverJWKSURL(ctx context.Context, client *http.Client, issuer string) (string, error) {
	url := strings.TrimRight(issuer, "/") + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("oidc discovery: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("oidc discovery: status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	var doc struct {
		JWKSURI string `json:"jwks_uri"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return "", fmt.Errorf("oidc discovery decode: %w", err)
	}
	if doc.JWKSURI == "" {
		return "", fmt.Errorf("oidc discovery: jwks_uri missing")
	}
	return doc.JWKSURI, nil
}

func tokenScope(claims jwt.MapClaims) string {
	if s, ok := claims["scope"].(string); ok {
		return s
	}
	if v, ok := claims["scp"]; ok {
		switch t := v.(type) {
		case string:
			return t
		case []any:
			parts := make([]string, 0, len(t))
			for _, item := range t {
				if s, ok := item.(string); ok {
					parts = append(parts, s)
				}
			}
			return strings.Join(parts, " ")
		}
	}
	return ""
}

func scopeContains(scope, required string) bool {
	if required == "" {
		return true
	}
	for _, s := range strings.Fields(scope) {
		if s == required {
			return true
		}
	}
	return false
}

func extractAgentID(claims jwt.MapClaims, primary string) string {
	for _, name := range []string{primary, "client_id", "azp", "sub"} {
		if name == "" {
			continue
		}
		if v := stringClaim(claims, name); v != "" {
			return v
		}
	}
	return ""
}

func stringClaim(claims jwt.MapClaims, name string) string {
	if v, ok := claims[name].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}
