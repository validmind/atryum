package auth

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	jwt "github.com/golang-jwt/jwt/v5"
)

// testIdP stands up an in-memory IdP that serves a JWKS document for an RSA
// signing key. It can also mint signed JWTs with arbitrary claims so tests
// can exercise every validation path without external dependencies.
type testIdP struct {
	server *httptest.Server
	priv   *rsa.PrivateKey
	kid    string
}

func newTestIdP(t *testing.T) *testIdP {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate rsa: %v", err)
	}
	idp := &testIdP{priv: priv, kid: "test-key-1"}
	idp.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/jwks.json") {
			http.NotFound(w, r)
			return
		}
		nB := base64.RawURLEncoding.EncodeToString(priv.PublicKey.N.Bytes())
		eBytes := big.NewInt(int64(priv.PublicKey.E)).Bytes()
		eB := base64.RawURLEncoding.EncodeToString(eBytes)
		_ = json.NewEncoder(w).Encode(jwkSet{Keys: []jwk{{
			Kty: "RSA", Kid: idp.kid, Alg: "RS256", Use: "sig", N: nB, E: eB,
		}}})
	}))
	t.Cleanup(idp.server.Close)
	return idp
}

func (idp *testIdP) jwksURL() string { return idp.server.URL + "/jwks.json" }

func (idp *testIdP) sign(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = idp.kid
	signed, err := token.SignedString(idp.priv)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return signed
}

// signWithKid lets a test override the kid header (used to simulate an
// unknown signing key).
func (idp *testIdP) signWithKid(t *testing.T, claims jwt.MapClaims, kid string) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = kid
	signed, err := token.SignedString(idp.priv)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return signed
}

func validClaims() jwt.MapClaims {
	now := time.Now()
	return jwt.MapClaims{
		"iss":       "https://idp.example/",
		"aud":       "atryum",
		"sub":       "agent-subject",
		"client_id": "agent-1",
		"scope":     "atryum:mcp other:scope",
		"iat":       now.Unix(),
		"exp":       now.Add(5 * time.Minute).Unix(),
	}
}

func newValidatorForIdP(t *testing.T, idp *testIdP, overrides ...func(*Config)) *Validator {
	t.Helper()
	c := Config{
		Enabled:       true,
		Issuer:        "https://idp.example/",
		Audience:      "atryum",
		JWKSURL:       idp.jwksURL(),
		RequiredScope: "atryum:mcp",
		AgentIDClaim:  "client_id",
	}
	for _, override := range overrides {
		override(&c)
	}
	v, err := NewValidator([]Config{c}, nil)
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	return v
}

func TestValidatorMissingToken(t *testing.T) {
	idp := newTestIdP(t)
	v := newValidatorForIdP(t, idp)
	_, err := v.Validate(context.Background(), "")
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.Result != ResultMissing {
		t.Fatalf("expected ResultMissing, got %v", err)
	}
}

func TestValidatorMalformedToken(t *testing.T) {
	idp := newTestIdP(t)
	v := newValidatorForIdP(t, idp)
	_, err := v.Validate(context.Background(), "not-a-jwt")
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.Result != ResultInvalid {
		t.Fatalf("expected ResultInvalid, got %v", err)
	}
}

func TestValidatorExpiredToken(t *testing.T) {
	idp := newTestIdP(t)
	v := newValidatorForIdP(t, idp)
	claims := validClaims()
	claims["exp"] = time.Now().Add(-1 * time.Hour).Unix()
	claims["iat"] = time.Now().Add(-2 * time.Hour).Unix()
	tok := idp.sign(t, claims)
	_, err := v.Validate(context.Background(), tok)
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.Result != ResultInvalid {
		t.Fatalf("expected ResultInvalid for expired, got %v", err)
	}
	if !strings.Contains(ve.Description, "expired") {
		t.Fatalf("expected expired description, got %q", ve.Description)
	}
}

func TestValidatorWrongIssuer(t *testing.T) {
	idp := newTestIdP(t)
	v := newValidatorForIdP(t, idp)
	claims := validClaims()
	claims["iss"] = "https://attacker.example/"
	tok := idp.sign(t, claims)
	_, err := v.Validate(context.Background(), tok)
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.Result != ResultInvalid {
		t.Fatalf("expected ResultInvalid for issuer mismatch, got %v", err)
	}
}

func TestValidatorWrongAudience(t *testing.T) {
	idp := newTestIdP(t)
	v := newValidatorForIdP(t, idp)
	claims := validClaims()
	claims["aud"] = "someone-else"
	tok := idp.sign(t, claims)
	_, err := v.Validate(context.Background(), tok)
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.Result != ResultInvalid {
		t.Fatalf("expected ResultInvalid for audience mismatch, got %v", err)
	}
}

