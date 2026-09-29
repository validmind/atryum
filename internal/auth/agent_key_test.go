package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/validmind/atryum/pkg/authz"
)

type stubKeyResolver struct {
	want     string
	identity Identity
	calls    int
}

func (s *stubKeyResolver) ResolveAgentKey(_ context.Context, token string) (Identity, error) {
	s.calls++
	if token == s.want {
		return s.identity, nil
	}
	return Identity{}, errors.New("unknown key")
}

func TestGenerateAgentKeyShape(t *testing.T) {
	token, hash, prefix, err := GenerateAgentKey()
	if err != nil {
		t.Fatalf("GenerateAgentKey: %v", err)
	}
	if !IsAgentKey(token) || !strings.HasPrefix(token, AgentKeyPrefix) {
		t.Fatalf("token %q should carry prefix %q", token, AgentKeyPrefix)
	}
	if len(token) < 40 {
		t.Fatalf("token too short: %d", len(token))
	}
	if hash != HashAgentKey(token) || len(hash) != 64 {
		t.Fatalf("hash mismatch or wrong length: %q", hash)
	}
	if prefix != token[:12] || !strings.HasPrefix(token, prefix) {
		t.Fatalf("prefix %q should be the first 12 chars of %q", prefix, token)
	}
	if HashAgentKey(" "+token+" ") != hash {
		t.Fatalf("hash should ignore surrounding whitespace")
	}
	other, _, _, _ := GenerateAgentKey()
	if other == token {
		t.Fatalf("two generated keys collided")
	}
	if IsAgentKey("eyJhbGciOi.jwt.token") {
		t.Fatalf("JWT should not be classified as an agent key")
	}
}

func TestMiddlewareAcceptsAgentKeyViaResolverAlongsideJWT(t *testing.T) {
	idp := newTestIdP(t)
	v := newValidatorForIdP(t, idp)
	resolver := &stubKeyResolver{want: "atr_good", identity: Identity{AgentID: "agent-uuid", Method: IdentityMethodAPIKey, KeyID: "k1"}}
	var got Identity
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = IdentityFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	h := MiddlewareWithOptions(v, "/.well-known/oauth-protected-resource", MiddlewareOptions{KeyResolver: resolver})(next)

	// Valid key.
	req := httptest.NewRequest(http.MethodGet, "/mcp/x", nil)
	req.Header.Set("Authorization", "Bearer atr_good")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK || got.AgentID != "agent-uuid" || got.Method != IdentityMethodAPIKey {
		t.Fatalf("expected key identity, got code=%d identity=%+v", w.Code, got)
	}

	// Unknown key: 401 with a bearer challenge, JWT validation never consulted.
	req = httptest.NewRequest(http.MethodGet, "/mcp/x", nil)
	req.Header.Set("Authorization", "Bearer atr_bad")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized || !strings.Contains(w.Header().Get("WWW-Authenticate"), "invalid_token") {
		t.Fatalf("expected 401 invalid_token for unknown key, got %d %q", w.Code, w.Header().Get("WWW-Authenticate"))
	}

	// JWT still works on the same chain.
	got = Identity{}
	req = httptest.NewRequest(http.MethodGet, "/mcp/x", nil)
	req.Header.Set("Authorization", "Bearer "+idp.sign(t, validClaims()))
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK || got.AgentID != "agent-1" || got.Method != IdentityMethodJWT {
		t.Fatalf("expected jwt identity, got code=%d identity=%+v", w.Code, got)
	}
	if resolver.calls != 2 {
		t.Fatalf("resolver should only see atr_ tokens, got %d calls", resolver.calls)
	}
}

func TestMiddlewareKeysAreOpportunisticWithoutValidator(t *testing.T) {
	resolver := &stubKeyResolver{want: "atr_good", identity: Identity{AgentID: "agent-uuid", Method: IdentityMethodAPIKey}}
	var got Identity
	var hadIdentity bool
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, hadIdentity = IdentityFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	h := MiddlewareWithOptions(nil, "", MiddlewareOptions{KeyResolver: resolver})(next)

	// No header: anonymous, as in any no-auth deployment.
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/mcp/x", nil))
	if w.Code != http.StatusOK || hadIdentity {
		t.Fatalf("expected anonymous pass-through, got code=%d identity=%v", w.Code, hadIdentity)
	}

	// A presented key is honoured.
	req := httptest.NewRequest(http.MethodGet, "/mcp/x", nil)
	req.Header.Set("Authorization", "Bearer atr_good")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK || got.AgentID != "agent-uuid" {
		t.Fatalf("expected key identity, got code=%d identity=%+v", w.Code, got)
	}

	// A bad key is refused even without an IdP: presenting atr_ opts in.
	req = httptest.NewRequest(http.MethodGet, "/mcp/x", nil)
	req.Header.Set("Authorization", "Bearer atr_bad")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for bad key without validator, got %d", w.Code)
	}

	// A non-key bearer is ignored, matching the historical no-auth behaviour.
	hadIdentity = false
	req = httptest.NewRequest(http.MethodGet, "/mcp/x", nil)
	req.Header.Set("Authorization", "Bearer some.jwt.value")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK || hadIdentity {
		t.Fatalf("expected anonymous pass-through for non-key bearer, got code=%d identity=%v", w.Code, hadIdentity)
	}
}

