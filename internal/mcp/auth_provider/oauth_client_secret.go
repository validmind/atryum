package authprovider

import (
	"context"
	"net/url"
	"strings"

	"atryum/internal/mcp"
)

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
	values := url.Values{}
	values.Set("response_type", "code")
	values.Set("client_id", upstream.OAuthClientID)
	values.Set("redirect_uri", redirectURI)
	values.Set("state", state)
	if strings.TrimSpace(upstream.OAuthScopes) != "" {
		values.Set("scope", upstream.OAuthScopes)
	}
	separator := "?"
	if strings.Contains(upstream.OAuthAuthorizeURL, "?") {
		separator = "&"
	}
	return ConnectRequest{URL: upstream.OAuthAuthorizeURL + separator + values.Encode()}, nil
}
func (OAuthClientSecretProvider) ExchangeAuthCode(ctx context.Context, client *mcp.Client, upstream mcp.Upstream, code string, redirectURI string, _ ConnectSession) (mcp.OAuthToken, error) {
	return client.ExchangeOAuthCode(ctx, upstream, code, redirectURI, "")
}
