package authprovider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/validmind/atryum/internal/mcp"
)

// OAuthMetadataProvider is the discovery-driven dispatcher. It runs RFC 9728
// protected-resource discovery (when possible) and RFC 8414 / OIDC
// authorization-server discovery, records what the server advertises onto the
// upstream, and then resolves to a concrete sub-provider (DCR, PKCE, client
// secret) based on what the server supports *and* what credentials the admin
// has supplied.
type OAuthMetadataProvider struct{}

func (OAuthMetadataProvider) ID() string          { return "oauth_metadata" }
func (OAuthMetadataProvider) DisplayName() string { return "OAuth (auto-detected)" }

// CanHandle deliberately accepts any HTTP upstream — this provider is the
// catch-all that runs discovery. It must be registered LAST so that
// statically-configured providers (bearer token / custom headers / explicit
// client-secret) win first.
func (OAuthMetadataProvider) CanHandle(upstream mcp.Upstream) bool {
	return upstream.Mode == mcp.UpstreamModeHTTP && strings.TrimSpace(upstream.BaseURL) != ""
}

func (OAuthMetadataProvider) Prepare(ctx context.Context, upstream mcp.Upstream) (mcp.Upstream, error) {
	prepared := upstream
	prepared.Status.AuthType = mcp.AuthTypeHosted

	metadata, _ := DiscoverAuthorizationServer(ctx, prepared.BaseURL)
	prepared.OAuthAuthorizeURL = firstNonEmpty(prepared.OAuthAuthorizeURL, metadata.AuthorizationEndpoint)
	prepared.OAuthTokenURL = firstNonEmpty(prepared.OAuthTokenURL, metadata.TokenEndpoint)
	if strings.TrimSpace(prepared.OAuthScopes) == "" && len(metadata.ScopesSupported) > 0 {
		prepared.OAuthScopes = strings.Join(defaultScopes(metadata.ScopesSupported), " ")
	}

	return resolveGenericOAuthStrategy(prepared, metadata), nil
}

func (OAuthMetadataProvider) BuildConnectRequest(ctx context.Context, upstream mcp.Upstream, redirectURI string, state string) (ConnectRequest, error) {
	// If Prepare didn't resolve a strategy (often because discovery failed at
	// save time), re-discover now and try again. This is the on-demand path
	// hit when a user clicks Connect on a freshly-added server.
	if strings.TrimSpace(upstream.OAuthProviderID) == "" || upstream.OAuthProviderID == "oauth_metadata" {
		metadata, _ := DiscoverAuthorizationServer(ctx, upstream.BaseURL)
		upstream.OAuthAuthorizeURL = firstNonEmpty(upstream.OAuthAuthorizeURL, metadata.AuthorizationEndpoint)
		upstream.OAuthTokenURL = firstNonEmpty(upstream.OAuthTokenURL, metadata.TokenEndpoint)
		if strings.TrimSpace(upstream.OAuthScopes) == "" && len(metadata.ScopesSupported) > 0 {
			upstream.OAuthScopes = strings.Join(defaultScopes(metadata.ScopesSupported), " ")
		}
		upstream = resolveGenericOAuthStrategy(upstream, metadata)
	}
	provider, err := strategyProvider(upstream)
	if err != nil {
		return ConnectRequest{}, fmt.Errorf("could not determine an OAuth strategy for this server: discovery did not return usable authorization endpoints — confirm the server publishes RFC 9728 protected-resource metadata or supply an authorize URL / client id manually")
	}
	cr, err := provider.BuildConnectRequest(ctx, upstream, redirectURI, state)
	if err != nil {
		return ConnectRequest{}, err
	}
	// If we just resolved the strategy on the fly, layer the discovered
	// endpoints into the patch so the next reconnect is fast.
	prevPatch := cr.UpstreamPatch
	authorize := upstream.OAuthAuthorizeURL
	token := upstream.OAuthTokenURL
	scopes := upstream.OAuthScopes
	cr.UpstreamPatch = func(u *mcp.Upstream) {
		if authorize != "" {
			u.OAuthAuthorizeURL = authorize
		}
		if token != "" {
			u.OAuthTokenURL = token
		}
		if scopes != "" && strings.TrimSpace(u.OAuthScopes) == "" {
			u.OAuthScopes = scopes
		}
		if prevPatch != nil {
			prevPatch(u)
		}
	}
	return cr, nil
}

func (OAuthMetadataProvider) ExchangeAuthCode(ctx context.Context, client *mcp.Client, upstream mcp.Upstream, code string, redirectURI string, session ConnectSession) (mcp.OAuthToken, error) {
	provider, err := strategyProvider(upstream)
	if err != nil {
		return mcp.OAuthToken{}, err
	}
	return provider.ExchangeAuthCode(ctx, client, upstream, code, redirectURI, session)
}

