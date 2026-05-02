package authprovider

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"

	"atryum/internal/mcp"
)

type OAuthPKCEProvider struct{}

func (OAuthPKCEProvider) ID() string          { return "oauth_pkce" }
func (OAuthPKCEProvider) DisplayName() string { return "OAuth 2.1 PKCE" }
func (OAuthPKCEProvider) CanHandle(upstream mcp.Upstream) bool {
	return upstream.Mode == mcp.UpstreamModeHTTP && strings.TrimSpace(upstream.OAuthAuthorizeURL) != "" && strings.TrimSpace(upstream.OAuthTokenURL) != "" && strings.TrimSpace(upstream.OAuthClientID) != "" && strings.TrimSpace(upstream.OAuthClientSecret) == ""
}
func (OAuthPKCEProvider) Prepare(_ context.Context, upstream mcp.Upstream) (mcp.Upstream, error) {
	prepared := upstream
	prepared.Status.AuthType = mcp.AuthTypeHosted
	prepared.OAuthProviderID = "oauth_pkce"
	prepared.OAuthProviderLabel = "OAuth 2.1 PKCE"
	if strings.TrimSpace(prepared.OAuthScopes) == "" {
		prepared.OAuthScopes = "openid profile email"
	}
	return prepared, nil
}
func (OAuthPKCEProvider) BuildConnectRequest(_ context.Context, upstream mcp.Upstream, redirectURI string, state string) (ConnectRequest, error) {
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
func (OAuthPKCEProvider) ExchangeAuthCode(ctx context.Context, client *mcp.Client, upstream mcp.Upstream, code string, redirectURI string, session ConnectSession) (mcp.OAuthToken, error) {
	if strings.TrimSpace(session.CodeVerifier) == "" {
		return mcp.OAuthToken{}, fmt.Errorf("missing pkce code verifier")
	}
	return client.ExchangeOAuthCode(ctx, upstream, code, redirectURI, session.CodeVerifier)
}

func newPKCEPair() (string, string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	verifier := base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge, nil
}