func TestValidatorSelectsConfigByIssuerAndAudience(t *testing.T) {
	idp := newTestIdP(t)
	configs := []Config{
		{
			Enabled:       true,
			Issuer:        "https://idp.example/",
			Audience:      "other-api",
			JWKSURL:       idp.jwksURL(),
			RequiredScope: "other:scope",
			AgentIDClaim:  "sub",
		},
		{
			Enabled:       true,
			Issuer:        "https://idp.example/",
			Audience:      "atryum",
			JWKSURL:       idp.jwksURL(),
			RequiredScope: "atryum:mcp",
			AgentIDClaim:  "client_id",
		},
	}
	v, err := NewValidator(configs, nil)
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	tok := idp.sign(t, validClaims())
	id, err := v.Validate(context.Background(), tok)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if id.AgentID != "agent-1" {
		t.Fatalf("expected config matching atryum audience, got agent id %q", id.AgentID)
	}
}

func TestValidatorMissingScope(t *testing.T) {
	idp := newTestIdP(t)
	v := newValidatorForIdP(t, idp)
	claims := validClaims()
	claims["scope"] = "other:scope"
	tok := idp.sign(t, claims)
	_, err := v.Validate(context.Background(), tok)
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.Result != ResultMissingScope {
		t.Fatalf("expected ResultMissingScope, got %v", err)
	}
}

func TestValidatorAllowsMissingTokenScopeWhenRequiredScopeEmpty(t *testing.T) {
	idp := newTestIdP(t)
	v := newValidatorForIdP(t, idp, func(c *Config) { c.RequiredScope = "" })
	claims := validClaims()
	delete(claims, "scope")
	delete(claims, "scp")
	tok := idp.sign(t, claims)
	id, err := v.Validate(context.Background(), tok)
	if err != nil {
		t.Fatalf("expected ok without token scope, got %v", err)
	}
	if id.AgentID != "agent-1" {
		t.Fatalf("agent id = %q", id.AgentID)
	}
	if id.Scope != "" {
		t.Fatalf("scope = %q", id.Scope)
	}
}

func TestValidatorScpClaimArrayAccepted(t *testing.T) {
	idp := newTestIdP(t)
	v := newValidatorForIdP(t, idp)
	claims := validClaims()
	delete(claims, "scope")
	claims["scp"] = []any{"foo", "atryum:mcp"}
	tok := idp.sign(t, claims)
	id, err := v.Validate(context.Background(), tok)
	if err != nil {
		t.Fatalf("expected ok, got %v", err)
	}
	if id.AgentID != "agent-1" {
		t.Fatalf("agent id = %q", id.AgentID)
	}
}

func TestValidatorValidToken(t *testing.T) {
	idp := newTestIdP(t)
	v := newValidatorForIdP(t, idp)
	tok := idp.sign(t, validClaims())
	id, err := v.Validate(context.Background(), tok)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if id.AgentID != "agent-1" {
		t.Fatalf("AgentID = %q", id.AgentID)
	}
	if id.Issuer != "https://idp.example" {
		t.Fatalf("Issuer = %q", id.Issuer)
	}
}

func TestValidatorAgentIDFallbackToSub(t *testing.T) {
	idp := newTestIdP(t)
	v := newValidatorForIdP(t, idp, func(c *Config) { c.AgentIDClaim = "preferred_username" })
	claims := validClaims()
	delete(claims, "client_id")
	tok := idp.sign(t, claims)
	id, err := v.Validate(context.Background(), tok)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if id.AgentID != "agent-subject" {
		t.Fatalf("expected fallback to sub, got %q", id.AgentID)
	}
}

func TestValidatorUnknownKid(t *testing.T) {
	idp := newTestIdP(t)
	v := newValidatorForIdP(t, idp)
	tok := idp.signWithKid(t, validClaims(), "different-kid")
	_, err := v.Validate(context.Background(), tok)
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.Result != ResultInvalid {
		t.Fatalf("expected ResultInvalid, got %v", err)
	}
}

func TestRSAPublicKeyFromJWKRejectsOversizedExponent(t *testing.T) {
	_, err := rsaPublicKeyFromJWK(jwk{
		Kty: "RSA",
		Kid: "bad-exp",
		N:   base64.RawURLEncoding.EncodeToString([]byte{1}),
		E:   base64.RawURLEncoding.EncodeToString([]byte{1, 2, 3, 4, 5, 6, 7, 8, 9}),
	})
	if err == nil {
		t.Fatal("expected oversized exponent to be rejected")
	}
	if !strings.Contains(err.Error(), "invalid exponent") {
		t.Fatalf("expected invalid exponent error, got %v", err)
	}
}

func TestMiddlewareBlocksMissingToken(t *testing.T) {
	idp := newTestIdP(t)
	v := newValidatorForIdP(t, idp)
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })
	h := Middleware(v, "/.well-known/oauth-protected-resource")(next)

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/mcp/x", strings.NewReader("{}")))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
	chal := w.Header().Get("WWW-Authenticate")
	if !strings.Contains(chal, "Bearer") || !strings.Contains(chal, "resource_metadata=") {
		t.Fatalf("expected Bearer challenge with resource_metadata, got %q", chal)
	}
	if called {
		t.Fatal("next handler should not have been called")
	}
}