// AuthorizationServerMetadata is a subset of RFC 8414 + OIDC discovery + the
// MCP 2025-11-25 additions that we care about for picking a registration
// approach.
type AuthorizationServerMetadata struct {
	Issuer                        string   `json:"issuer"`
	AuthorizationEndpoint         string   `json:"authorization_endpoint"`
	TokenEndpoint                 string   `json:"token_endpoint"`
	RegistrationEndpoint          string   `json:"registration_endpoint"`
	ScopesSupported               []string `json:"scopes_supported"`
	ResponseTypesSupported        []string `json:"response_types_supported"`
	GrantTypesSupported           []string `json:"grant_types_supported"`
	CodeChallengeMethodsSupported []string `json:"code_challenge_methods_supported"`
	TokenEndpointAuthMethods      []string `json:"token_endpoint_auth_methods_supported"`
	ClientIDMetadataDocSupported  bool     `json:"client_id_metadata_document_supported"`
}

type protectedResourceMetadata struct {
	Resource             string   `json:"resource"`
	AuthorizationServers []string `json:"authorization_servers"`
}

// DiscoverAuthorizationServer walks the spec'd discovery chain:
//  1. fetch protected-resource metadata at the MCP base URL
//  2. follow each advertised authorization_server
//  3. for each, try oauth-authorization-server then openid-configuration
//
// If protected-resource discovery fails it falls back to probing the base URL
// directly. Returns the first metadata document with non-empty
// authorize/token endpoints. Errors are non-fatal — callers should treat an
// empty metadata struct as "discovery turned up nothing".
func DiscoverAuthorizationServer(ctx context.Context, rawBaseURL string) (AuthorizationServerMetadata, error) {
	base, err := url.Parse(strings.TrimSpace(rawBaseURL))
	if err != nil || base.Host == "" {
		return AuthorizationServerMetadata{}, fmt.Errorf("invalid base url")
	}

	client := &http.Client{Timeout: 10 * time.Second}

	// Per RFC 9728 §3.1 the protected-resource metadata for a resource with
	// a path component lives at the *path-inserted* URL
	// (https://host/.well-known/oauth-protected-resource/<path>). Some
	// servers instead append the well-known suffix to the resource path,
	// and others only publish at the host root. Try all three, in spec
	// order, plus look for a WWW-Authenticate header on a probe request to
	// the MCP endpoint itself (the spec-canonical "challenge-driven" path).
	rootPath := *base
	rootPath.Path = ""
	rootPath.RawPath = ""
	prmURLs := []string{}
	seenPRM := map[string]bool{}
	addPRM := func(s string) {
		if s == "" || seenPRM[s] {
			return
		}
		seenPRM[s] = true
		prmURLs = append(prmURLs, s)
	}
	addPRM(wellKnownPathInserted(base, "/.well-known/oauth-protected-resource"))
	addPRM(wellKnownPathAppended(base, "/.well-known/oauth-protected-resource"))
	addPRM(wellKnownPathAppended(&rootPath, "/.well-known/oauth-protected-resource"))

	issuers := []string{}
	seenIssuer := map[string]bool{}
	addIssuer := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" || seenIssuer[s] {
			return
		}
		seenIssuer[s] = true
		issuers = append(issuers, s)
	}

	for _, candidate := range prmURLs {
		if rm, ok := fetchProtectedResourceMetadataAt(ctx, client, candidate); ok {
			for _, server := range rm.AuthorizationServers {
				addIssuer(server)
			}
		}
	}
	if rmURL, ok := resourceMetadataURLFromChallenge(ctx, client, base); ok {
		if rm, ok := fetchProtectedResourceMetadataAt(ctx, client, rmURL); ok {
			for _, server := range rm.AuthorizationServers {
				addIssuer(server)
			}
		}
	}
	// Fallback: try the resource URL itself and the host root as issuers.
	addIssuer(base.String())
	addIssuer(rootPath.String())

	debugf("discovery base=%s candidate_issuers=%v", base.String(), issuers)
	for _, issuer := range issuers {
		if metadata, ok := fetchAuthorizationServerMetadata(ctx, client, issuer); ok {
			debugf("discovery success issuer=%s authorize=%s token=%s registration=%s", issuer, metadata.AuthorizationEndpoint, metadata.TokenEndpoint, metadata.RegistrationEndpoint)
			return metadata, nil
		}
	}
	debugf("discovery failed base=%s candidates_tried=%d", base.String(), len(issuers))
	return AuthorizationServerMetadata{}, fmt.Errorf("oauth metadata discovery failed")
}

