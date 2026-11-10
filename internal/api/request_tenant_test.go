package api

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/validmind/atryum/internal/auth"
	"github.com/validmind/atryum/pkg/tenant"
)

// keyTenants resolves an agent key only in the tenant that holds it, as
// row-level security would.
type keyTenants map[string]string // key -> tenant

func (k keyTenants) ResolveAgentKey(ctx context.Context, token string) (auth.Identity, error) {
	if k[token] != tenant.From(ctx) {
		return auth.Identity{}, errors.New("unknown key")
	}
	return auth.Identity{AgentID: "agent-" + k[token], Method: auth.IdentityMethodAPIKey}, nil
}

// An agent route asks SetRequestTenant's function first, so the key lookup
// and the handler run as the tenant it names; a request it can't place gets
// 401 before any lookup.
func TestRequestTenantScopesAgentRoutes(t *testing.T) {
	keys := keyTenants{"atr_acme": "acme", "atr_bank": "bank"}
	h := &Handler{keyResolver: keys}
	h.SetRequestTenant(func(r *http.Request) (string, error) {
		switch r.Header.Get("Authorization") {
		case "Bearer atr_acme":
			return "acme", nil
		case "Bearer atr_bank":
			return "bank", nil
		case "Bearer atr_unplaced":
			return "", nil
		case "Bearer atr_panic":
			panic("hook bug")
		}
		return "", errors.New("unknown issuer")
	})
	var ran string
	route := h.agentRuntimeHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ran = tenant.From(r.Context()) + "/" + auth.AgentIDFromContext(r.Context())
	}))
	var logged bytes.Buffer
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	for _, c := range []struct {
		bearer, ran string
		code        int
		logs        string // a hook that fails is logged; an unknown credential isn't
	}{
		{"atr_acme", "acme/agent-acme", http.StatusOK, ""},
		{"atr_bank", "bank/agent-bank", http.StatusOK, ""},
		{"atr_unplaced", "", http.StatusUnauthorized, ""},
		{"atr_other", "", http.StatusUnauthorized, "unknown issuer"},
		{"atr_panic", "", http.StatusUnauthorized, "panicked: hook bug"},
	} {
		ran = ""
		logged.Reset()
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/api/v1/external/invocations", nil)
		r.Header.Set("Authorization", "Bearer "+c.bearer)
		route.ServeHTTP(w, r)
		if w.Code != c.code || ran != c.ran {
			t.Errorf("%s: %d, ran %q; want %d, %q", c.bearer, w.Code, ran, c.code, c.ran)
		}
		if got := logged.String(); c.logs == "" && got != "" || !strings.Contains(got, c.logs) {
			t.Errorf("%s: logged %q, want %q", c.bearer, got, c.logs)
		}
	}
}

// The client info an initialize leaves is the tenant's own: another tenant's
// request from the same address and User-Agent doesn't find it.
func TestClientInfoIsPerTenant(t *testing.T) {
	h := &Handler{clientInfoCache: map[string]clientInfoSnapshot{}}
	req := func(id string) *http.Request {
		r := httptest.NewRequest("POST", "/mcp/shared", nil)
		r.RemoteAddr = "10.0.0.1:443" // the ingress, for every client
		r.Header.Set("User-Agent", "mcp-sdk")
		return r.WithContext(tenant.With(r.Context(), id))
	}
	h.recordInitializeClientInfo(req("acme"), []byte(`{"clientInfo": {"name": "AcmeOnlyClient", "version": "1"}}`))
	if snap, ok := h.lookupClientInfo(req("bank")); ok {
		t.Fatalf("bank found acme's client info: %+v", snap)
	}
	if snap, ok := h.lookupClientInfo(req("acme")); !ok || snap.Name != "AcmeOnlyClient" {
		t.Fatalf("acme lost its own client info: %+v %v", snap, ok)
	}
}

// With an IdP configured, the 401 for a request the hook can't place keeps
// the challenge an MCP client follows to find where to sign in.
func TestRequestTenantRefusalKeepsTheChallenge(t *testing.T) {
	v, err := auth.NewValidator([]auth.Config{{Enabled: true, Issuer: "https://idp.test", Audience: "atryum", JWKSURL: "http://127.0.0.1:1"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{authValidator: v}
	h.SetRequestTenant(func(*http.Request) (string, error) { return "", nil })
	route := h.agentRuntimeHandler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("an unplaced request reached the route")
	}))
	w := httptest.NewRecorder()
	route.ServeHTTP(w, httptest.NewRequest("POST", "/mcp/shared", nil))
	if c := w.Header().Get("WWW-Authenticate"); w.Code != http.StatusUnauthorized || !strings.Contains(c, `resource_metadata="http://example.com/.well-known/oauth-protected-resource"`) {
		t.Fatalf("%d, WWW-Authenticate %q; want 401 with the resource metadata", w.Code, c)
	}
}
