package authprovider

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"atryum/internal/mcp"
)

// OAuthClientSecretProvider handles the confidential-client authorization
// code flow. It still uses PKCE — OAuth 2.1 recommends PKCE for all clients,
// and the DCR path can produce an upstream with both a client_secret AND a
// PKCE-bound authorization code, so we must always send the verifier on the
// exchange to avoid an invalid_request on the second leg.
type OAuthClientSecretProvider struct{}

func (OAuthClientSecretProvider) ID() string          { return "oauth_client_secret" }
func (OAuthClientSecretProvider) DisplayName() string { return "OAuth Client Secret" }
func (OAuthClientSecretProvider) CanHandle(upstream mcp.Upstream) bool {
	return upstream.Mode == mcp.UpstreamModeHTTP && strings.TrimSpace(upstream.OAuthAuthorizeURL) != "" && strings.TrimSpace(upstream.OAuthTokenURL) != "" && strings.TrimSpace(upstream.OAuthClientID) != "" && strings.TrimSpace(upstream.OAuthClientSecret) != ""
}

func (OAuthClientSecretProvider) Prepare(_ context.Context, upstream mcp.Upstream) (mcp.Upstream, error) {
	prepared := upstream
	prepared.Status.AuthType = mcp.AuthTypeHosted
	prepared.OAuthProviderID = "oauth_client_secret"
	prepared.OAuthProviderLabel = "OAuth Client Secret"
	if strings.TrimSpace(prepared.OAuthScopes) == "" {
		prepared.OAuthScopes = "openid profile email"
	}
	return prepared, nil
}

func (OAuthClientSecretProvider) BuildConnectRequest(_ context.Context, upstream mcp.Upstream, redirectURI string, state string) (ConnectRequest, error) {
	if strings.TrimSpace(upstream.OAuthAuthorizeURL) == "" || strings.TrimSpace(upstream.OAuthClientID) == "" {
		return ConnectRequest{}, fmt.Errorf("missing oauth provider configuration")
	}
	verifier, challenge, err := newPKCEPair()
	if err != nil {
		return ConnectRequest{}, err
	}
	values := url.Values{}
	values.Set("response_type", "code")
	values.Set("client_id", upstream.OAuthClientID)
	values.Set("redirect_uri", redirectURI)
	values.Set("state", state)
	values.Set("code_challenge", challenge)
	values.Set("code_challenge_method", "S256")
	if strings.TrimSpace(upstream.OAuthScopes) != "" {
		values.Set("scope", upstream.OAuthScopes)
	}
	separator := "?"
	if strings.Contains(upstream.OAuthAuthorizeURL, "?") {
		separator = "&"
	}
	return ConnectRequest{URL: upstream.OAuthAuthorizeURL + separator + values.Encode(), CodeVerifier: verifier}, nil
}

func (OAuthClientSecretProvider) ExchangeAuthCode(ctx context.Context, client *mcp.Client, upstream mcp.Upstream, code string, redirectURI string, session ConnectSession) (mcp.OAuthToken, error) {
	return client.ExchangeOAuthCode(ctx, upstream, code, redirectURI, session.CodeVerifier)
}