type stubProvisioner struct {
	disabled bool
	seen     UserIdentity
}

func (s *stubProvisioner) ProvisionUser(_ context.Context, id UserIdentity) (authz.Principal, error) {
	s.seen = id
	if s.disabled {
		return authz.Principal{}, ErrUserDisabled
	}
	role := authz.RoleMember
	if id.Admin {
		role = authz.RoleAdmin
	}
	return authz.Principal{UserID: "user-" + id.Subject, Issuer: id.Issuer, Subject: id.Subject, Email: id.Email, Role: role, Method: authz.MethodJWT}, nil
}

func adminConfig(c *Config) {
	c.AdminEnabled = true
	c.AdminClientID = "ui"
	c.AdminClaim = "atryum_admin"
	c.AdminClaimValue = "true"
}

func TestOperatorMiddlewareAttachesPrincipalAndRequireAdminGates(t *testing.T) {
	idp := newTestIdP(t)
	v := newValidatorForIdP(t, idp, adminConfig)
	prov := &stubProvisioner{}
	var got authz.Principal
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = authz.PrincipalFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	open := OperatorMiddleware(v, APIKeyConfig{Key: "k", Secret: "s"}, MiddlewareOptions{}, prov)(next)
	adminOnly := OperatorMiddleware(v, APIKeyConfig{Key: "k", Secret: "s"}, MiddlewareOptions{}, prov)(RequireAdmin(next))

	claims := validClaims()
	claims["email"] = "m@example.com"
	memberTok := idp.sign(t, claims)

	// Member reaches an open route with a member principal from the provisioner.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	req.Header.Set("Authorization", "Bearer "+memberTok)
	w := httptest.NewRecorder()
	open.ServeHTTP(w, req)
	if w.Code != http.StatusOK || got.Role != authz.RoleMember || got.UserID != "user-agent-subject" || got.Email != "m@example.com" {
		t.Fatalf("expected member principal, got code=%d principal=%+v", w.Code, got)
	}
	if prov.seen.Admin {
		t.Fatalf("provisioner should have seen a non-admin identity")
	}

	// Member is refused by RequireAdmin with 403.
	req = httptest.NewRequest(http.MethodGet, "/api/v1/servers", nil)
	req.Header.Set("Authorization", "Bearer "+memberTok)
	w = httptest.NewRecorder()
	adminOnly.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for member on admin route, got %d body=%s", w.Code, w.Body.String())
	}

	// Admin claim promotes the principal.
	claims["atryum_admin"] = true
	req = httptest.NewRequest(http.MethodGet, "/api/v1/servers", nil)
	req.Header.Set("Authorization", "Bearer "+idp.sign(t, claims))
	w = httptest.NewRecorder()
	adminOnly.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !got.IsAdmin() {
		t.Fatalf("expected admin principal, got code=%d principal=%+v", w.Code, got)
	}

	// Machine key yields a synthetic admin principal.
	req = httptest.NewRequest(http.MethodGet, "/api/v1/servers", nil)
	req.Header.Set("X-API-Key", "k")
	req.Header.Set("X-API-Secret", "s")
	w = httptest.NewRecorder()
	adminOnly.ServeHTTP(w, req)
	if w.Code != http.StatusOK || got.Method != authz.MethodMachineKey || !got.IsAdmin() {
		t.Fatalf("expected machine-key admin principal, got code=%d principal=%+v", w.Code, got)
	}

	// Disabled user is refused with 403 before reaching the handler.
	prov.disabled = true
	req = httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	req.Header.Set("Authorization", "Bearer "+memberTok)
	w = httptest.NewRecorder()
	open.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "disabled") {
		t.Fatalf("expected 403 for disabled user, got %d body=%s", w.Code, w.Body.String())
	}

	// No token at all is still 401.
	w = httptest.NewRecorder()
	open.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/me", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d", w.Code)
	}
}

