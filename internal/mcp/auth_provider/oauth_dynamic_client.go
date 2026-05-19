package authprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"atryum/internal/mcp"
)

// OAuthDynamicClientProvider implements RFC 7591 Dynamic Client Registration.
// At connect time it re-discovers the authorization server metadata,
// registers a fresh client, persists the resulting client_id (and
// client_secret, if returned) onto the upstream via UpstreamPatch, then
// builds a PKCE authorization-code request using the new client_id.
type OAuthDynamicClientProvider struct{}

func (OAuthDynamicClientProvider) ID() string          { return "oauth_dcr" }
func (OAuthDynamicClientProvider) DisplayName() string { return "OAuth Dynamic Client Registration" }
func (OAuthDynamicClientProvider) CanHandle(upstream mcp.Upstream) bool {
	return upstream.Mode == mcp.UpstreamModeHTTP && strings.TrimSpace(upstream.OAuthAuthorizeURL) != "" && strings.TrimSpace(upstream.OAuthTokenURL) != "" && strings.TrimSpace(upstream.OAuthClientID) == "" && strings.TrimSpace(upstream.OAuthClientSecret) == ""
}

func (p OAuthDynamicClientProvider) Prepare(_ context.Context, upstream mcp.Upstream) (mcp.Upstream, error) {
	prepared := upstream
	prepared.Status.AuthType = mcp.AuthTypeHosted
	prepared.OAuthProviderID = p.ID()
	prepared.OAuthProviderLabel = p.DisplayName()
	if strings.TrimSpace(prepared.OAuthScopes) == "" {
		prepared.OAuthScopes = "openid profile email"
	}
	return prepared, nil
}

func (p OAuthDynamicClientProvider) BuildConnectRequest(ctx context.Context, upstream mcp.Upstream, redirectURI string, state string) (ConnectRequest, error) {
	metadata, err := DiscoverAuthorizationServer(ctx, upstream.BaseURL)
	if err != nil || strings.TrimSpace(metadata.RegistrationEndpoint) == "" {
		return ConnectRequest{}, fmt.Errorf("dynamic client registration is not advertised by this server")
	}

	debugf("dcr starting registration_endpoint=%s redirect_uri=%s scopes=%q", metadata.RegistrationEndpoint, redirectURI, upstream.OAuthScopes)
	registered, err := registerOAuthClient(ctx, metadata.RegistrationEndpoint, redirectURI, upstream.OAuthScopes)
	if err != nil {
		debugf("dcr registration failed err=%v", err)
		return ConnectRequest{}, fmt.Errorf("dynamic client registration failed: %w", err)
	}
	debugf("dcr registration success client_id=%s has_secret=%t token_endpoint_auth_method=%q", registered.ClientID, registered.ClientSecret != "", registered.TokenEndpointAuthMethod)

	authorizeURL := firstNonEmpty(upstream.OAuthAuthorizeURL, metadata.AuthorizationEndpoint)
	tokenURL := firstNonEmpty(upstream.OAuthTokenURL, metadata.TokenEndpoint)
	if authorizeURL == "" || tokenURL == "" {
		return ConnectRequest{}, fmt.Errorf("authorization or token endpoint missing after discovery")
	}

	verifier, challenge, err := newPKCEPair()
	if err != nil {
		return ConnectRequest{}, err
	}
	debugf("dcr build pkce state=%s verifier_len=%d challenge=%s client_id=%s redirect_uri=%s", state, len(verifier), challenge, registered.ClientID, redirectURI)
	values := url.Values{}
	values.Set("response_type", "code")
	values.Set("client_id", registered.ClientID)
	values.Set("redirect_uri", redirectURI)
	values.Set("state", state)
	values.Set("code_challenge", challenge)
	values.Set("code_challenge_method", "S256")
	if strings.TrimSpace(upstream.OAuthScopes) != "" {
		values.Set("scope", upstream.OAuthScopes)
	}
	separator := "?"
	if strings.Contains(authorizeURL, "?") {
		separator = "&"
	}

	// Decide the runtime strategy from the AS-declared authentication
	// method, falling back to "is there a secret in the response?" for ASes
	// that omit token_endpoint_auth_method on the registration response.
	// RFC 7591 §3.2.1 requires the AS to return the actual method it chose;
	// we honor that when present, and otherwise infer from the payload
	// shape so older / loose ASes still work.
	authMethod := strings.ToLower(strings.TrimSpace(registered.TokenEndpointAuthMethod))
	if authMethod == "" {
		if registered.ClientSecret != "" {
			authMethod = "client_secret_basic"
		} else {
			authMethod = "none"
		}
	}

	patch := func(u *mcp.Upstream) {
		u.OAuthAuthorizeURL = authorizeURL
		u.OAuthTokenURL = tokenURL
		u.OAuthClientID = registered.ClientID
		u.OAuthClientRegistration = mcp.ClientRegistrationDynamic
		if authMethod == "none" {
			u.OAuthClientSecret = ""
			u.OAuthProviderID = "oauth_pkce"
			u.OAuthProviderLabel = "OAuth 2.1 PKCE"
		} else {
			u.OAuthClientSecret = registered.ClientSecret
			u.OAuthProviderID = "oauth_client_secret"
			u.OAuthProviderLabel = "OAuth Client Secret"
		}
	}

	return ConnectRequest{
		URL:           authorizeURL + separator + values.Encode(),
		CodeVerifier:  verifier,
		UpstreamPatch: patch,
	}, nil
}

