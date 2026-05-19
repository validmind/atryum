package authprovider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"atryum/internal/mcp"
)

func TestPrepareDetectsOAuthMetadataAndFallsBackToDCR(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"authorization_endpoint":"http://issuer.example/auth","token_endpoint":"http://issuer.example/token","registration_endpoint":"http://issuer.example/register","scopes_supported":["openid","profile","email"]}`))
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

func TestPrepareSurfacesNeedsClientRegistrationWhenNoDCR(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"authorization_endpoint":"http://issuer.example/auth","token_endpoint":"http://issuer.example/token"}`))
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
		t.Fatalf("expected oauth_dcr provider id, got %q", prepared.OAuthProviderID)
	}
	if !strings.Contains(prepared.OAuthProviderLabel, "needs client registration") {
		t.Fatalf("expected label to flag missing registration, got %q", prepared.OAuthProviderLabel)
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

func TestOAuthDCRRegistersAndReturnsPatch(t *testing.T) {
	var registrationCalled bool
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		base := "http://" + r.Host
		_, _ = w.Write([]byte(`{"authorization_endpoint":"` + base + `/auth","token_endpoint":"` + base + `/token","registration_endpoint":"` + base + `/register"}`))
	})
	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		registrationCalled = true
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if redirects, ok := body["redirect_uris"].([]any); !ok || len(redirects) == 0 {
			t.Errorf("registration request missing redirect_uris: %v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"client_id":"dyn-client-abc"}`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	provider := OAuthDynamicClientProvider{}
	result, err := provider.BuildConnectRequest(context.Background(), mcp.Upstream{
		Mode:              mcp.UpstreamModeHTTP,
		BaseURL:           server.URL,
		OAuthAuthorizeURL: server.URL + "/auth",
		OAuthTokenURL:     server.URL + "/token",
		OAuthScopes:       "openid",
	}, "http://localhost:8080/callback", "state-xyz")
	if err != nil {
		t.Fatalf("DCR BuildConnectRequest: %v", err)
	}
	if !registrationCalled {
		t.Fatal("expected registration endpoint to be called")
	}
	if !strings.Contains(result.URL, "client_id=dyn-client-abc") {
		t.Fatalf("expected newly registered client_id in url, got %q", result.URL)
	}
	if !strings.Contains(result.URL, "code_challenge=") {
		t.Fatalf("expected PKCE code_challenge, got %q", result.URL)
	}
	if result.UpstreamPatch == nil {
		t.Fatal("expected UpstreamPatch to be set")
	}
	var u mcp.Upstream
	result.UpstreamPatch(&u)
	if u.OAuthClientID != "dyn-client-abc" {
		t.Fatalf("patch did not set client id, got %q", u.OAuthClientID)
	}
	if u.OAuthProviderID != "oauth_pkce" {
		t.Fatalf("expected provider to flip to oauth_pkce after registration, got %q", u.OAuthProviderID)
	}
}

// TestOAuthDCRWithReturnedSecretStillSendsVerifier mirrors the Shortcut
// shape: the AS ignores token_endpoint_auth_method=none and returns a
// client_secret, which flips the resolved provider to oauth_client_secret.
// The authorization code is still PKCE-bound (we sent a challenge), so the
// exchange MUST include the verifier or the AS returns invalid_request.
func TestOAuthDCRWithReturnedSecretStillSendsVerifier(t *testing.T) {
	provider := OAuthDynamicClientProvider{}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		base := "http://" + r.Host
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"authorization_endpoint":"` + base + `/auth","token_endpoint":"` + base + `/token","registration_endpoint":"` + base + `/register"}`))
	})
	mux.HandleFunc("/register", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"client_id":"cid","client_secret":"shh"}`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	result, err := provider.BuildConnectRequest(context.Background(), mcp.Upstream{
		Mode:              mcp.UpstreamModeHTTP,
		BaseURL:           server.URL,
		OAuthAuthorizeURL: server.URL + "/auth",
		OAuthTokenURL:     server.URL + "/token",
	}, "http://localhost:8080/callback", "state")
	if err != nil {
		t.Fatalf("BuildConnectRequest: %v", err)
	}
	if result.CodeVerifier == "" {
		t.Fatal("expected PKCE verifier to be stored even when a client_secret was issued")
	}
	if !strings.Contains(result.URL, "code_challenge=") {
		t.Fatal("expected PKCE challenge in authorize URL")
	}
	if result.UpstreamPatch == nil {
		t.Fatal("expected patch")
	}
	var u mcp.Upstream
	result.UpstreamPatch(&u)
	if u.OAuthProviderID != "oauth_client_secret" {
		t.Fatalf("expected provider to flip to oauth_client_secret with returned secret, got %q", u.OAuthProviderID)
	}
	if u.OAuthClientSecret != "shh" {
		t.Fatalf("expected secret to be persisted, got %q", u.OAuthClientSecret)
	}

	// And the exchange path on OAuthClientSecretProvider must forward the
	// verifier — regression for the Shortcut invalid_request bug.
	tokenCalled := make(chan url.Values, 1)
	tokenMux := http.NewServeMux()
	tokenMux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		tokenCalled <- r.PostForm
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"at","token_type":"Bearer"}`))
	})
	tokenServer := httptest.NewServer(tokenMux)
	defer tokenServer.Close()

	upstream := mcp.Upstream{
		Mode:              mcp.UpstreamModeHTTP,
		OAuthTokenURL:     tokenServer.URL + "/token",
		OAuthClientID:     "cid",
		OAuthClientSecret: "shh",
	}
	client := mcp.NewHTTPClient()
	_, err = OAuthClientSecretProvider{}.ExchangeAuthCode(context.Background(), client, upstream, "code", "http://localhost:8080/callback", ConnectSession{CodeVerifier: "the-verifier"})
	if err != nil {
		t.Fatalf("ExchangeAuthCode: %v", err)
	}
	form := <-tokenCalled
	if form.Get("code_verifier") != "the-verifier" {
		t.Fatalf("client_secret exchange must forward code_verifier; got form=%v", form)
	}
	if form.Get("client_secret") != "shh" {
		t.Fatalf("client_secret exchange must send client_secret; got form=%v", form)
	}
}

// TestOAuthDCRHonorsDeclaredTokenEndpointAuthMethod proves that the AS-
// declared token_endpoint_auth_method (RFC 7591 §3.2.1) wins over the
// secret-arrived heuristic. An AS that returns method="none" but also
// includes a stray client_secret should be treated as public (PKCE-only),
// not confidential.
func TestOAuthDCRHonorsDeclaredTokenEndpointAuthMethod(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		base := "http://" + r.Host
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"authorization_endpoint":"` + base + `/auth","token_endpoint":"` + base + `/token","registration_endpoint":"` + base + `/register"}`))
	})
	mux.HandleFunc("/register", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"client_id":"cid","client_secret":"stray","token_endpoint_auth_method":"none"}`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	result, err := OAuthDynamicClientProvider{}.BuildConnectRequest(context.Background(), mcp.Upstream{
		Mode:              mcp.UpstreamModeHTTP,
		BaseURL:           server.URL,
		OAuthAuthorizeURL: server.URL + "/auth",
		OAuthTokenURL:     server.URL + "/token",
	}, "http://localhost:8080/callback", "state")
	if err != nil {
		t.Fatalf("BuildConnectRequest: %v", err)
	}
	var u mcp.Upstream
	result.UpstreamPatch(&u)
	if u.OAuthProviderID != "oauth_pkce" {
		t.Fatalf("declared auth_method=none must resolve to oauth_pkce, got %q", u.OAuthProviderID)
	}
	if u.OAuthClientSecret != "" {
		t.Fatalf("declared auth_method=none must not retain stray client_secret, got %q", u.OAuthClientSecret)
	}
}

func TestOAuthDCRFailsWhenRegistrationNotAdvertised(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		base := "http://" + r.Host
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"authorization_endpoint":"` + base + `/auth","token_endpoint":"` + base + `/token"}`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	_, err := OAuthDynamicClientProvider{}.BuildConnectRequest(context.Background(), mcp.Upstream{
		Mode:              mcp.UpstreamModeHTTP,
		BaseURL:           server.URL,
		OAuthAuthorizeURL: server.URL + "/auth",
		OAuthTokenURL:     server.URL + "/token",
	}, "http://localhost:8080/callback", "state-xyz")
	if err == nil || !strings.Contains(err.Error(), "not advertised") {
		t.Fatalf("expected not-advertised error, got %v", err)
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

// TestPrepareDiscoversAtHostRootWhenResourcePathIs404 mirrors the Datadog
// shape: the MCP path does not serve protected-resource metadata, but the
// host root does. Discovery must walk up to the root before giving up.
func TestPrepareDiscoversAtHostRootWhenResourcePathIs404(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/mcp/.well-known/oauth-protected-resource", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(404)
	})
	mux.HandleFunc("/.well-known/oauth-protected-resource", func(w http.ResponseWriter, r *http.Request) {
		base := "http://" + r.Host
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"resource":"` + base + `","authorization_servers":["` + base + `"]}`))
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		base := "http://" + r.Host
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"authorization_endpoint":"` + base + `/auth","token_endpoint":"` + base + `/token","registration_endpoint":"` + base + `/register","code_challenge_methods_supported":["S256"]}`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	prepared, err := Prepare(context.Background(), NewRegistry(), mcp.Upstream{
		Mode:    mcp.UpstreamModeHTTP,
		BaseURL: server.URL + "/api/mcp",
	})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if prepared.OAuthProviderID != "oauth_dcr" {
		t.Fatalf("expected oauth_dcr (DCR advertised by host-root metadata), got %q", prepared.OAuthProviderID)
	}
	if !strings.HasSuffix(prepared.OAuthAuthorizeURL, "/auth") {
		t.Fatalf("expected discovered authorize endpoint, got %q", prepared.OAuthAuthorizeURL)
	}
}

