package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/validmind/atryum/internal/auth"
	"github.com/validmind/atryum/internal/invocation"
)

const onBehalfBody = `{"source":"payments","tool":"transfer","input":{"amount":750},
	"on_behalf_of":{"app_agent":"money-assistant","token":"eyJ.x.y",
	"end_user":{"id":"u_1","email":"dana@example.com","roles":["retail_customer"],"tenant":"t-a"}}}`

func submitAs(t *testing.T, id *auth.Identity, body string, rejected *[]AssertionRejection) (*stubService, *httptest.ResponseRecorder) {
	t.Helper()
	svc := &stubService{}
	h := NewHandler(svc, stubServerService{}, nil, nil, nil, nil, nil, nil, nil, nil)
	h.SetAssertionRejected(func(_ context.Context, r AssertionRejection) error {
		if rejected != nil {
			*rejected = append(*rejected, r)
		}
		return nil
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/external/invocations", strings.NewReader(body))
	req.Header.Set("content-type", "application/json")
	if id != nil {
		req = req.WithContext(auth.WithIdentity(req.Context(), *id))
	}
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)
	return svc, w
}

// Only a credential with the "app" capability may act for an end user; any
// other is refused before a call exists, and the refusal is reported.
func TestOnBehalfOfNeedsTheAppCapability(t *testing.T) {
	for _, tc := range []struct {
		name string
		id   *auth.Identity
	}{
		{"plain agent key", &auth.Identity{AgentID: "agent-1", Method: auth.IdentityMethodAPIKey, KeyID: "k1"}},
		{"gateway key", &auth.Identity{AgentID: "gw", Method: auth.IdentityMethodAPIKey, Capabilities: []string{auth.CapabilityAsserter}}},
		{"no identity", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var rejected []AssertionRejection
			svc, w := submitAs(t, tc.id, onBehalfBody, &rejected)
			if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), `"code":"not_an_asserter"`) {
				t.Fatalf("got %d %s", w.Code, w.Body)
			}
			if svc.submitReq != nil {
				t.Fatal("a refused claim reached Submit")
			}
			if len(rejected) != 1 || rejected[0].Path != "/api/v1/external/invocations" {
				t.Fatalf("rejections = %+v", rejected)
			}
		})
	}
}

// An app credential's claim becomes the asserted identity; By comes from the
// credential, never the body, and the token reaches the decider only.
func TestOnBehalfOfBecomesAsserted(t *testing.T) {
	id := auth.Identity{AgentID: "app-bank", Method: auth.IdentityMethodAPIKey, Capabilities: []string{auth.CapabilityApp}}
	svc, w := submitAs(t, &id, strings.Replace(onBehalfBody, `"tool"`, `"by":"evil","tool"`, 1), nil)
	if w.Code != http.StatusOK {
		t.Fatalf("got %d %s", w.Code, w.Body)
	}
	a := svc.submitReq.Asserted
	if a == nil || a.By != "app-bank" || a.Kind != invocation.AssertedKindApp || a.UserID != "u_1" || a.AppAgent != "money-assistant" ||
		a.EUTenant != "t-a" || !a.HasEUTenant || a.Email != "dana@example.com" || len(a.Roles) != 1 || a.Token != "eyJ.x.y" {
		t.Fatalf("asserted = %+v", a)
	}
	if a.UserKey != invocation.EndUserKey("app-bank", "t-a", true, "u_1") {
		t.Fatalf("user key = %q", a.UserKey)
	}
	b, _ := json.Marshal(a)
	if strings.Contains(string(b), "eyJ") || strings.Contains(string(b), a.UserKey) {
		t.Fatalf("token or key serialized: %s", b)
	}
}

func TestOnBehalfOfValidation(t *testing.T) {
	id := auth.Identity{AgentID: "app-bank", Capabilities: []string{auth.CapabilityApp}}
	long := strings.Repeat("x", invocation.MaxEndUserIDLen+1)
	for _, tc := range []struct{ name, block, field string }{
		{"missing id", `{"end_user":{"email":"a@b.co"}}`, "on_behalf_of.end_user.id"},
		{"id too long", `{"end_user":{"id":"` + long + `"}}`, "on_behalf_of.end_user.id"},
		{"bad email", `{"end_user":{"id":"u","email":"not an email"}}`, "on_behalf_of.end_user.email"},
		{"empty role", `{"end_user":{"id":"u","roles":["ok",""]}}`, "on_behalf_of.end_user.roles[1]"},
		{"control char tenant", `{"end_user":{"id":"u","tenant":"a\u0007b"}}`, "on_behalf_of.end_user.tenant"},
		{"token without end user", `{"token":"x"}`, "on_behalf_of.end_user"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, w := submitAs(t, &id, `{"source":"s","tool":"t","input":{},"on_behalf_of":`+tc.block+`}`, nil)
			var body struct{ Code, Field string }
			_ = json.Unmarshal(w.Body.Bytes(), &body)
			if w.Code != http.StatusBadRequest || body.Code != "invalid_on_behalf_of" || body.Field != tc.field {
				t.Fatalf("got %d %s", w.Code, w.Body)
			}
			if svc.submitReq != nil {
				t.Fatal("an invalid claim reached Submit")
			}
		})
	}
}