func (p OAuthDynamicClientProvider) ExchangeAuthCode(ctx context.Context, client *mcp.Client, upstream mcp.Upstream, code string, redirectURI string, session ConnectSession) (mcp.OAuthToken, error) {
	if strings.TrimSpace(upstream.OAuthClientID) == "" {
		return mcp.OAuthToken{}, fmt.Errorf("dynamic client registration did not persist a client id; reconnect from scratch")
	}
	if strings.TrimSpace(upstream.OAuthClientSecret) != "" {
		return OAuthClientSecretProvider{}.ExchangeAuthCode(ctx, client, upstream, code, redirectURI, session)
	}
	return OAuthPKCEProvider{}.ExchangeAuthCode(ctx, client, upstream, code, redirectURI, session)
}

type dynamicClientRegistrationResponse struct {
	ClientID                string `json:"client_id"`
	ClientSecret            string `json:"client_secret"`
	TokenEndpointAuthMethod string `json:"token_endpoint_auth_method"`
}

func registerOAuthClient(ctx context.Context, registrationEndpoint string, redirectURI string, scopes string) (dynamicClientRegistrationResponse, error) {
	body := map[string]any{
		"client_name":                "Atryum",
		"redirect_uris":              []string{redirectURI},
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
	}
	if strings.TrimSpace(scopes) != "" {
		body["scope"] = scopes
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return dynamicClientRegistrationResponse{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, registrationEndpoint, bytes.NewReader(encoded))
	if err != nil {
		return dynamicClientRegistrationResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return dynamicClientRegistrationResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		snippet := strings.TrimSpace(string(bodyBytes))
		if len(snippet) > 400 {
			snippet = snippet[:400] + "…"
		}
		if snippet == "" {
			return dynamicClientRegistrationResponse{}, fmt.Errorf("registration endpoint returned status %d", resp.StatusCode)
		}
		return dynamicClientRegistrationResponse{}, fmt.Errorf("registration endpoint returned status %d: %s", resp.StatusCode, snippet)
	}
	var payload dynamicClientRegistrationResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return dynamicClientRegistrationResponse{}, err
	}
	if strings.TrimSpace(payload.ClientID) == "" {
		return dynamicClientRegistrationResponse{}, fmt.Errorf("registration endpoint returned no client_id")
	}
	return payload, nil
}
