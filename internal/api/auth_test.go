package api

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	jwt "github.com/golang-jwt/jwt/v5"

	"github.com/validmind/atryum/internal/auth"
	"github.com/validmind/atryum/internal/invocation"
	"github.com/validmind/atryum/internal/store"
)

// jwksHandler serves a minimal JWKS document for the given RSA public key so
// the auth.Validator can verify signatures during tests.
func jwksHandler(pub *rsa.PublicKey, kid string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		nB := base64.RawURLEncoding.EncodeToString(pub.N.Bytes())
		eB := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes())
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]any{{
				"kty": "RSA", "kid": kid, "alg": "RS256", "use": "sig",
				"n": nB, "e": eB,
			}},
		})
	}
}

// authTestRig bundles the IdP server, RSA key, and a configured Validator so
// each test can mint tokens and assert routing/identity behavior.
type authTestRig struct {
	priv *rsa.PrivateKey
	idp  *httptest.Server
	v    *auth.Validator
	kid  string
}

func newAuthTestRig(t *testing.T, overrides ...func(*auth.Config)) *authTestRig {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa: %v", err)
	}
	rig := &authTestRig{priv: priv, kid: "k1"}
	rig.idp = httptest.NewServer(jwksHandler(&priv.PublicKey, rig.kid))
	t.Cleanup(rig.idp.Close)
	cfg := auth.Config{
		Enabled:       true,
		Issuer:        "https://idp.test",
		Audience:      "atryum",
		JWKSURL:       rig.idp.URL,
		RequiredScope: "atryum:mcp",
		AgentIDClaim:  "client_id",
	}
	for _, override := range overrides {
		override(&cfg)
	}
	v, err := auth.NewValidator([]auth.Config{cfg}, nil)
	if err != nil {
		t.Fatalf("validator: %v", err)
	}
	rig.v = v
	return rig
}

func (r *authTestRig) sign(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = r.kid
	signed, err := tok.SignedString(r.priv)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return signed
}

func defaultClaims() jwt.MapClaims {
	now := time.Now()
	return jwt.MapClaims{
		"iss":       "https://idp.test",
		"aud":       "atryum",
		"sub":       "subject-1",
		"client_id": "agent-007",
		"scope":     "atryum:mcp",
		"iat":       now.Unix(),
		"exp":       now.Add(5 * time.Minute).Unix(),
	}
}

func newAuthedHandler(t *testing.T, svc service, rig *authTestRig) http.Handler {
	t.Helper()
	h := NewHandler(svc, stubServerService{}, nil, nil, nil, nil, nil, nil, nil, nil)
	h.SetAuthValidator(rig.v)
	return h.Routes()
}

func TestMCPRequiresAuthWhenValidatorConfigured(t *testing.T) {
	rig := newAuthTestRig(t)
	h := newAuthedHandler(t, &stubService{}, rig)

	req := httptest.NewRequest(http.MethodPost, "/mcp/demo", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without bearer, got %d body=%s", w.Code, w.Body.String())
	}
	chal := w.Header().Get("WWW-Authenticate")
	if !strings.Contains(chal, "Bearer") {
		t.Fatalf("expected Bearer challenge, got %q", chal)
	}
	if !strings.Contains(chal, "resource_metadata=") {
		t.Fatalf("expected resource_metadata advertisement, got %q", chal)
	}
}

func TestMCPRejectsInvalidToken(t *testing.T) {
	rig := newAuthTestRig(t)
	h := newAuthedHandler(t, &stubService{}, rig)

	req := httptest.NewRequest(http.MethodPost, "/mcp/demo", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	req.Header.Set("Authorization", "Bearer not-a-jwt")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for malformed token, got %d", w.Code)
	}
}

