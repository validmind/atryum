package authprovider

import (
	"context"
	"fmt"
	"strings"

	"atryum/internal/mcp"
)

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
func (p OAuthDynamicClientProvider) BuildConnectRequest(_ context.Context, _ mcp.Upstream, _ string, _ string) (ConnectRequest, error) {
	return ConnectRequest{}, fmt.Errorf("dynamic client registration is not implemented yet")
}
func (p OAuthDynamicClientProvider) ExchangeAuthCode(_ context.Context, _ *mcp.Client, _ mcp.Upstream, _ string, _ string, _ ConnectSession) (mcp.OAuthToken, error) {
	return mcp.OAuthToken{}, fmt.Errorf("dynamic client registration is not implemented yet")
}
