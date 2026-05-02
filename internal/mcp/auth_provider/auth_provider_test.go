package authprovider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"atryum/internal/mcp"
)

func TestPrepareDetectsOAuthMetadataAndFallsBackToDCR(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"authorization_endpoint":"http://issuer.example/auth","token_endpoint":"http://issuer.example/token","scopes_supported":["openid","profile","email"]}`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	prepared, err := Prepare(context.Background(), NewRegistry(), mcp.Upstream{
		Name:    "oidc",
		Mode:    mcp.UpstreamModeHTTP,
		BaseURL: server.URL,
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("Prepare returned error: %v", err)
	}
	if prepared.OAuthProviderID != "oauth_dcr" {
		t.Fatalf("expected oauth_dcr provider, got %q", prepared.OAuthProviderID)
	}
}

func TestPreparePrefersPKCEWhenClientIDIsPresent(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"authorization_endpoint":"http://issuer.example/auth","token_endpoint":"http://issuer.example/token","scopes_supported":["openid","profile","email"]}`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	prepared, err := Prepare(context.Background(), NewRegistry(), mcp.Upstream{
		Name:          "oidc",
		Mode:          mcp.UpstreamModeHTTP,
		BaseURL:       server.URL,
		OAuthClientID: "client-id",
		Enabled:       true,
	})
	if err != nil {
		t.Fatalf("Prepare returned error: %v", err)
	}
	if prepared.OAuthProviderID != "oauth_pkce" {
		t.Fatalf("expected oauth_pkce provider, got %q", prepared.OAuthProviderID)
	}
}

func TestOAuthPKCEBuildConnectRequestIncludesChallenge(t *testing.T) {
	provider := OAuthPKCEProvider{}
	result, err := provider.BuildConnectRequest(context.Background(), mcp.Upstream{
		Mode:              mcp.UpstreamModeHTTP,
		OAuthAuthorizeURL: "http://issuer.example/auth",
		OAuthClientID:     "client-id",
		OAuthScopes:       "openid profile",
	}, "http://localhost:8080/callback", "state-123")
	if err != nil {
		t.Fatalf("BuildConnectRequest returned error: %v", err)
	}
	if result.CodeVerifier == "" {
		t.Fatal("expected code verifier to be stored")
	}
	if !strings.Contains(result.URL, "code_challenge=") {
		t.Fatalf("expected code_challenge in url %q", result.URL)
	}
}

func TestOAuthDCRBuildConnectRequestIsExplicitlyNotImplemented(t *testing.T) {
	provider := OAuthDynamicClientProvider{}
	_, err := provider.BuildConnectRequest(context.Background(), mcp.Upstream{
		Mode:              mcp.UpstreamModeHTTP,
		OAuthAuthorizeURL: "http://issuer.example/auth",
		OAuthTokenURL:     "http://issuer.example/token",
	}, "http://localhost:8080/callback", "state-123")
	if err == nil || !strings.Contains(err.Error(), "not implemented") {
		t.Fatalf("expected not implemented error, got %v", err)
	}
}

func TestPrepareDetectsBearerTokenAndCustomHeaders(t *testing.T) {
	bearerPrepared, err := Prepare(context.Background(), NewRegistry(), mcp.Upstream{
		Mode:      mcp.UpstreamModeHTTP,
		BaseURL:   "https://example.com/mcp",
		AuthToken: "pat-123",
	})
	if err != nil {
		t.Fatalf("Prepare returned error for bearer: %v", err)
	}
	if bearerPrepared.OAuthProviderID != "bearer_token" {
		t.Fatalf("expected bearer_token provider, got %q", bearerPrepared.OAuthProviderID)
	}

	headersPrepared, err := Prepare(context.Background(), NewRegistry(), mcp.Upstream{
		Mode:    mcp.UpstreamModeHTTP,
		BaseURL: "https://example.com/mcp",
		AuthHeaders: []mcp.AuthHeader{{
			Name:  "X-API-Key",
			Value: "secret",
		}},
	})
	if err != nil {
		t.Fatalf("Prepare returned error for headers: %v", err)
	}
	if headersPrepared.OAuthProviderID != "custom_headers" {
		t.Fatalf("expected custom_headers provider, got %q", headersPrepared.OAuthProviderID)
	}
}
