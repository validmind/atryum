package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestConnectionDebugLogsHTTPProbeWithoutSecrets(t *testing.T) {
	var logs bytes.Buffer
	prevWriter := log.Writer()
	prevFlags := log.Flags()
	log.SetOutput(&logs)
	log.SetFlags(0)
	defer func() {
		log.SetOutput(prevWriter)
		log.SetFlags(prevFlags)
	}()

	const targetURL = "https://shortcut.example/mcp"
	client := NewHTTPClient()
	client.httpClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusUnauthorized,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"token expired"}}`)),
			Request:    r,
		}, nil
	})}
	client.debug = true
	result := client.TestConnection(context.Background(), Upstream{
		Name:        "shortcut",
		Mode:        UpstreamModeHTTP,
		BaseURL:     targetURL,
		AuthToken:   "secret-token",
		AuthHeaders: []AuthHeader{{Name: "X-Api-Key", Value: "super-secret"}},
		Enabled:     true,
	})

	if result.Ok {
		t.Fatalf("expected failed auth probe")
	}
	got := logs.String()
	for _, want := range []string{
		"connection test start server=shortcut",
		"target=" + targetURL,
		"auth=has_bearer=true auth_headers=X-Api-Key",
		"connection test http initialize server=shortcut",
		"connection test http response server=shortcut status=401",
		"connection test result server=shortcut",
		"message=\"upstream initialize using MCP 2025-11-25 failed: http 401: token expired\"",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected logs to contain %q, got:\n%s", want, got)
		}
	}
	for _, secret := range []string{"secret-token", "super-secret"} {
		if strings.Contains(got, secret) {
			t.Fatalf("expected logs to redact %q, got:\n%s", secret, got)
		}
	}
}

func TestListToolsRetriesInitializeWithCompatibleProtocol(t *testing.T) {
	var protocols []string
	var sawToolsListSession bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req Envelope
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch req.Method {
		case "initialize":
			protocol := r.Header.Get("MCP-Protocol-Version")
			protocols = append(protocols, protocol)
			if protocol == MCPProtocolVersion2025 {
				writeTestRPC(w, req.ID, nil, map[string]any{"code": -32602, "message": "unsupported protocol version"})
				return
			}
			if protocol != MCPProtocolVersion2025_03_26 {
				writeTestRPC(w, req.ID, nil, map[string]any{"code": -32602, "message": "unsupported protocol version"})
				return
			}
			w.Header().Set("Mcp-Session-Id", "sid-1")
			writeTestRPC(w, req.ID, map[string]any{"protocolVersion": protocol, "capabilities": map[string]any{}}, nil)
		case "notifications/initialized":
			if got := r.Header.Get("Mcp-Session-Id"); got != "sid-1" {
				t.Fatalf("initialized notification missing session id: %q", got)
			}
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			if got := r.Header.Get("Mcp-Session-Id"); got == "sid-1" {
				sawToolsListSession = true
			}
			if got := r.Header.Get("MCP-Protocol-Version"); got != MCPProtocolVersion2025_03_26 {
				t.Fatalf("tools/list used protocol %q, want %q", got, MCPProtocolVersion2025_03_26)
			}
			writeTestRPC(w, req.ID, map[string]any{"tools": []map[string]any{{"name": "stories.search"}}}, nil)
		default:
			t.Fatalf("unexpected method %q", req.Method)
		}
	}))
	defer server.Close()

	client := NewHTTPClient()
	client.httpClient = server.Client()
	tools, err := client.ListTools(context.Background(), Upstream{Name: "shortcut", Mode: UpstreamModeHTTP, BaseURL: server.URL})
	if err != nil {
		t.Fatalf("ListTools returned error: %v; protocols=%#v", err, protocols)
	}
	if len(tools) != 1 || tools[0].Name != "stories.search" {
		t.Fatalf("unexpected tools: %#v", tools)
	}
	if !sawToolsListSession {
		t.Fatal("tools/list did not include the negotiated session id")
	}
	if len(protocols) != 3 {
		t.Fatalf("expected three initialize attempts, got %d: %#v", len(protocols), protocols)
	}
	if protocols[0] != MCPProtocolVersion2025 || protocols[1] != MCPProtocolVersion2025_06_18 || protocols[2] != MCPProtocolVersion2025_03_26 {
		t.Fatalf("unexpected protocol fallback order: %#v", protocols)
	}
}

func TestMissingSessionRPCErrorDetection(t *testing.T) {
	if !isMissingSessionRPCError(json.RawMessage(`{"code":-32000,"message":"No session ID provided for non-initialization request"}`)) {
		t.Fatal("expected missing session error to be detected")
	}
}

func TestListToolsSerializesConcurrentInitialize(t *testing.T) {
	var activeInitializes int32
	var maxActiveInitializes int32
	var initializeCount int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req Envelope
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch req.Method {
		case "initialize":
			active := atomic.AddInt32(&activeInitializes, 1)
			for {
				maxActive := atomic.LoadInt32(&maxActiveInitializes)
				if active <= maxActive || atomic.CompareAndSwapInt32(&maxActiveInitializes, maxActive, active) {
					break
				}
			}
			count := atomic.AddInt32(&initializeCount, 1)
			time.Sleep(20 * time.Millisecond)
			atomic.AddInt32(&activeInitializes, -1)
			w.Header().Set("Mcp-Session-Id", "sid-concurrent")
			writeTestRPC(w, req.ID, map[string]any{"protocolVersion": r.Header.Get("MCP-Protocol-Version"), "capabilities": map[string]any{}}, nil)
			if count > 1 {
				t.Fatalf("unexpected duplicate initialize %d", count)
			}
		case "notifications/initialized":
			if got := r.Header.Get("Mcp-Session-Id"); got != "sid-concurrent" {
				t.Fatalf("initialized notification missing session id: %q", got)
			}
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			if got := r.Header.Get("Mcp-Session-Id"); got != "sid-concurrent" {
				t.Fatalf("tools/list missing session id: %q", got)
			}
			writeTestRPC(w, req.ID, map[string]any{"tools": []map[string]any{{"name": "stories.search"}}}, nil)
		default:
			t.Fatalf("unexpected method %q", req.Method)
		}
	}))
	defer server.Close()

	client := NewHTTPClient()
	client.httpClient = server.Client()
	upstream := Upstream{Name: "shortcut", Mode: UpstreamModeHTTP, BaseURL: server.URL}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := client.ListTools(context.Background(), upstream)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("ListTools returned error: %v", err)
		}
	}
	if got := atomic.LoadInt32(&initializeCount); got != 1 {
		t.Fatalf("initialize count = %d, want 1", got)
	}
	if got := atomic.LoadInt32(&maxActiveInitializes); got != 1 {
		t.Fatalf("max active initializes = %d, want 1", got)
	}
}

func TestListToolsRecoversWhenUpstreamRequiresSessionAfterStatelessInitialize(t *testing.T) {
	var protocols []string
	var sawSessionlessList bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req Envelope
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch req.Method {
		case "initialize":
			protocol := r.Header.Get("MCP-Protocol-Version")
			protocols = append(protocols, protocol)
			if protocol == MCPProtocolVersion2025_03_26 {
				w.Header().Set("Mcp-Session-Id", "sid-2")
			}
			writeTestRPC(w, req.ID, map[string]any{"protocolVersion": protocol, "capabilities": map[string]any{}}, nil)
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			if r.Header.Get("Mcp-Session-Id") == "" {
				sawSessionlessList = true
				writeTestRPC(w, req.ID, nil, map[string]any{"code": -32000, "message": "No session ID provided for non-initialization request"})
				return
			}
			if got := r.Header.Get("MCP-Protocol-Version"); got != MCPProtocolVersion2025_03_26 {
				t.Fatalf("tools/list used protocol %q, want %q", got, MCPProtocolVersion2025_03_26)
			}
			writeTestRPC(w, req.ID, map[string]any{"tools": []map[string]any{{"name": "stories.get"}}}, nil)
		default:
			t.Fatalf("unexpected method %q", req.Method)
		}
	}))
	defer server.Close()

	client := NewHTTPClient()
	client.httpClient = server.Client()
	tools, err := client.ListTools(context.Background(), Upstream{Name: "shortcut", Mode: UpstreamModeHTTP, BaseURL: server.URL})
	if err != nil {
		t.Fatalf("ListTools returned error: %v; protocols=%#v", err, protocols)
	}
	if len(tools) != 1 || tools[0].Name != "stories.get" {
		t.Fatalf("unexpected tools: %#v", tools)
	}
	if !sawSessionlessList {
		t.Fatal("test did not exercise sessionless tools/list failure")
	}
	want := []string{MCPProtocolVersion2025, MCPProtocolVersion2025, MCPProtocolVersion2025_06_18, MCPProtocolVersion2025_03_26}
	if len(protocols) != len(want) {
		t.Fatalf("expected initialize protocols %#v, got %#v", want, protocols)
	}
	for i := range want {
		if protocols[i] != want[i] {
			t.Fatalf("expected initialize protocols %#v, got %#v", want, protocols)
		}
	}
}

func TestListToolsRetriesAfterSessionExpired404(t *testing.T) {
	var initializeCount int32
	var expiredOnce atomic.Bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req Envelope
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch req.Method {
		case "initialize":
			count := atomic.AddInt32(&initializeCount, 1)
			sessionID := "sid-1"
			if count == 2 {
				sessionID = "sid-2"
			}
			w.Header().Set("Mcp-Session-Id", sessionID)
			writeTestRPC(w, req.ID, map[string]any{"protocolVersion": r.Header.Get("MCP-Protocol-Version"), "capabilities": map[string]any{}}, nil)
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			switch r.Header.Get("Mcp-Session-Id") {
			case "sid-1":
				if !expiredOnce.CompareAndSwap(false, true) {
					t.Fatal("expired session used more than once")
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":{"message":"session expired"}}`))
			case "sid-2":
				writeTestRPC(w, req.ID, map[string]any{"tools": []map[string]any{{"name": "stories.retry"}}}, nil)
			default:
				t.Fatalf("unexpected tools/list session id %q", r.Header.Get("Mcp-Session-Id"))
			}
		default:
			t.Fatalf("unexpected method %q", req.Method)
		}
	}))
	defer server.Close()

	client := NewHTTPClient()
	client.httpClient = server.Client()
	tools, err := client.ListTools(context.Background(), Upstream{Name: "shortcut", Mode: UpstreamModeHTTP, BaseURL: server.URL})
	if err != nil {
		t.Fatalf("ListTools returned error: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "stories.retry" {
		t.Fatalf("unexpected tools: %#v", tools)
	}
	if got := atomic.LoadInt32(&initializeCount); got != 2 {
		t.Fatalf("initialize count = %d, want 2", got)
	}
	if !expiredOnce.Load() {
		t.Fatal("test did not exercise expired session 404")
	}
}

