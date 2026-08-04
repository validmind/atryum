package authprovider

import (
	"context"
	"fmt"

	"github.com/validmind/atryum/internal/mcp"
)

type CustomHeaderProvider struct{}

func (CustomHeaderProvider) ID() string          { return "custom_headers" }
func (CustomHeaderProvider) DisplayName() string { return "Custom Headers" }
func (CustomHeaderProvider) CanHandle(upstream mcp.Upstream) bool {
	return upstream.Mode == mcp.UpstreamModeHTTP && len(upstream.AuthHeaders) > 0
}
func (CustomHeaderProvider) Prepare(_ context.Context, upstream mcp.Upstream) (mcp.Upstream, error) {
	prepared := upstream
	prepared.Status.AuthType = mcp.AuthTypeEnv
	prepared.OAuthProviderID = "custom_headers"
	prepared.OAuthProviderLabel = "Custom Headers"
	prepared.OAuthAuthorizeURL = ""
	prepared.OAuthTokenURL = ""
	prepared.OAuthClientID = ""
	prepared.OAuthClientSecret = ""
	prepared.OAuthScopes = ""
	return prepared, nil
}
func (CustomHeaderProvider) BuildConnectRequest(_ context.Context, _ mcp.Upstream, _ string, _ string) (ConnectRequest, error) {
	return ConnectRequest{}, fmt.Errorf("custom header provider does not support browser connect")
}
func (CustomHeaderProvider) ExchangeAuthCode(_ context.Context, _ *mcp.Client, _ mcp.Upstream, _ string, _ string, _ ConnectSession) (mcp.OAuthToken, error) {
	return mcp.OAuthToken{}, fmt.Errorf("custom header provider does not support auth code exchange")
}
