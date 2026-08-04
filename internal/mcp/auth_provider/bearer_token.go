package authprovider

import (
	"context"
	"fmt"
	"strings"

	"github.com/validmind/atryum/internal/mcp"
)

type BearerTokenProvider struct{}

func (BearerTokenProvider) ID() string          { return "bearer_token" }
func (BearerTokenProvider) DisplayName() string { return "Bearer Token" }
func (BearerTokenProvider) CanHandle(upstream mcp.Upstream) bool {
	return upstream.Mode == mcp.UpstreamModeHTTP && strings.TrimSpace(upstream.AuthToken) != ""
}
func (BearerTokenProvider) Prepare(_ context.Context, upstream mcp.Upstream) (mcp.Upstream, error) {
	prepared := upstream
	prepared.Status.AuthType = mcp.AuthTypeBearer
	prepared.OAuthProviderID = "bearer_token"
	prepared.OAuthProviderLabel = "Bearer Token"
	prepared.OAuthAuthorizeURL = ""
	prepared.OAuthTokenURL = ""
	prepared.OAuthClientID = ""
	prepared.OAuthClientSecret = ""
	prepared.OAuthScopes = ""
	return prepared, nil
}
func (BearerTokenProvider) BuildConnectRequest(_ context.Context, _ mcp.Upstream, _ string, _ string) (ConnectRequest, error) {
	return ConnectRequest{}, fmt.Errorf("bearer token provider does not support browser connect")
}
func (BearerTokenProvider) ExchangeAuthCode(_ context.Context, _ *mcp.Client, _ mcp.Upstream, _ string, _ string, _ ConnectSession) (mcp.OAuthToken, error) {
	return mcp.OAuthToken{}, fmt.Errorf("bearer token provider does not support auth code exchange")
}
