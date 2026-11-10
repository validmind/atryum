package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
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
		}
		return "", errors.New("unknown issuer")
	})
	var ran string
	route := h.agentRuntimeHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ran = tenant.From(r.Context()) + "/" + auth.AgentIDFromContext(r.Context())
	}))
	for _, c := range []struct {
		bearer, ran string
		code        int
	}{
		{"atr_acme", "acme/agent-acme", http.StatusOK},
		{"atr_bank", "bank/agent-bank", http.StatusOK},
		{"atr_unplaced", "", http.StatusUnauthorized},
		{"atr_other", "", http.StatusUnauthorized},
	} {
		ran = ""
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/api/v1/external/invocations", nil)
		r.Header.Set("Authorization", "Bearer "+c.bearer)
		route.ServeHTTP(w, r)
		if w.Code != c.code || ran != c.ran {
			t.Errorf("%s: %d, ran %q; want %d, %q", c.bearer, w.Code, ran, c.code, c.ran)
		}
	}
}
