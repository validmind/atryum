package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/validmind/atryum/internal/auth"
)

const testOperatorToken = "test-operator-token"

func operatorTokenHandler(token string) http.Handler {
	h := NewHandler(&stubService{}, stubServerService{}, nil, nil, nil, nil, nil, nil, nil, nil)
	h.SetAPIKeyAuth(auth.APIKeyConfig{Key: "machine-key", Secret: "machine-secret"})
	h.RequireOperatorToken(token)
	return h.Routes()
}

func serve(t *testing.T, routes http.Handler, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	routes.ServeHTTP(rec, req)
	return rec
}

func TestOperatorAPIRequiresOperatorTokenWhenConfigured(t *testing.T) {
	routes := operatorTokenHandler(testOperatorToken)

	cases := []struct {
		name   string
		mutate func(*http.Request)
		want   int
	}{
		{"no credentials", func(*http.Request) {}, http.StatusUnauthorized},
		{"wrong bearer", func(r *http.Request) { r.Header.Set("Authorization", "Bearer nope") }, http.StatusUnauthorized},
		{"operator token bearer", func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+testOperatorToken) }, http.StatusOK},
		{"wrong cookie", func(r *http.Request) { r.AddCookie(&http.Cookie{Name: auth.OperatorTokenCookie, Value: "nope"}) }, http.StatusUnauthorized},
		{"operator cookie", func(r *http.Request) {
			r.AddCookie(&http.Cookie{Name: auth.OperatorTokenCookie, Value: testOperatorToken})
		}, http.StatusOK},
		{"machine key", func(r *http.Request) {
			r.Header.Set("X-API-Key", "machine-key")
			r.Header.Set("X-API-Secret", "machine-secret")
		}, http.StatusOK},
		{"wrong machine key", func(r *http.Request) {
			r.Header.Set("X-API-Key", "machine-key")
			r.Header.Set("X-API-Secret", "wrong")
		}, http.StatusUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, path := range []string{"/api/v1/servers", "/api/v1/review/invocations", "/api/v1/me"} {
				req := httptest.NewRequest(http.MethodGet, path, nil)
				tc.mutate(req)
				if rec := serve(t, routes, req); rec.Code != tc.want {
					t.Fatalf("GET %s = %d, want %d (body %s)", path, rec.Code, tc.want, rec.Body.String())
				}
			}
		})
	}
}

func TestOperatorAPIStaysOpenWithoutOperatorToken(t *testing.T) {
	routes := operatorTokenHandler("")
	if rec := serve(t, routes, httptest.NewRequest(http.MethodGet, "/api/v1/servers", nil)); rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/servers = %d, want 200 when no operator token is set", rec.Code)
	}
	rec := serve(t, routes, httptest.NewRequest(http.MethodPost, "/api/v1/auth/operator-token", strings.NewReader(`{"token":"x"}`)))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("operator-token sign-in = %d, want 404 when not enabled", rec.Code)
	}
}

func TestAuthConfigAdvertisesOperatorToken(t *testing.T) {
	for _, tc := range []struct {
		token string
		want  bool
	}{{testOperatorToken, true}, {"", false}} {
		rec := serve(t, operatorTokenHandler(tc.token), httptest.NewRequest(http.MethodGet, "/api/v1/auth/config", nil))
		var resp AuthConfigResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if resp.OperatorTokenRequired != tc.want {
			t.Fatalf("token %q: operator_token_required = %t, want %t", tc.token, resp.OperatorTokenRequired, tc.want)
		}
	}
}

func operatorCookie(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.OperatorTokenCookie {
			return c
		}
	}
	return nil
}

func TestOperatorTokenSignInSetsStrictHTTPOnlyCookie(t *testing.T) {
	routes := operatorTokenHandler(testOperatorToken)

	bad := serve(t, routes, httptest.NewRequest(http.MethodPost, "/api/v1/auth/operator-token", strings.NewReader(`{"token":"nope"}`)))
	if bad.Code != http.StatusUnauthorized || operatorCookie(t, bad) != nil {
		t.Fatalf("wrong token: code %d, cookie %v; want 401 and no cookie", bad.Code, operatorCookie(t, bad))
	}

	good := serve(t, routes, httptest.NewRequest(http.MethodPost, "/api/v1/auth/operator-token", strings.NewReader(`{"token":"`+testOperatorToken+`"}`)))
	if good.Code != http.StatusNoContent {
		t.Fatalf("sign in = %d, want 204", good.Code)
	}
	cookie := operatorCookie(t, good)
	if cookie == nil || cookie.Value != testOperatorToken || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" {
		t.Fatalf("cookie = %+v, want HttpOnly SameSite=Strict Path=/ with the token", cookie)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/servers", nil)
	req.AddCookie(cookie)
	if rec := serve(t, routes, req); rec.Code != http.StatusOK {
		t.Fatalf("GET with sign-in cookie = %d, want 200", rec.Code)
	}

	out := serve(t, routes, httptest.NewRequest(http.MethodDelete, "/api/v1/auth/operator-token", nil))
	if c := operatorCookie(t, out); out.Code != http.StatusNoContent || c == nil || c.MaxAge >= 0 {
		t.Fatalf("sign out: code %d cookie %+v, want 204 and an expired cookie", out.Code, c)
	}
}

func TestOperatorTokenSignInLinkSetsCookieAndDropsTokenFromURL(t *testing.T) {
	routes := operatorTokenHandler(testOperatorToken)

	rec := serve(t, routes, httptest.NewRequest(http.MethodGet, "/ui/?token="+testOperatorToken, nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("sign-in link = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/ui/" {
		t.Fatalf("redirect = %q, want /ui/ without the token", loc)
	}
	if c := operatorCookie(t, rec); c == nil || c.Value != testOperatorToken {
		t.Fatalf("sign-in link did not set the operator cookie")
	}

	wrong := serve(t, routes, httptest.NewRequest(http.MethodGet, "/ui/servers?token=nope&x=1", nil))
	if wrong.Code != http.StatusFound || operatorCookie(t, wrong) != nil {
		t.Fatalf("wrong token link: code %d, want 302 and no cookie", wrong.Code)
	}
	if loc := wrong.Header().Get("Location"); strings.Contains(loc, "token") {
		t.Fatalf("redirect kept the token: %q", loc)
	}
}
