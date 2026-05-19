package authprovider

import (
	"context"

	"atryum/internal/mcp"
)

type Provider interface {
	ID() string
	DisplayName() string
	CanHandle(upstream mcp.Upstream) bool
	Prepare(ctx context.Context, upstream mcp.Upstream) (mcp.Upstream, error)
	BuildConnectRequest(ctx context.Context, upstream mcp.Upstream, redirectURI string, state string) (ConnectRequest, error)
	ExchangeAuthCode(ctx context.Context, client *mcp.Client, upstream mcp.Upstream, code string, redirectURI string, session ConnectSession) (mcp.OAuthToken, error)
}

type ConnectRequest struct {
	URL          string
	CodeVerifier string
	// UpstreamPatch, if non-nil, is applied to the persisted upstream before
	// the connect URL is returned to the user. Used by Dynamic Client
	// Registration to record the newly registered client_id/secret and the
	// discovered authorize/token endpoints.
	UpstreamPatch func(*mcp.Upstream)
}

type ConnectSession struct {
	State        string
	CodeVerifier string
}