// Without on_behalf_of nothing changes, whatever the credential.
func TestNoOnBehalfOfIsUnchanged(t *testing.T) {
	svc, w := submitAs(t, &auth.Identity{AgentID: "agent-1"}, `{"source":"s","tool":"t","input":{}}`, nil)
	if w.Code != http.StatusOK || svc.submitReq == nil || svc.submitReq.Asserted != nil {
		t.Fatalf("got %d, asserted %+v", w.Code, svc.submitReq)
	}
}

func TestOnBehalfOfFromMCPHeaders(t *testing.T) {
	hdr := http.Header{}
	if o, _ := onBehalfOfFromHeaders(hdr); o != nil {
		t.Fatalf("no headers gave %+v", o)
	}
	hdr.Set(headerEndUser, "u_1")
	hdr.Set(headerEndUserRoles, " a, b ,,")
	hdr.Set(headerEndUserTenant, "")
	hdr.Set(headerAppAgent, "money-assistant")
	hdr.Set(headerWaitMS, "1500")
	o, wait := onBehalfOfFromHeaders(hdr)
	if o == nil || o.EndUser == nil || o.EndUser.ID != "u_1" || len(o.EndUser.Roles) != 2 || o.EndUser.Tenant == nil || *o.EndUser.Tenant != "" || o.AppAgent != "money-assistant" || wait != 1500 {
		t.Fatalf("got %+v wait %d", o, wait)
	}
}

// On the MCP proxy, a claim from a credential without "app" is a JSON-RPC
// error and no call is made.
func TestMCPEndUserHeadersNeedTheAppCapability(t *testing.T) {
	svc := &stubService{}
	h := NewHandler(svc, stubServerService{}, nil, nil, nil, nil, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/mcp/demo", strings.NewReader(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"transfer","arguments":{}}}`))
	req.Header.Set(headerEndUser, "u_1")
	req = req.WithContext(auth.WithIdentity(req.Context(), auth.Identity{AgentID: "agent-1"}))
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)
	if svc.invokedReq != nil || !strings.Contains(w.Body.String(), "not_an_asserter") {
		t.Fatalf("invoked %+v, body %s", svc.invokedReq, w.Body)
	}

	svc = &stubService{invoke: invocation.InvocationResponse{InvocationID: "inv_1", Status: invocation.StatusSucceeded, Result: json.RawMessage(`{"content":[]}`)}}
	h = NewHandler(svc, stubServerService{}, nil, nil, nil, nil, nil, nil, nil, nil)
	req = httptest.NewRequest(http.MethodPost, "/mcp/demo", strings.NewReader(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"transfer","arguments":{}}}`))
	req.Header.Set(headerEndUser, "u_1")
	req = req.WithContext(auth.WithIdentity(req.Context(), auth.Identity{AgentID: "app-bank", Capabilities: []string{auth.CapabilityApp}}))
	h.Routes().ServeHTTP(httptest.NewRecorder(), req)
	if svc.invokedReq == nil || svc.invokedReq.Asserted == nil || svc.invokedReq.Asserted.UserID != "u_1" || svc.invokedReq.Asserted.By != "app-bank" {
		t.Fatalf("invoked %+v", svc.invokedReq)
	}
}

// An embedder's agent routes are mounted behind agent authentication, with
// or without an external decider.
func TestAgentRoutesMounted(t *testing.T) {
	for _, external := range []bool{false, true} {
		h := NewHandler(&stubService{}, stubServerService{}, nil, nil, nil, nil, nil, nil, nil, nil)
		if external {
			h.UseExternalDecider()
		}
		var seen string
		h.AddAgentRoutes(func(mux *http.ServeMux, authenticate func(http.Handler) http.Handler) {
			mux.Handle("GET /api/v1/app/ping", authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				id, _ := auth.IdentityFromContext(r.Context())
				seen = id.AgentID
			})))
		})
		h.RequireAgentIdentity()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/app/ping", nil)
		w := httptest.NewRecorder()
		h.Routes().ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized || seen != "" {
			t.Fatalf("external=%v: unauthenticated got %d", external, w.Code)
		}
	}
}