func TestMCPRejectsTokenMissingScope(t *testing.T) {
	rig := newAuthTestRig(t)
	h := newAuthedHandler(t, &stubService{}, rig)
	claims := defaultClaims()
	claims["scope"] = "wrong"
	tok := rig.sign(t, claims)

	req := httptest.NewRequest(http.MethodPost, "/mcp/demo", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for insufficient scope, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestMCPAcceptsValidTokenAndPlumbsAgentID(t *testing.T) {
	rig := newAuthTestRig(t)
	now := time.Now().UTC()
	svc := &stubService{
		invoke: invocation.InvocationResponse{
			InvocationID: "inv_1", ServerName: "demo", ToolName: "demo_tool",
			Status: invocation.StatusSucceeded, Input: json.RawMessage(`{"a":1}`),
			SubmittedAt: now, CompletedAt: &now,
			Result: json.RawMessage(`{"content":[{"type":"text","text":"ok"}]}`),
		},
	}
	h := newAuthedHandler(t, svc, rig)

	tok := rig.sign(t, defaultClaims())
	req := httptest.NewRequest(http.MethodPost, "/mcp/demo", strings.NewReader(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"demo_tool","arguments":{"a":1}}}`))
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if svc.invokedReq == nil {
		t.Fatal("expected service.Invoke to be called")
	}
	id := auth.AgentIDFromContext(svc.invokedCtx)
	if id != "agent-007" {
		t.Fatalf("expected agent_id agent-007 on invoke ctx, got %q", id)
	}
}

func TestMCPPlanGetRejectsAnotherAuthenticatedAgentsPlan(t *testing.T) {
	rig := newAuthTestRig(t)
	now := time.Now().UTC()
	svc := &stubService{plan: invocation.Plan{
		PlanID:      "plan_other_agent",
		AgentID:     "agent-other",
		Goal:        "sensitive plan",
		Actions:     []invocation.PlanAction{{Tool: "Bash"}},
		Status:      invocation.PlanStatusApproved,
		Revision:    1,
		TTLSeconds:  3600,
		SubmittedAt: now,
	}}
	h := newAuthedHandler(t, svc, rig)

	req := httptest.NewRequest(http.MethodPost, "/mcp/demo", strings.NewReader(`{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"atryum.plan.get","arguments":{"plan_id":"plan_other_agent"}}}`))
	req.Header.Set("Authorization", "Bearer "+rig.sign(t, defaultClaims())) // agent-007
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected JSON-RPC response, got %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"plan not found"`) {
		t.Fatalf("expected ownership error, got %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), `"sensitive plan"`) {
		t.Fatalf("response leaked another agent's plan: %s", w.Body.String())
	}
}

func TestAgentRulesRequiresAuthAndUsesTokenAgentID(t *testing.T) {
	rig := newAuthTestRig(t)
	tokenAgent := store.AgentRecord{ID: "agent-cuid-007", AgentIDs: `["agent-007"]`}
	otherAgent := store.AgentRecord{ID: "agent-cuid-other", AgentIDs: `["other"]`}
	rules := &stubRulesRepo{rules: []store.Rule{
		{ID: "other-deny", Action: invocation.RuleActionAutoDeny, ServerPatterns: []string{"amp"}, ToolPatterns: []string{"Read"}, AgentCUIDs: []string{otherAgent.ID}, Enabled: true, Order: 0},
		{ID: "auto-rule", Action: invocation.RuleActionAutoApprove, ServerPatterns: []string{"amp"}, ToolPatterns: []string{"Read"}, AgentCUIDs: []string{tokenAgent.ID}, Enabled: true, Order: 1},
	}}
	agents := &stubAgentsRepo{records: []store.AgentRecord{tokenAgent, otherAgent}}
	h := NewHandler(&stubService{}, stubServerService{}, nil, rules, agents, nil, nil, nil, nil, nil)
	h.SetAuthValidator(rig.v)
	handler := h.Routes()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/agent/rules?source=amp&tool=Read", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without bearer, got %d body=%s", w.Code, w.Body.String())
	}

	tok := rig.sign(t, defaultClaims())
	req = httptest.NewRequest(http.MethodGet, "/api/v1/agent/rules?agent_id=other&source=amp&tool=Read", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 with bearer, got %d body=%s", w.Code, w.Body.String())
	}
	var resp AgentRulesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.AgentID != "agent-007" {
		t.Fatalf("expected token agent_id to win over query param, got %q", resp.AgentID)
	}
	if resp.Action != invocation.RuleActionAutoApprove {
		t.Fatalf("expected auto_approve action, got %q", resp.Action)
	}
	if len(resp.Items) != 1 || resp.Items[0].ID != "auto-rule" {
		t.Fatalf("expected only token agent rule to be visible, got %#v", resp.Items)
	}
}