func TestPrepareResourceMetadataPointsToIssuer(t *testing.T) {
	var issuer *httptest.Server
	issuerMux := http.NewServeMux()
	issuerMux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"authorization_endpoint":"` + issuer.URL + `/auth","token_endpoint":"` + issuer.URL + `/token","registration_endpoint":"` + issuer.URL + `/register"}`))
	})
	issuer = httptest.NewServer(issuerMux)
	defer issuer.Close()

	resourceMux := http.NewServeMux()
	resourceMux.HandleFunc("/.well-known/oauth-protected-resource", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"resource":"mcp","authorization_servers":["` + issuer.URL + `"]}`))
	})
	resource := httptest.NewServer(resourceMux)
	defer resource.Close()

	prepared, err := Prepare(context.Background(), NewRegistry(), mcp.Upstream{
		Mode:    mcp.UpstreamModeHTTP,
		BaseURL: resource.URL,
	})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if prepared.OAuthAuthorizeURL != issuer.URL+"/auth" {
		t.Fatalf("expected authorize endpoint resolved via resource metadata, got %q", prepared.OAuthAuthorizeURL)
	}
	if prepared.OAuthProviderID != "oauth_dcr" {
		t.Fatalf("expected dcr provider id, got %q", prepared.OAuthProviderID)
	}
}