// wellKnownPathInserted builds the RFC 8414 §3.1 / RFC 9728 §3.1 form for a
// URL with a path component: the well-known suffix is inserted between the
// host and the path (https://host/.well-known/<suffix>/<path>). For a URL
// with no path this is identical to the appended form, and "" is returned
// so callers can skip the duplicate.
func wellKnownPathInserted(u *url.URL, suffix string) string {
	path := strings.Trim(u.Path, "/")
	if path == "" {
		return ""
	}
	out := *u
	out.RawPath = ""
	out.RawQuery = ""
	out.Fragment = ""
	out.Path = suffix + "/" + path
	return out.String()
}

// wellKnownPathAppended builds the legacy / OIDC-style form where the
// well-known suffix is appended after the path
// (https://host/<path>/.well-known/<suffix>).
func wellKnownPathAppended(u *url.URL, suffix string) string {
	out := *u
	out.RawPath = ""
	out.RawQuery = ""
	out.Fragment = ""
	out.Path = strings.TrimRight(u.Path, "/") + suffix
	return out.String()
}

// resourceMetadataURLFromChallenge sends unauthenticated requests to the
// resource and, if the server responds with a 401 + WWW-Authenticate that
// includes resource_metadata="...", returns that URL. It tries GET first and
// then a POST (a bare JSON-RPC initialize) because streamable-HTTP MCP
// servers commonly answer GET with 405 and only challenge on POST.
func resourceMetadataURLFromChallenge(ctx context.Context, client *http.Client, resource *url.URL) (string, bool) {
	if u, ok := challengeFromRequest(ctx, client, http.MethodGet, resource.String(), ""); ok {
		return u, true
	}
	const initialize = `{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"atryum","version":"discovery"}}}`
	return challengeFromRequest(ctx, client, http.MethodPost, resource.String(), initialize)
}

func challengeFromRequest(ctx context.Context, client *http.Client, method string, target string, body string) (string, bool) {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return "", false
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := client.Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	challenge := resp.Header.Get("WWW-Authenticate")
	if challenge == "" {
		return "", false
	}
	const key = "resource_metadata="
	idx := strings.Index(challenge, key)
	if idx < 0 {
		return "", false
	}
	rest := challenge[idx+len(key):]
	rest = strings.TrimLeft(rest, " ")
	if strings.HasPrefix(rest, `"`) {
		rest = rest[1:]
		end := strings.Index(rest, `"`)
		if end < 0 {
			return "", false
		}
		return rest[:end], true
	}
	end := strings.IndexAny(rest, ", ")
	if end < 0 {
		return rest, true
	}
	return rest[:end], true
}

func fetchProtectedResourceMetadataAt(ctx context.Context, client *http.Client, fullURL string) (protectedResourceMetadata, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fullURL, nil)
	if err != nil {
		return protectedResourceMetadata{}, false
	}
	resp, err := client.Do(req)
	if err != nil {
		return protectedResourceMetadata{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return protectedResourceMetadata{}, false
	}
	var payload protectedResourceMetadata
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return protectedResourceMetadata{}, false
	}
	return payload, len(payload.AuthorizationServers) > 0
}

func fetchAuthorizationServerMetadata(ctx context.Context, client *http.Client, issuer string) (AuthorizationServerMetadata, bool) {
	parsed, err := url.Parse(strings.TrimSpace(issuer))
	if err != nil {
		return AuthorizationServerMetadata{}, false
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	// Order follows the MCP authorization spec: for an issuer with a path
	// component try the RFC 8414 path-inserted forms first, then the
	// OIDC-style path-appended forms. For a bare host both collapse to the
	// same URLs and the empty path-inserted entries are skipped.
	candidates := []string{}
	seen := map[string]bool{}
	for _, candidate := range []string{
		wellKnownPathInserted(parsed, "/.well-known/oauth-authorization-server"),
		wellKnownPathInserted(parsed, "/.well-known/openid-configuration"),
		wellKnownPathAppended(parsed, "/.well-known/oauth-authorization-server"),
		wellKnownPathAppended(parsed, "/.well-known/openid-configuration"),
	} {
		if candidate == "" || seen[candidate] {
			continue
		}
		seen[candidate] = true
		candidates = append(candidates, candidate)
	}
	for _, candidate := range candidates {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, candidate, nil)
		if err != nil {
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			debugf("as metadata fetch error url=%s err=%v", candidate, err)
			continue
		}
		var payload AuthorizationServerMetadata
		err = json.NewDecoder(resp.Body).Decode(&payload)
		status := resp.StatusCode
		resp.Body.Close()
		if err == nil && strings.TrimSpace(payload.AuthorizationEndpoint) != "" && strings.TrimSpace(payload.TokenEndpoint) != "" {
			return payload, true
		}
		debugf("as metadata not usable url=%s status=%d decode_err=%v", candidate, status, err)
	}
	return AuthorizationServerMetadata{}, false
}

