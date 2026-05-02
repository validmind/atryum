package authprovider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"atryum/internal/mcp"
)

type OAuthMetadataProvider struct{}

func (OAuthMetadataProvider) ID() string          { return "oauth_metadata" }
func (OAuthMetadataProvider) DisplayName() string { return "OAuth / OIDC Discovery" }
func (OAuthMetadataProvider) CanHandle(upstream mcp.Upstream) bool {
	return upstream.Mode == mcp.UpstreamModeHTTP && strings.TrimSpace(upstream.BaseURL) != ""
}
func (OAuthMetadataProvider) Prepare(ctx context.Context, upstream mcp.Upstream) (mcp.Upstream, error) {
	prepared := upstream
	prepared.Status.AuthType = mcp.AuthTypeHosted

	metadata, err := discoverOAuthMetadata(ctx, prepared.BaseURL)
	if err == nil {
		prepared.OAuthAuthorizeURL = firstNonEmpty(prepared.OAuthAuthorizeURL, metadata.AuthorizationEndpoint)
		prepared.OAuthTokenURL = firstNonEmpty(prepared.OAuthTokenURL, metadata.TokenEndpoint)
		if strings.TrimSpace(prepared.OAuthScopes) == "" && len(metadata.ScopesSupported) > 0 {
			prepared.OAuthScopes = strings.Join(defaultScopes(metadata.ScopesSupported), " ")
		}
	}

	return resolveGenericOAuthStrategy(prepared), nil
}
func (OAuthMetadataProvider) BuildConnectRequest(ctx context.Context, upstream mcp.Upstream, redirectURI string, state string) (ConnectRequest, error) {
	provider, err := strategyProvider(upstream)
	if err != nil {
		return ConnectRequest{}, err
	}
	return provider.BuildConnectRequest(ctx, upstream, redirectURI, state)
}
func (OAuthMetadataProvider) ExchangeAuthCode(ctx context.Context, client *mcp.Client, upstream mcp.Upstream, code string, redirectURI string, session ConnectSession) (mcp.OAuthToken, error) {
	provider, err := strategyProvider(upstream)
	if err != nil {
		return mcp.OAuthToken{}, err
	}
	return provider.ExchangeAuthCode(ctx, client, upstream, code, redirectURI, session)
}

type oidcDiscoveryDocument struct {
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	ScopesSupported       []string `json:"scopes_supported"`
}

func resolveGenericOAuthStrategy(upstream mcp.Upstream) mcp.Upstream {
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
	if strings.TrimSpace(prepared.OAuthAuthorizeURL) != "" && strings.TrimSpace(prepared.OAuthTokenURL) != "" {
		if strings.TrimSpace(prepared.OAuthClientID) != "" && strings.TrimSpace(prepared.OAuthClientSecret) != "" {
			prepared.OAuthProviderID = "oauth_client_secret"
			prepared.OAuthProviderLabel = "OAuth Client Secret"
		} else if strings.TrimSpace(prepared.OAuthClientID) != "" {
			prepared.OAuthProviderID = "oauth_pkce"
			prepared.OAuthProviderLabel = "OAuth 2.1 PKCE"
		} else {
			prepared.OAuthProviderID = "oauth_dcr"
			prepared.OAuthProviderLabel = "OAuth Dynamic Client Registration"
		}
		prepared.Status.AuthType = mcp.AuthTypeHosted
		return prepared
	}
	prepared.OAuthProviderID = ""
	prepared.OAuthProviderLabel = ""
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

func discoverOAuthMetadata(ctx context.Context, rawBaseURL string) (oidcDiscoveryDocument, error) {
	base, err := url.Parse(strings.TrimSpace(rawBaseURL))
	if err != nil {
		return oidcDiscoveryDocument{}, err
	}
	base.Path = strings.TrimRight(base.Path, "/")
	candidates := []string{
		base.ResolveReference(&url.URL{Path: strings.TrimRight(base.Path, "/") + "/.well-known/oauth-authorization-server"}).String(),
		base.ResolveReference(&url.URL{Path: strings.TrimRight(base.Path, "/") + "/.well-known/openid-configuration"}).String(),
	}
	client := &http.Client{}
	for _, candidate := range candidates {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, candidate, nil)
		if err != nil {
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		var payload oidcDiscoveryDocument
		err = json.NewDecoder(resp.Body).Decode(&payload)
		resp.Body.Close()
		if err == nil && strings.TrimSpace(payload.AuthorizationEndpoint) != "" && strings.TrimSpace(payload.TokenEndpoint) != "" {
			return payload, nil
		}
	}
	return oidcDiscoveryDocument{}, fmt.Errorf("oauth metadata discovery failed")
}

func defaultScopes(scopes []string) []string {
	preferred := []string{"openid", "profile", "email"}
	out := make([]string, 0, len(preferred))
	available := make(map[string]struct{}, len(scopes))
	for _, scope := range scopes {
		available[scope] = struct{}{}
	}
	for _, scope := range preferred {
		if _, ok := available[scope]; ok {
			out = append(out, scope)
		}
	}
	if len(out) == 0 {
		for _, scope := range scopes {
			if strings.TrimSpace(scope) != "" {
				out = append(out, scope)
			}
			if len(out) == 3 {
				break
			}
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
