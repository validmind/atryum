package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// With a decider set, only agent-facing routes exist: the operator API, the
// UI and the reporting endpoints are not mounted at all.
func TestAgentRoutesOnly(t *testing.T) {
	h := NewHandler(&stubService{}, stubServerService{}, nil, nil, nil, nil, nil, nil, nil, nil)
	h.UseExternalDecider()
	h.AddExtraRoutes(func(mux *http.ServeMux) {
		mux.HandleFunc("/api/v1/events", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	})
	routes := h.Routes()
	for path, want := range map[string]int{
		"/healthz":                     http.StatusOK,
		"/api/v1/events":               http.StatusTeapot,
		"/api/v1/external/invocations": http.StatusMethodNotAllowed,
		"/api/v1/rules":                http.StatusNotFound,
		"/api/v1/review/invocations":   http.StatusNotFound,
		"/api/v1/servers":              http.StatusNotFound,
		"/api/v1/agent/rules":          http.StatusNotFound,
		"/api/v1/plans":                http.StatusNotFound,
		"/agent_ids":                   http.StatusNotFound,
		"/ui/":                         http.StatusNotFound,
		"/":                            http.StatusNotFound,
	} {
		rec := httptest.NewRecorder()
		routes.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != want {
			t.Errorf("GET %s = %d, want %d", path, rec.Code, want)
		}
	}
}

// Under an external decider the MCP proxy does not point agents at Atryum's
// own rules tools.
func TestExternalDeciderHidesAtryumRulesFromMCP(t *testing.T) {
	h := NewHandler(&stubService{}, stubServerService{}, nil, nil, nil, nil, nil, nil, nil, nil)
	h.UseExternalDecider()
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp/demo", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)))
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "atryum_rules") {
		t.Fatalf("initialize = %d %s", rec.Code, rec.Body.String())
	}
}

// With RequireAgentIdentity, an agent route without a verified identity is
// refused, and the ?agent_id= hint does not stand in for one.
func TestRequireAgentIdentity(t *testing.T) {
	h := NewHandler(&stubService{}, stubServerService{}, nil, nil, nil, nil, nil, nil, nil, nil)
	h.UseExternalDecider()
	h.RequireAgentIdentity()
	routes := h.Routes()
	for _, path := range []string{
		"/api/v1/external/invocations?agent_id=agent-a",
		"/api/v1/external/invocations/inv_1?agent_id=agent-a",
		"/api/v1/external/sessions",
		"/api/v1/invocations",
		"/mcp/demo?agent_id=agent-a",
	} {
		rec := httptest.NewRecorder()
		routes.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"agent_id":"agent-a","tool":"Bash"}`)))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("POST %s = %d, want 401", path, rec.Code)
		}
	}
}