// resolveGenericOAuthStrategy picks the concrete provider that should handle a
// connect attempt, based on what credentials the admin has supplied AND what
// the authorization server advertises. Priority:
//
//  1. static escape hatches: bearer token, custom headers
//  2. pre-registered confidential client (client_id + client_secret)
//  3. pre-registered public client (client_id only) → PKCE
//  4. server advertises Dynamic Client Registration → DCR (registers, then PKCE)
//  5. nothing usable
func resolveGenericOAuthStrategy(upstream mcp.Upstream, metadata AuthorizationServerMetadata) mcp.Upstream {
	prepared := upstream
	if strings.TrimSpace(prepared.AuthToken) != "" {
		prepared.OAuthProviderID = "bearer_token"
		prepared.OAuthProviderLabel = "Bearer Token"
		prepared.Status.AuthType = mcp.AuthTypeBearer
		return prepared
	}
	if len(prepared.AuthHeaders) > 0 {
		prepared.OAuthProviderID = "custom_headers"
		prepared.OAuthProviderLabel = "Custom Headers"
		prepared.Status.AuthType = mcp.AuthTypeEnv
		return prepared
	}
	hasEndpoints := strings.TrimSpace(prepared.OAuthAuthorizeURL) != "" && strings.TrimSpace(prepared.OAuthTokenURL) != ""
	dcrAvailable := strings.TrimSpace(metadata.RegistrationEndpoint) != ""
	switch {
	case hasEndpoints && strings.TrimSpace(prepared.OAuthClientID) != "" && strings.TrimSpace(prepared.OAuthClientSecret) != "":
		prepared.OAuthProviderID = "oauth_client_secret"
		prepared.OAuthProviderLabel = "OAuth Client Secret"
	case hasEndpoints && strings.TrimSpace(prepared.OAuthClientID) != "":
		prepared.OAuthProviderID = "oauth_pkce"
		prepared.OAuthProviderLabel = "OAuth 2.1 PKCE"
	case hasEndpoints && dcrAvailable:
		prepared.OAuthProviderID = "oauth_dcr"
		prepared.OAuthProviderLabel = "OAuth Dynamic Client Registration"
	case hasEndpoints:
		// Endpoints exist but no DCR and no creds — admin must supply a
		// client_id manually. Surface as DCR so the UI shows a helpful label,
		// but the connect attempt will fail with a clear error.
		prepared.OAuthProviderID = "oauth_dcr"
		prepared.OAuthProviderLabel = "OAuth (needs client registration)"
	default:
		prepared.OAuthProviderID = ""
		prepared.OAuthProviderLabel = ""
	}
	prepared.Status.AuthType = mcp.AuthTypeHosted
	return prepared
}

func strategyProvider(upstream mcp.Upstream) (Provider, error) {
	switch strings.TrimSpace(upstream.OAuthProviderID) {
	case "oauth_pkce":
		return OAuthPKCEProvider{}, nil
	case "oauth_client_secret":
		return OAuthClientSecretProvider{}, nil
	case "oauth_dcr":
		return OAuthDynamicClientProvider{}, nil
	case "bearer_token":
		return BearerTokenProvider{}, nil
	case "custom_headers":
		return CustomHeaderProvider{}, nil
	default:
		return nil, fmt.Errorf("no connectable auth strategy is configured")
	}
}

// defaultScopes picks a sensible scope default for OIDC-style ASes only.
// For pure-OAuth ASes (Slack, GitHub, etc.) the right behavior is to send
// no scope parameter at all and let the AS grant whatever scopes the
// registered client app declared — picking an arbitrary subset of
// scopes_supported (as we used to) silently truncates consent to the
// first few scopes alphabetically and confuses everyone. Admins who want
// to scope down explicitly can fill in the manual OAuth form.
func defaultScopes(scopes []string) []string {
	available := make(map[string]struct{}, len(scopes))
	for _, scope := range scopes {
		available[scope] = struct{}{}
	}
	if _, ok := available["openid"]; !ok {
		return nil
	}
	out := []string{"openid"}
	for _, scope := range []string{"profile", "email"} {
		if _, ok := available[scope]; ok {
			out = append(out, scope)
		}
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