func TestOperatorMiddlewareDisabledAuthInjectsAdminPrincipal(t *testing.T) {
	var got authz.Principal
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = authz.PrincipalFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	h := OperatorMiddleware(nil, APIKeyConfig{}, MiddlewareOptions{}, nil)(RequireAdmin(next))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/servers", nil))
	if w.Code != http.StatusOK || got.Method != authz.MethodNone || !got.IsAdmin() {
		t.Fatalf("expected synthetic admin principal, got code=%d principal=%+v", w.Code, got)
	}
}

func TestRequireAdminFailsClosedWithoutPrincipal(t *testing.T) {
	h := RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 when no principal is attached, got %d", w.Code)
	}
}

func TestValidateUserUsesConfiguredClaimLists(t *testing.T) {
	idp := newTestIdP(t)
	v := newValidatorForIdP(t, idp, adminConfig)

	// Standard claims.
	claims := validClaims()
	claims["email"] = "a@example.com"
	claims["name"] = "Alice"
	u, err := v.ValidateUser(context.Background(), idp.sign(t, claims))
	if err != nil || u.Email != "a@example.com" || u.Name != "Alice" || u.AccessToken == "" {
		t.Fatalf("standard claims: %+v err=%v", u, err)
	}

	// Entra-style: no email claim, upn + given/family names.
	claims = validClaims()
	claims["upn"] = "bob@corp.example"
	claims["given_name"] = "Bob"
	claims["family_name"] = "Builder"
	u, err = v.ValidateUser(context.Background(), idp.sign(t, claims))
	if err != nil || u.Email != "bob@corp.example" || u.Name != "Bob Builder" {
		t.Fatalf("entra-style claims: %+v err=%v", u, err)
	}

	// Auth0-style: nothing but sub, unless a namespaced claim is configured.
	claims = validClaims()
	claims["https://atryum.dev/email"] = "carol@example.com"
	u, err = v.ValidateUser(context.Background(), idp.sign(t, claims))
	if err != nil || u.Email != "" || u.Name != "" {
		t.Fatalf("default lists should not find namespaced claim: %+v err=%v", u, err)
	}
	v2 := newValidatorForIdP(t, idp, adminConfig, func(c *Config) {
		c.EmailClaims = []string{"https://atryum.dev/email"}
	})
	u, err = v2.ValidateUser(context.Background(), idp.sign(t, claims))
	if err != nil || u.Email != "carol@example.com" {
		t.Fatalf("configured namespaced claim: %+v err=%v", u, err)
	}
}

func TestUserInfoClientDiscoversAndMapsProfile(t *testing.T) {
	idp := newTestIdP(t)
	var gotAuth string
	mux := http.NewServeMux()
	mux.HandleFunc("/userinfo", func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"sub":"google-oauth2|123","email":"dana@example.com","name":"Dana"}`))
	})
	var srv *httptest.Server
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"userinfo_endpoint":"` + srv.URL + `/userinfo"}`))
	})
	srv = httptest.NewServer(mux)
	defer srv.Close()

	v := newValidatorForIdP(t, idp, adminConfig, func(c *Config) { c.Issuer = srv.URL })
	client := NewUserInfoClient(v, srv.Client())
	profile, err := client.FetchUserInfo(context.Background(), srv.URL+"/", "tok-1")
	if err != nil || profile.Email != "dana@example.com" || profile.Name != "Dana" {
		t.Fatalf("FetchUserInfo: %+v err=%v", profile, err)
	}
	if gotAuth != "Bearer tok-1" {
		t.Fatalf("userinfo should receive the bearer, got %q", gotAuth)
	}
	// Second call reuses the discovered endpoint; disabling per issuer works.
	if _, err := client.FetchUserInfo(context.Background(), srv.URL, "tok-1"); err != nil {
		t.Fatalf("second fetch: %v", err)
	}
	off := false
	vOff := newValidatorForIdP(t, idp, adminConfig, func(c *Config) { c.Issuer = srv.URL; c.UserInfo = &off })
	if _, err := NewUserInfoClient(vOff, srv.Client()).FetchUserInfo(context.Background(), srv.URL, "tok-1"); !errors.Is(err, ErrUserInfoDisabled) {
		t.Fatalf("expected ErrUserInfoDisabled, got %v", err)
	}
}
