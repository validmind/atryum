package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/validmind/atryum/pkg/tenant"
)

// Two tenants' servers named alike keep their own upstream sessions.
func TestSessionsKeptPerTenant(t *testing.T) {
	var mu sync.Mutex
	seen := map[string][]string{} // server -> session ids sent on tools/list
	serve := func(name string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var req Envelope
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("decode: %v", err)
				return
			}
			switch req.Method {
			case "initialize":
				w.Header().Set("Mcp-Session-Id", "sid-"+name)
				writeTestRPC(w, req.ID, map[string]any{"protocolVersion": r.Header.Get("MCP-Protocol-Version"), "capabilities": map[string]any{}}, nil)
			case "notifications/initialized":
				w.WriteHeader(http.StatusAccepted)
			case "tools/list":
				mu.Lock()
				seen[name] = append(seen[name], r.Header.Get("Mcp-Session-Id"))
				mu.Unlock()
				writeTestRPC(w, req.ID, map[string]any{"tools": []map[string]any{}}, nil)
			}
		}))
	}
	a, b := serve("a"), serve("b")
	defer a.Close()
	defer b.Close()

	client := NewHTTPClient()
	list := func(id, url string) {
		ctx := tenant.With(context.Background(), id)
		if _, err := client.ListTools(ctx, Upstream{Name: "payments", Mode: UpstreamModeHTTP, BaseURL: url}); err != nil {
			t.Fatal(err)
		}
	}
	list("bank-a", a.URL)
	list("bank-b", b.URL)
	list("bank-a", a.URL)

	mu.Lock()
	defer mu.Unlock()
	if got := seen["b"]; len(got) != 1 || got[0] != "sid-b" {
		t.Fatalf("bank-b's server saw sessions %q, want [sid-b]", got)
	}
	if got := seen["a"]; len(got) != 2 || got[0] != "sid-a" || got[1] != "sid-a" {
		t.Fatalf("bank-a's server saw sessions %q, want [sid-a sid-a]", got)
	}
}