func TestReinitializeSkipsWhenNewerSessionExists(t *testing.T) {
	var initializeCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req Envelope
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.Method == "initialize" {
			atomic.AddInt32(&initializeCount, 1)
		}
		writeTestRPC(w, req.ID, map[string]any{"protocolVersion": r.Header.Get("MCP-Protocol-Version"), "capabilities": map[string]any{}}, nil)
	}))
	defer server.Close()

	client := NewHTTPClient()
	client.httpClient = server.Client()
	client.setSession("shortcut", "sid-fresh", MCPProtocolVersion2025_03_26)

	err := client.reinitializeRequiredHTTPSession(context.Background(), Upstream{Name: "shortcut", Mode: UpstreamModeHTTP, BaseURL: server.URL}, "sid-stale")
	if err != nil {
		t.Fatalf("reinitializeRequiredHTTPSession returned error: %v", err)
	}
	if got := atomic.LoadInt32(&initializeCount); got != 0 {
		t.Fatalf("initialize count = %d, want 0", got)
	}
	if got := client.getSession("shortcut"); got != "sid-fresh" {
		t.Fatalf("session = %q, want sid-fresh", got)
	}
}

func TestConnectionUsesProtocolFallback(t *testing.T) {
	var protocols []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req Envelope
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch req.Method {
		case "initialize":
			protocol := r.Header.Get("MCP-Protocol-Version")
			protocols = append(protocols, protocol)
			if protocol != MCPProtocolVersion2025_03_26 {
				writeTestRPC(w, req.ID, nil, map[string]any{"code": -32602, "message": "unsupported protocol version"})
				return
			}
			w.Header().Set("Mcp-Session-Id", "sid-test")
			writeTestRPC(w, req.ID, map[string]any{"protocolVersion": protocol, "capabilities": map[string]any{}}, nil)
		case "notifications/initialized":
			if got := r.Header.Get("Mcp-Session-Id"); got != "sid-test" {
				t.Fatalf("initialized notification missing session id: %q", got)
			}
			w.WriteHeader(http.StatusAccepted)
		default:
			t.Fatalf("unexpected method %q", req.Method)
		}
	}))
	defer server.Close()

	client := NewHTTPClient()
	client.httpClient = server.Client()
	result := client.TestConnection(context.Background(), Upstream{Name: "shortcut", Mode: UpstreamModeHTTP, BaseURL: server.URL, Enabled: true})
	if !result.Ok {
		t.Fatalf("TestConnection returned not ok: %#v; protocols=%#v", result, protocols)
	}
	want := []string{MCPProtocolVersion2025, MCPProtocolVersion2025_06_18, MCPProtocolVersion2025_03_26}
	if len(protocols) != len(want) {
		t.Fatalf("expected initialize protocols %#v, got %#v", want, protocols)
	}
	for i := range want {
		if protocols[i] != want[i] {
			t.Fatalf("expected initialize protocols %#v, got %#v", want, protocols)
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func writeTestRPC(w http.ResponseWriter, id json.RawMessage, result any, rpcErr any) {
	w.Header().Set("Content-Type", "application/json")
	resp := map[string]any{"jsonrpc": "2.0", "id": id}
	if rpcErr != nil {
		resp["error"] = rpcErr
	} else {
		resp["result"] = result
	}
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		panic(err)
	}
}