func TestAgentRuntimeEndpointsRequireAuthWhenValidatorConfigured(t *testing.T) {
	rig := newAuthTestRig(t)
	h := newAuthedHandler(t, &stubService{}, rig)

	cases := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"external submit", http.MethodPost, "/api/v1/external/invocations", `{"tool":"Read","input":{}}`},
		{"external poll", http.MethodGet, "/api/v1/external/invocations/inv_123", ""},
		{"external patch", http.MethodPatch, "/api/v1/external/invocations/inv_123", `{"execution_status":"running"}`},
		{"direct submit", http.MethodPost, "/api/v1/invocations", `{"server":"demo","tool":"Read","input":{}}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(c.method, c.path, strings.NewReader(c.body))
			if c.body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401 without bearer, got %d body=%s", w.Code, w.Body.String())
			}
			if chal := w.Header().Get("WWW-Authenticate"); !strings.Contains(chal, "Bearer") {
				t.Fatalf("expected Bearer challenge, got %q", chal)
			}
		})
	}
}

func TestAgentRuntimeEndpointsAcceptValidToken(t *testing.T) {
	rig := newAuthTestRig(t)
	now := time.Now().UTC()
	svc := &stubService{invoke: invocation.InvocationResponse{
		InvocationID: "inv_123",
		ServerName:   "demo",
		ToolName:     "Read",
		Status:       invocation.StatusApproved,
		SubmittedAt:  now,
	}}
	h := newAuthedHandler(t, svc, rig)
	tok := rig.sign(t, defaultClaims())

	submitReq := httptest.NewRequest(http.MethodPost, "/api/v1/external/invocations", strings.NewReader(`{"source":"amp","tool":"Read","input":{},"agent_id":"spoofed"}`))
	submitReq.Header.Set("Content-Type", "application/json")
	submitReq.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, submitReq)
	if w.Code != http.StatusOK {
		t.Fatalf("external submit expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if svc.submitReq == nil || svc.submitReq.AgentID != "spoofed" {
		t.Fatalf("expected body agent_id to be passed to service, got %#v", svc.submitReq)
	}
	if got := auth.AgentIDFromContext(svc.submitCtx); got != "agent-007" {
		t.Fatalf("expected token agent_id on external submit ctx, got %q", got)
	}

	pollReq := httptest.NewRequest(http.MethodGet, "/api/v1/external/invocations/inv_123?agent_id=spoofed", nil)
	pollReq.Header.Set("Authorization", "Bearer "+tok)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, pollReq)
	if w.Code != http.StatusOK {
		t.Fatalf("external poll expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if got := auth.AgentIDFromContext(svc.getCtx); got != "agent-007" {
		t.Fatalf("expected token agent_id on external poll ctx, got %q", got)
	}

	patchReq := httptest.NewRequest(http.MethodPatch, "/api/v1/external/invocations/inv_123", strings.NewReader(`{"execution_status":"running"}`))
	patchReq.Header.Set("Content-Type", "application/json")
	patchReq.Header.Set("Authorization", "Bearer "+tok)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, patchReq)
	if w.Code != http.StatusOK {
		t.Fatalf("external patch expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if svc.recordID != "inv_123" || svc.recordReq == nil || svc.recordReq.ExecutionStatus != "running" {
		t.Fatalf("unexpected patch call id=%q req=%#v", svc.recordID, svc.recordReq)
	}
	if got := auth.AgentIDFromContext(svc.recordCtx); got != "agent-007" {
		t.Fatalf("expected token agent_id on external patch ctx, got %q", got)
	}

	directReq := httptest.NewRequest(http.MethodPost, "/api/v1/invocations?agent_id=spoofed", strings.NewReader(`{"server":"demo","tool":"Read","input":{}}`))
	directReq.Header.Set("Content-Type", "application/json")
	directReq.Header.Set("Authorization", "Bearer "+tok)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, directReq)
	if w.Code != http.StatusOK {
		t.Fatalf("direct submit expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if svc.invokedReq == nil || svc.invokedReq.Tool != "Read" {
		t.Fatalf("expected direct invocation service call, got %#v", svc.invokedReq)
	}
	if got := auth.AgentIDFromContext(svc.invokedCtx); got != "agent-007" {
		t.Fatalf("expected token agent_id on direct submit ctx, got %q", got)
	}
}

func TestUnprotectedRoutesRemainOpenWhenAuthEnabled(t *testing.T) {
	rig := newAuthTestRig(t)
	now := time.Now().UTC()
	svc := &stubService{invoke: invocation.InvocationResponse{InvocationID: "inv_1", ServerName: "demo", ToolName: "demo_tool", Status: invocation.StatusSucceeded, SubmittedAt: now}}
	h := newAuthedHandler(t, svc, rig)

	cases := []struct {
		method string
		path   string
		body   string
		want   int
	}{
		{http.MethodGet, "/healthz", "", http.StatusOK},
		{http.MethodGet, "/api/v1/review/invocations", "", http.StatusOK},
		{http.MethodGet, "/ui/", "", http.StatusOK},
	}
	for _, c := range cases {
		var body *strings.Reader
		if c.body != "" {
			body = strings.NewReader(c.body)
		} else {
			body = strings.NewReader("")
		}
		req := httptest.NewRequest(c.method, c.path, body)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != c.want {
			t.Errorf("%s %s: expected %d (no auth required), got %d body=%s", c.method, c.path, c.want, w.Code, w.Body.String())
		}
		if got := w.Header().Get("WWW-Authenticate"); got != "" {
			t.Errorf("%s %s: did not expect WWW-Authenticate, got %q", c.method, c.path, got)
		}
	}
}

func TestPrivilegedRoutesAcceptAdminTokenAndMachineCredentials(t *testing.T) {
	rig := newAuthTestRig(t, func(c *auth.Config) {
		c.AdminEnabled = true
		c.AdminClientID = "admin-client"
		c.AdminClaim = "atryum_admin"
		c.AdminClaimValue = "true"
	})
	handler := NewHandler(&stubService{}, stubServerService{}, nil, nil, nil, nil, nil, nil, nil, nil)
	handler.SetAuthValidator(rig.v)
	handler.SetAPIKeyAuth(auth.APIKeyConfig{Key: "vm-key", Secret: "vm-secret"})
	h := handler.Routes()

	paths := []string{
		"/api/v1/review/invocations",
		"/api/v1/servers",
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401 without credentials, got %d body=%s", w.Code, w.Body.String())
			}

			claims := defaultClaims()
			tok := rig.sign(t, claims)
			req = httptest.NewRequest(http.MethodGet, path, nil)
			req.Header.Set("Authorization", "Bearer "+tok)
			w = httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != http.StatusForbidden {
				t.Fatalf("expected 403 for non-admin token, got %d body=%s", w.Code, w.Body.String())
			}

			claims["atryum_admin"] = true
			tok = rig.sign(t, claims)
			req = httptest.NewRequest(http.MethodGet, path, nil)
			req.Header.Set("Authorization", "Bearer "+tok)
			w = httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("expected 200 for admin token, got %d body=%s", w.Code, w.Body.String())
			}

			req = httptest.NewRequest(http.MethodGet, path, nil)
			req.Header.Set("X-API-Key", "vm-key")
			req.Header.Set("X-API-Secret", "vm-secret")
			w = httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("expected 200 for machine credentials, got %d body=%s", w.Code, w.Body.String())
			}
		})
	}
}

func TestRenamedPrivilegedRoutesAreRegisteredAndProtected(t *testing.T) {
	rig := newAuthTestRig(t, func(c *auth.Config) {
		c.AdminEnabled = true
		c.AdminClientID = "admin-client"
		c.AdminClaim = "atryum_admin"
		c.AdminClaimValue = "true"
	})
	h := newAuthedHandler(t, &stubService{}, rig)
	for _, path := range []string{
		"/api/v1/review/invocations",
		"/api/v1/plans",
		"/api/v1/servers",
		"/api/v1/rules",
		"/api/v1/agents",
		"/api/v1/model-configs",
		"/api/v1/llm-configs",
		"/api/v1/settings",
		"/api/v1/vm/organizations",
		"/api/v1/vm/record-types",
		"/api/v1/vm/custom-fields",
		"/api/v1/policy",
		"/api/v1/managed-agents/accounts",
		"/api/v1/managed-agents/agents",
		"/api/v1/managed-agents/sessions",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("GET %s: status = %d, want 401", path, w.Code)
		}
	}
}

func TestAdminAuthConfigEndpointReturnsSafeProviderMetadata(t *testing.T) {
	rig := newAuthTestRig(t, func(c *auth.Config) {
		c.AdminEnabled = true
		c.AdminProvider = "auth0"
		c.AdminClientID = "admin-client"
		c.AdminScopes = "openid profile email"
	})
	h := newAuthedHandler(t, &stubService{}, rig)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/config", nil)
	req.Host = "atryum.example"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	var resp AdminAuthConfigResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Providers) != 1 {
		t.Fatalf("providers = %#v", resp.Providers)
	}
	provider := resp.Providers[0]
	if provider.ClientID != "admin-client" || provider.Audience != "atryum" || provider.Issuer != "https://idp.test" {
		t.Fatalf("unexpected provider metadata: %#v", provider)
	}
	if provider.RedirectURI != "http://atryum.example/ui/auth/callback" {
		t.Fatalf("redirect_uri = %q", provider.RedirectURI)
	}
}

func TestAdminAuthConfigProviderIDsAreUniqueForSharedClientID(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa: %v", err)
	}
	idp := httptest.NewServer(jwksHandler(&priv.PublicKey, "k1"))
	t.Cleanup(idp.Close)
	v, err := auth.NewValidator([]auth.Config{
		{
			Enabled:       true,
			Issuer:        "https://login.example/",
			Audience:      "atryum",
			JWKSURL:       idp.URL,
			AdminEnabled:  true,
			AdminClientID: "shared-client",
		},
		{
			Enabled:       true,
			Issuer:        "https://tenant.auth0.com/",
			Audience:      "atryum",
			JWKSURL:       idp.URL,
			AdminEnabled:  true,
			AdminClientID: "shared-client",
		},
	}, nil)
	if err != nil {
		t.Fatalf("validator: %v", err)
	}
	h := NewHandler(&stubService{}, stubServerService{}, nil, nil, nil, nil, nil, nil, nil, nil)
	h.SetAuthValidator(v)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/config", nil)
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	var resp AdminAuthConfigResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Providers) != 2 {
		t.Fatalf("providers = %#v", resp.Providers)
	}
	if resp.Providers[0].ID == resp.Providers[1].ID {
		t.Fatalf("expected unique provider IDs, got %q", resp.Providers[0].ID)
	}
}

func TestRenamedRoutesDoNotKeepLegacyAliases(t *testing.T) {
	h := newAuthedHandler(t, &stubService{}, newAuthTestRig(t))
	for _, path := range []string{
		"/api/v1/admin/invocations",
		"/api/v1/admin/invocations/inv_123",
		"/api/v1/admin/plans",
		"/api/v1/admin/servers",
		"/api/v1/admin/rules",
		"/api/v1/admin/agents",
		"/api/v1/admin/model-configs",
		"/api/v1/admin/llm-configs",
		"/api/v1/admin/settings",
		"/api/v1/admin/vm/organizations",
		"/api/v1/admin/vm/record-types",
		"/api/v1/admin/vm/custom-fields",
		"/api/v1/admin/policy",
		"/api/v1/admin/managed-agents/accounts",
		"/api/v1/admin/managed-agents/agents",
		"/api/v1/admin/managed-agents/sessions",
		"/api/v1/admin/managed-agents/sessions/session_123",
		"/api/v1/admin-auth/config",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Errorf("GET %s: status = %d, want 404", path, w.Code)
		}
	}
}

func TestProtectedResourceMetadataServed(t *testing.T) {
	rig := newAuthTestRig(t)
	h := newAuthedHandler(t, &stubService{}, rig)
	req := httptest.NewRequest(http.MethodGet, "/.well-known/oauth-protected-resource", nil)
	req.Host = "atryum.example"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var doc auth.ProtectedResourceMetadata
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatalf("decode: %v body=%s", err, w.Body.String())
	}
	if doc.Resource != "http://atryum.example/mcp" {
		t.Fatalf("resource = %q", doc.Resource)
	}
	if len(doc.AuthorizationServers) != 1 || doc.AuthorizationServers[0] != "https://idp.test" {
		t.Fatalf("authorization_servers = %#v", doc.AuthorizationServers)
	}
}

// Sanity: when no validator is configured, /mcp/ behaves as before.
func TestMCPNoValidatorPreservesAnonymousAccess(t *testing.T) {
	h := NewHandler(&stubService{}, stubServerService{}, nil, nil, nil, nil, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/mcp/demo", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 anonymous, got %d", w.Code)
	}
}

func TestAgentRuntimeNoValidatorPreservesAnonymousAccess(t *testing.T) {
	now := time.Now().UTC()
	svc := &stubService{invoke: invocation.InvocationResponse{InvocationID: "inv_1", ServerName: "demo", ToolName: "Read", Status: invocation.StatusApproved, SubmittedAt: now}}
	h := NewHandler(svc, stubServerService{}, nil, nil, nil, nil, nil, nil, nil, nil).Routes()

	cases := []struct {
		name   string
		method string
		path   string
		body   string
		want   int
	}{
		{"external submit", http.MethodPost, "/api/v1/external/invocations", `{"tool":"Read","input":{},"agent_id":"local-agent"}`, http.StatusOK},
		{"external poll", http.MethodGet, "/api/v1/external/invocations/inv_1", "", http.StatusOK},
		{"external patch", http.MethodPatch, "/api/v1/external/invocations/inv_1", `{"execution_status":"running"}`, http.StatusOK},
		{"direct submit", http.MethodPost, "/api/v1/invocations?agent_id=local-agent", `{"server":"demo","tool":"Read","input":{}}`, http.StatusOK},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(c.method, c.path, strings.NewReader(c.body))
			if c.body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != c.want {
				t.Fatalf("expected %d anonymous, got %d body=%s", c.want, w.Code, w.Body.String())
			}
			if got := w.Header().Get("WWW-Authenticate"); got != "" {
				t.Fatalf("did not expect WWW-Authenticate, got %q", got)
			}
		})
	}
	if got := auth.AgentIDFromContext(svc.invokedCtx); got != "local-agent" {
		t.Fatalf("expected no-auth query agent_id on direct submit ctx, got %q", got)
	}
}

func TestProtectedResourceMetadataNotServedWhenAuthDisabled(t *testing.T) {
	h := NewHandler(&stubService{}, stubServerService{}, nil, nil, nil, nil, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/.well-known/oauth-protected-resource", nil)
	req.Host = "atryum.example"
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 when auth disabled, got %d body=%s", w.Code, w.Body.String())
	}
}