func TestMiddlewareReturns403ForMissingScope(t *testing.T) {
	idp := newTestIdP(t)
	v := newValidatorForIdP(t, idp)
	claims := validClaims()
	claims["scope"] = "wrong"
	tok := idp.sign(t, claims)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	h := Middleware(v, "/.well-known/oauth-protected-resource")(next)
	req := httptest.NewRequest(http.MethodPost, "/mcp/x", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", w.Code)
	}
	if !strings.Contains(w.Header().Get("WWW-Authenticate"), "insufficient_scope") {
		t.Fatalf("expected insufficient_scope challenge, got %q", w.Header().Get("WWW-Authenticate"))
	}
}

func TestMiddlewareLogsMissingScope(t *testing.T) {
	idp := newTestIdP(t)
	v := newValidatorForIdP(t, idp)
	claims := validClaims()
	claims["scope"] = "wrong"
	tok := idp.sign(t, claims)
	var logs bytes.Buffer
	origWriter := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(origWriter) })

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	h := Middleware(v, "/.well-known/oauth-protected-resource")(next)
	req := httptest.NewRequest(http.MethodPost, "/mcp/x", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+tok)
	h.ServeHTTP(httptest.NewRecorder(), req)

	got := logs.String()
	if !strings.Contains(got, "[auth] insufficient_scope") || !strings.Contains(got, `scope="atryum:mcp"`) {
		t.Fatalf("expected insufficient scope auth log, got %q", got)
	}
	if strings.Contains(got, tok) {
		t.Fatal("auth log should not include bearer token")
	}
}

func TestMiddlewareLogsInvalidToken(t *testing.T) {
	idp := newTestIdP(t)
	v := newValidatorForIdP(t, idp)
	var logs bytes.Buffer
	origWriter := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(origWriter) })

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	h := Middleware(v, "/.well-known/oauth-protected-resource")(next)
	req := httptest.NewRequest(http.MethodPost, "/mcp/x", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer not-a-jwt")
	h.ServeHTTP(httptest.NewRecorder(), req)

	got := logs.String()
	if !strings.Contains(got, "[auth] invalid_token") || !strings.Contains(got, `description="malformed token"`) {
		t.Fatalf("expected invalid token auth log, got %q", got)
	}
	if strings.Contains(got, "not-a-jwt") {
		t.Fatal("auth log should not include bearer token")
	}
}

func TestMiddlewareSetsIdentityOnContext(t *testing.T) {
	idp := newTestIdP(t)
	v := newValidatorForIdP(t, idp)
	tok := idp.sign(t, validClaims())
	var got Identity
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := IdentityFromContext(r.Context())
		if !ok {
			t.Fatal("identity not on context")
		}
		got = id
	})
	h := Middleware(v, "/.well-known/oauth-protected-resource")(next)
	req := httptest.NewRequest(http.MethodPost, "/mcp/x", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if got.AgentID != "agent-1" {
		t.Fatalf("expected agent-1, got %q", got.AgentID)
	}
}

func TestMiddlewareNilValidatorIsPassThrough(t *testing.T) {
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })
	h := Middleware(nil, "/x")(next)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/mcp/x", nil))
	if !called {
		t.Fatal("expected pass-through when validator is nil")
	}
}

func TestProtectedResourceMetadata(t *testing.T) {
	idp := newTestIdP(t)
	v := newValidatorForIdP(t, idp)
	h := ProtectedResourceHandler(v, "https://atryum.example/mcp")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/.well-known/oauth-protected-resource", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var doc ProtectedResourceMetadata
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if doc.Resource != "https://atryum.example/mcp" {
		t.Fatalf("resource = %q", doc.Resource)
	}
	if len(doc.AuthorizationServers) != 1 || doc.AuthorizationServers[0] != "https://idp.example" {
		t.Fatalf("unexpected authorization_servers: %#v", doc.AuthorizationServers)
	}
	if len(doc.ScopesSupported) != 1 || doc.ScopesSupported[0] != "atryum:mcp" {
		t.Fatalf("unexpected scopes: %#v", doc.ScopesSupported)
	}
}

func TestNewValidatorRejectsMissingFields(t *testing.T) {
	if _, err := NewValidator([]Config{{Enabled: true, Audience: "x"}}, nil); err == nil {
		t.Fatal("expected error for missing issuer")
	}
	if _, err := NewValidator([]Config{{Enabled: true, Issuer: "https://x"}}, nil); err == nil {
		t.Fatal("expected error for missing audience")
	}
	v, err := NewValidator(nil, nil)
	if err != nil || v != nil {
		t.Fatalf("expected nil validator/no-error for empty configs, got v=%v err=%v", v, err)
	}
}
