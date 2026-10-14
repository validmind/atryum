package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// With a decider set, only agent-facing routes exist: the operator API, the
// UI and the reporting endpoints are not mounted at all.
func TestAgentRoutesOnly(t *testing.T) {
	h := NewHandler(&stubService{}, stubServerService{}, nil, nil, nil, nil, nil, nil, nil, nil)
	h.SetAgentRoutesOnly()
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
