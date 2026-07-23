package mcp

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/validmind/atryum/internal/config"
)

type fakeServerStore struct {
	upstreams []Upstream
}

func (s fakeServerStore) GetServer(_ context.Context, name string) (Upstream, error) {
	for _, upstream := range s.upstreams {
		if upstream.Name == name && upstream.Enabled {
			return upstream, nil
		}
	}
	return Upstream{}, sql.ErrNoRows
}

func (s fakeServerStore) GetServerByEndpointSlug(_ context.Context, slug string) (Upstream, error) {
	for _, upstream := range s.upstreams {
		endpointSlug := upstream.EndpointSlug
		if endpointSlug == "" {
			endpointSlug = EndpointSlug(upstream.Name)
		}
		if endpointSlug == slug && upstream.Enabled {
			return upstream, nil
		}
	}
	return Upstream{}, sql.ErrNoRows
}

func (s fakeServerStore) ListServers(_ context.Context, filter ServerFilter) ([]Upstream, int, error) {
	var out []Upstream
	for _, upstream := range s.upstreams {
		if filter.Enabled != nil && upstream.Enabled != *filter.Enabled {
			continue
		}
		out = append(out, upstream)
	}
	return out, len(out), nil
}

func (s fakeServerStore) CountServers(context.Context) (int, error) {
	return len(s.upstreams), nil
}

func (s fakeServerStore) CreateServer(context.Context, Upstream) error {
	return nil
}

func TestEndpointSlug(t *testing.T) {
	tests := map[string]string{
		"Slack Local":    "slack-local",
		"  GitHub MCP  ": "github-mcp",
		"Foo_Bar.v1~dev": "foo_bar.v1~dev",
		"!!!":            "",
		".":              "",
		"...":            "",
	}
	for input, want := range tests {
		if got := EndpointSlug(input); got != want {
			t.Fatalf("EndpointSlug(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestResolverResolvesServerByEndpointSlug(t *testing.T) {
	resolver := NewResolver(fakeServerStore{upstreams: []Upstream{{
		Name:         "Slack Local",
		EndpointSlug: "slack-local-2",
		Mode:         UpstreamModeHTTP,
		BaseURL:      "https://mcp.slack.test",
		Enabled:      true,
	}}}, config.Config{})

	upstream, err := resolver.ResolveContext(context.Background(), "slack-local-2")
	if err != nil {
		t.Fatalf("ResolveContext: %v", err)
	}
	if upstream.Name != "Slack Local" {
		t.Fatalf("resolved name = %q, want Slack Local", upstream.Name)
	}
}

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

func TestRefreshOAuthTokenSendsRefreshGrant(t *testing.T) {
	var form url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if got := r.Header.Get("Content-Type"); got != "application/x-www-form-urlencoded" {
			t.Fatalf("content-type = %q", got)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		form = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"access-new","refresh_token":"refresh-new","token_type":"Bearer","scope":"openid","expires_in":3600}`))
	}))
	defer server.Close()

	client := NewHTTPClient()
	client.httpClient = server.Client()
	token, err := client.RefreshOAuthToken(context.Background(), Upstream{
		Name:              "shortcut",
		OAuthTokenURL:     server.URL,
		OAuthClientID:     "client-id",
		OAuthClientSecret: "client-secret",
	}, "refresh-old")
	if err != nil {
		t.Fatalf("RefreshOAuthToken: %v", err)
	}
	if token.AccessToken != "access-new" || token.RefreshToken != "refresh-new" {
		t.Fatalf("unexpected token: %#v", token)
	}
	if token.ExpiresAt == nil {
		t.Fatal("expected expires_at to be set")
	}
	want := map[string]string{
		"grant_type":    "refresh_token",
		"refresh_token": "refresh-old",
		"client_id":     "client-id",
		"client_secret": "client-secret",
	}
	for key, value := range want {
		if got := form.Get(key); got != value {
			t.Fatalf("form[%s] = %q, want %q; form=%v", key, got, value, form)
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

func TestListToolsDecodesSSEResponseAfterMissingSessionReinitialize(t *testing.T) {
	var toolsListCount int
	var sessions []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req Envelope
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch req.Method {
		case "initialize":
			sessionID := "sid-1"
			if len(sessions) > 0 {
				sessionID = "sid-2"
			}
			sessions = append(sessions, sessionID)
			w.Header().Set("Mcp-Session-Id", sessionID)
			writeTestRPC(w, req.ID, map[string]any{"protocolVersion": r.Header.Get("MCP-Protocol-Version"), "capabilities": map[string]any{}}, nil)
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			toolsListCount++
			if toolsListCount == 1 {
				writeTestRPC(w, req.ID, nil, map[string]any{"code": -32000, "message": "No session ID provided for non-initialization request"})
				return
			}
			if got := r.Header.Get("Mcp-Session-Id"); got != "sid-2" {
				t.Fatalf("retry tools/list used session %q, want sid-2", got)
			}
			writeTestRPCSSE(w, req.ID, map[string]any{"tools": []map[string]any{{"name": "stories.search"}}}, nil)
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
	if len(tools) != 1 || tools[0].Name != "stories.search" {
		t.Fatalf("unexpected tools: %#v", tools)
	}
	if toolsListCount != 2 {
		t.Fatalf("tools/list count = %d, want 2", toolsListCount)
	}
	if len(sessions) != 2 {
		t.Fatalf("initialize sessions = %#v, want two sessions", sessions)
	}
}

func TestInitializeDecodesSSEResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req Envelope
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch req.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "sid-sse")
			writeTestRPCSSE(w, req.ID, map[string]any{"protocolVersion": r.Header.Get("MCP-Protocol-Version"), "capabilities": map[string]any{}}, nil)
		case "notifications/initialized":
			if got := r.Header.Get("Mcp-Session-Id"); got != "sid-sse" {
				t.Fatalf("initialized notification missing session id: %q", got)
			}
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			if got := r.Header.Get("Mcp-Session-Id"); got != "sid-sse" {
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
	tools, err := client.ListTools(context.Background(), Upstream{Name: "shortcut", Mode: UpstreamModeHTTP, BaseURL: server.URL})
	if err != nil {
		t.Fatalf("ListTools returned error: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "stories.search" {
		t.Fatalf("unexpected tools: %#v", tools)
	}
}

func TestInitializeSSEHTTPErrorReportsRPCMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req Envelope
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.Method != "initialize" {
			t.Fatalf("unexpected method %q", req.Method)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusUnauthorized)
		writeTestRPCSSE(w, req.ID, nil, map[string]any{"code": -32000, "message": "unauthorized"})
	}))
	defer server.Close()

	client := NewHTTPClient()
	client.httpClient = server.Client()
	result := client.TestConnection(context.Background(), Upstream{Name: "shortcut", Mode: UpstreamModeHTTP, BaseURL: server.URL, Enabled: true})
	if result.Ok {
		t.Fatalf("expected failed connection test")
	}
	if !strings.Contains(result.Message, "unauthorized") {
		t.Fatalf("expected clean upstream error message, got %q", result.Message)
	}
	if strings.Contains(result.Message, "data:") {
		t.Fatalf("expected SSE framing to be hidden, got %q", result.Message)
	}
}

func TestInitializeSSEHTTPErrorReportsNullIDRPCMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req Envelope
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.Method != "initialize" {
			t.Fatalf("unexpected method %q", req.Method)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusBadRequest)
		writeTestRPCSSE(w, json.RawMessage("null"), nil, map[string]any{"code": -32700, "message": "parse error"})
	}))
	defer server.Close()

	client := NewHTTPClient()
	client.httpClient = server.Client()
	result := client.TestConnection(context.Background(), Upstream{Name: "shortcut", Mode: UpstreamModeHTTP, BaseURL: server.URL, Enabled: true})
	if result.Ok {
		t.Fatalf("expected failed connection test")
	}
	if !strings.Contains(result.Message, "parse error") {
		t.Fatalf("expected clean upstream error message, got %q", result.Message)
	}
	if strings.Contains(result.Message, "data:") {
		t.Fatalf("expected SSE framing to be hidden, got %q", result.Message)
	}
}

func TestListToolsAcceptsStringIDInSSEResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req Envelope
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch req.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "sid-string-id")
			writeTestRPC(w, req.ID, map[string]any{"protocolVersion": r.Header.Get("MCP-Protocol-Version"), "capabilities": map[string]any{}}, nil)
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			writeTestRPCSSE(w, json.RawMessage(`"1"`), map[string]any{"tools": []map[string]any{{"name": "stories.string_id"}}}, nil)
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
	if len(tools) != 1 || tools[0].Name != "stories.string_id" {
		t.Fatalf("unexpected tools: %#v", tools)
	}
}

func TestInvokeSkipsSSENotificationBeforeResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req Envelope
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch req.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "sid-notify")
			writeTestRPC(w, req.ID, map[string]any{"protocolVersion": r.Header.Get("MCP-Protocol-Version"), "capabilities": map[string]any{}}, nil)
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/call":
			writeTestSSEEvents(w,
				[]string{`{"jsonrpc":"2.0","method":"notifications/progress","params":{"progress":0.5}}`},
				[]string{`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"actual result"}]}}`},
			)
		default:
			t.Fatalf("unexpected method %q", req.Method)
		}
	}))
	defer server.Close()

	client := NewHTTPClient()
	client.httpClient = server.Client()
	result, err := client.Invoke(context.Background(), Upstream{Name: "shortcut", Mode: UpstreamModeHTTP, BaseURL: server.URL}, "stories.get", map[string]any{}, nil, nil)
	if err != nil {
		t.Fatalf("Invoke returned error: %v", err)
	}
	if !strings.Contains(string(result.Body), "actual result") {
		t.Fatalf("expected final response body, got %s", string(result.Body))
	}
}

func TestInvokeForwardsCallerMetaAndAtryumRequestID(t *testing.T) {
	var capturedParams struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
		Meta      map[string]any `json:"_meta"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req Envelope
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch req.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "sid-meta")
			writeTestRPC(w, req.ID, map[string]any{"protocolVersion": r.Header.Get("MCP-Protocol-Version"), "capabilities": map[string]any{}}, nil)
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/call":
			if err := json.Unmarshal(req.Params, &capturedParams); err != nil {
				t.Fatalf("decode tools/call params: %v", err)
			}
			writeTestRPC(w, req.ID, map[string]any{"content": []any{map[string]any{"type": "text", "text": "ok"}}}, nil)
		default:
			t.Fatalf("unexpected method %q", req.Method)
		}
	}))
	defer server.Close()

	client := NewHTTPClient()
	client.httpClient = server.Client()
	requestID := "req-42"
	meta := map[string]any{"progressToken": "tok-7"}
	if _, err := client.Invoke(context.Background(), Upstream{Name: "shortcut", Mode: UpstreamModeHTTP, BaseURL: server.URL}, "stories.get", map[string]any{}, &requestID, meta); err != nil {
		t.Fatalf("Invoke returned error: %v", err)
	}

	if got := capturedParams.Meta["progressToken"]; got != "tok-7" {
		t.Fatalf("expected caller progressToken preserved in upstream _meta, got %#v", capturedParams.Meta)
	}
	if got := capturedParams.Meta["atryumRequestId"]; got != "req-42" {
		t.Fatalf("expected atryumRequestId injected into upstream _meta, got %#v", capturedParams.Meta)
	}
}

func TestInvokeOmitsMetaWhenCallerMetaAndRequestIDAreEmpty(t *testing.T) {
	var sawMeta bool
	var rawParams json.RawMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req Envelope
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch req.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "sid-nometa")
			writeTestRPC(w, req.ID, map[string]any{"protocolVersion": r.Header.Get("MCP-Protocol-Version"), "capabilities": map[string]any{}}, nil)
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/call":
			rawParams = req.Params
			var decoded map[string]any
			if err := json.Unmarshal(req.Params, &decoded); err != nil {
				t.Fatalf("decode tools/call params: %v", err)
			}
			_, sawMeta = decoded["_meta"]
			writeTestRPC(w, req.ID, map[string]any{"content": []any{map[string]any{"type": "text", "text": "ok"}}}, nil)
		default:
			t.Fatalf("unexpected method %q", req.Method)
		}
	}))
	defer server.Close()

	client := NewHTTPClient()
	client.httpClient = server.Client()
	if _, err := client.Invoke(context.Background(), Upstream{Name: "shortcut", Mode: UpstreamModeHTTP, BaseURL: server.URL}, "stories.get", map[string]any{}, nil, nil); err != nil {
		t.Fatalf("Invoke returned error: %v", err)
	}

	if sawMeta {
		t.Fatalf("expected no _meta field when caller meta and requestID are both empty, got params %s", rawParams)
	}
}

func TestBoundedBufferCapsRetainedBytesWithoutErroringWrites(t *testing.T) {
	b := newBoundedBuffer(8)
	n, err := b.Write([]byte("1234"))
	if err != nil || n != 4 {
		t.Fatalf("Write(1234) = %d, %v, want 4, nil", n, err)
	}
	n, err = b.Write([]byte("567890")) // 4 + 6 = 10, exceeds the 8-byte cap
	if err != nil || n != 6 {
		t.Fatalf("Write(567890) = %d, %v, want 6, nil (full length reported even though truncated)", n, err)
	}
	if got := b.String(); got != "12345678" {
		t.Fatalf("String() = %q, want the first 8 bytes \"12345678\"", got)
	}
	if b.Len() != 8 {
		t.Fatalf("Len() = %d, want 8", b.Len())
	}
	// Further writes past the cap must still report full success (the
	// subprocess's stderr pipe must never see a short write or an error).
	n, err = b.Write([]byte("more data"))
	if err != nil || n != len("more data") {
		t.Fatalf("Write past cap = %d, %v, want %d, nil", n, err, len("more data"))
	}
	if b.Len() != 8 {
		t.Fatalf("Len() after writing past cap = %d, want 8 (unchanged)", b.Len())
	}
}

func TestMergeRequestMeta(t *testing.T) {
	reqID := "req-1"

	if got := mergeRequestMeta(nil, nil); got != nil {
		t.Fatalf("expected nil for empty meta and nil requestID, got %#v", got)
	}
	empty := ""
	if got := mergeRequestMeta(nil, &empty); got != nil {
		t.Fatalf("expected nil for empty meta and empty requestID, got %#v", got)
	}
	if got := mergeRequestMeta(map[string]any{"progressToken": "tok"}, nil); got["progressToken"] != "tok" || got["atryumRequestId"] != nil {
		t.Fatalf("expected caller meta preserved without atryumRequestId, got %#v", got)
	}
	if got := mergeRequestMeta(nil, &reqID); got["atryumRequestId"] != "req-1" {
		t.Fatalf("expected atryumRequestId-only meta, got %#v", got)
	}
	if got := mergeRequestMeta(map[string]any{"progressToken": "tok", "atryumRequestId": "spoofed"}, &reqID); got["progressToken"] != "tok" || got["atryumRequestId"] != "req-1" {
		t.Fatalf("expected atryumRequestId to win over a caller-supplied value, got %#v", got)
	}
}

// invokeStreamTestServer builds the initialize/notifications.initialized
// scaffolding shared by the InvokeStream tests below, dispatching tools/call
// to callHandler.
func invokeStreamTestServer(t *testing.T, sessionID string, callHandler func(w http.ResponseWriter, r *http.Request, req Envelope)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			// The standalone SSE stream a progressToken-bearing call opens
			// alongside its tools/call POST. This fake upstream doesn't
			// support it — a legitimate, spec-allowed response — so tests
			// using a progressToken don't need every callHandler to be
			// GET-aware.
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		var req Envelope
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch req.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", sessionID)
			writeTestRPC(w, req.ID, map[string]any{"protocolVersion": r.Header.Get("MCP-Protocol-Version"), "capabilities": map[string]any{}}, nil)
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/call":
			callHandler(w, r, req)
		default:
			t.Fatalf("unexpected method %q", req.Method)
		}
	}))
}

func TestInvokeStreamRelaysEventsBeforeTerminalResponseExists(t *testing.T) {
	release := make(chan struct{})
	server := invokeStreamTestServer(t, "sid-incremental", func(w http.ResponseWriter, r *http.Request, req Envelope) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		writeTestSSEEventFlush(w, flusher, `{"jsonrpc":"2.0","method":"notifications/progress","params":{"progressToken":"tok-7","progress":1}}`)
		<-release // the terminal response cannot be written until the test has observed the event above
		writeTestSSEEventFlush(w, flusher, `{"jsonrpc":"2.0","id":"1","result":{"content":[{"type":"text","text":"done"}]}}`)
	})
	defer server.Close()

	client := NewHTTPClient()
	client.httpClient = server.Client()
	sink := &fakeStreamSink{onEvent: func(StreamEvent) error {
		close(release)
		return nil
	}}

	result, err := client.InvokeStream(context.Background(), Upstream{Name: "shortcut", Mode: UpstreamModeHTTP, BaseURL: server.URL}, "stories.get", map[string]any{}, nil, nil, sink, StreamOptions{})
	if err != nil {
		t.Fatalf("InvokeStream returned error: %v", err)
	}
	if !sink.started {
		t.Fatal("expected StreamStarted to fire")
	}
	if len(sink.events) != 1 {
		t.Fatalf("expected exactly one relayed event, got %d", len(sink.events))
	}
	if !strings.Contains(string(sink.events[0].Data), "notifications/progress") {
		t.Fatalf("expected progress notification relayed, got %s", sink.events[0].Data)
	}
	if !strings.Contains(string(result.Body), "done") {
		t.Fatalf("expected terminal result body, got %s", result.Body)
	}
}

func TestInvokeStreamResumesAfterUpstreamClosesSSEBeforeTerminalResponse(t *testing.T) {
	var resumeRequests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			resumeRequests++
			if got := r.Header.Get("Last-Event-ID"); got != "evt-1" {
				t.Fatalf("resume Last-Event-ID = %q, want evt-1", got)
			}
			if got := r.Header.Get("Mcp-Session-Id"); got != "sid-resume" {
				t.Fatalf("resume Mcp-Session-Id = %q, want sid-resume", got)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			flusher := w.(http.Flusher)
			_, _ = io.WriteString(w, "id: evt-2\ndata: {\"jsonrpc\":\"2.0\",\"id\":\"1\",\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"done after resume\"}]}}\n\n")
			flusher.Flush()
			return
		}

		var req Envelope
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch req.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "sid-resume")
			writeTestRPC(w, req.ID, map[string]any{"protocolVersion": r.Header.Get("MCP-Protocol-Version"), "capabilities": map[string]any{}}, nil)
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/call":
			w.Header().Set("Content-Type", "text/event-stream")
			flusher := w.(http.Flusher)
			_, _ = io.WriteString(w, "id: evt-1\nretry: 1\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{\"progress\":1}}\n\n")
			flusher.Flush()
			// End this HTTP response without the terminal JSON-RPC response.
			// A resumable MCP stream continues through a GET with Last-Event-ID.
		default:
			t.Fatalf("unexpected method %q", req.Method)
		}
	}))
	defer server.Close()

	client := NewHTTPClient()
	client.httpClient = server.Client()
	sink := &fakeStreamSink{}
	result, err := client.InvokeStream(
		context.Background(),
		Upstream{Name: "shortcut", Mode: UpstreamModeHTTP, BaseURL: server.URL},
		"stories.get", map[string]any{}, nil, nil, sink,
		StreamOptions{IdleTimeout: time.Second, MaxDuration: 5 * time.Second},
	)
	if err != nil {
		t.Fatalf("InvokeStream returned error: %v", err)
	}
	if resumeRequests != 1 {
		t.Fatalf("resume request count = %d, want 1", resumeRequests)
	}
	if len(sink.events) != 1 || !strings.Contains(string(sink.events[0].Data), "notifications/progress") {
		t.Fatalf("expected exactly the pre-disconnect progress event, got %#v", sink.events)
	}
	if !strings.Contains(string(result.Body), "done after resume") {
		t.Fatalf("expected terminal response from resumed stream, got %s", result.Body)
	}
}

// TestInvokeStreamResumeSkipsInclusivelyReplayedCursorEvent is a regression
// test for reconnect duplicate delivery: Last-Event-ID replay is exclusive
// of the cursor, but the classic server off-by-one replays the cursor event
// itself again. That event's data already reached the agent before the
// disconnect — relaying it twice would deliver a duplicate notification.
func TestInvokeStreamResumeSkipsInclusivelyReplayedCursorEvent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			if got := r.Header.Get("Last-Event-ID"); got != "evt-1" {
				t.Fatalf("resume Last-Event-ID = %q, want evt-1", got)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			flusher := w.(http.Flusher)
			// Buggy inclusive replay: evt-1 again, then genuinely new events.
			_, _ = io.WriteString(w, "id: evt-1\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{\"progress\":1}}\n\n")
			_, _ = io.WriteString(w, "id: evt-2\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{\"progress\":2}}\n\n")
			_, _ = io.WriteString(w, "id: evt-3\ndata: {\"jsonrpc\":\"2.0\",\"id\":\"1\",\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"done after resume\"}]}}\n\n")
			flusher.Flush()
			return
		}

		var req Envelope
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch req.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "sid-resume-dupe")
			writeTestRPC(w, req.ID, map[string]any{"protocolVersion": r.Header.Get("MCP-Protocol-Version"), "capabilities": map[string]any{}}, nil)
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/call":
			w.Header().Set("Content-Type", "text/event-stream")
			flusher := w.(http.Flusher)
			_, _ = io.WriteString(w, "id: evt-1\nretry: 1\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{\"progress\":1}}\n\n")
			flusher.Flush()
			// Close without the terminal response → client resumes via GET.
		default:
			t.Fatalf("unexpected method %q", req.Method)
		}
	}))
	defer server.Close()

	client := NewHTTPClient()
	client.httpClient = server.Client()
	sink := &fakeStreamSink{}
	result, err := client.InvokeStream(
		context.Background(),
		Upstream{Name: "shortcut", Mode: UpstreamModeHTTP, BaseURL: server.URL},
		"stories.get", map[string]any{}, nil, nil, sink,
		StreamOptions{IdleTimeout: time.Second, MaxDuration: 5 * time.Second},
	)
	if err != nil {
		t.Fatalf("InvokeStream returned error: %v", err)
	}
	if len(sink.events) != 2 {
		t.Fatalf("expected exactly 2 relayed notifications (evt-1 once + evt-2, cursor replay deduplicated), got %d: %#v", len(sink.events), sink.events)
	}
	if !strings.Contains(string(sink.events[0].Data), `"progress":1`) || !strings.Contains(string(sink.events[1].Data), `"progress":2`) {
		t.Fatalf("expected progress 1 then progress 2, got %#v", sink.events)
	}
	if !strings.Contains(string(result.Body), "done after resume") {
		t.Fatalf("expected terminal response from resumed stream, got %s", result.Body)
	}
}

func TestInvokeStreamRelaysNotificationAndServerRequest(t *testing.T) {
	server := invokeStreamTestServer(t, "sid-mixed", func(w http.ResponseWriter, r *http.Request, req Envelope) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		writeTestSSEEventFlush(w, flusher, `{"jsonrpc":"2.0","method":"notifications/message","params":{"level":"info","data":"halfway"}}`)
		writeTestSSEEventFlush(w, flusher, `{"jsonrpc":"2.0","id":"srv-1","method":"sampling/createMessage","params":{}}`)
		writeTestSSEEventFlush(w, flusher, `{"jsonrpc":"2.0","id":"1","result":{"content":[{"type":"text","text":"done"}]}}`)
	})
	defer server.Close()

	client := NewHTTPClient()
	client.httpClient = server.Client()
	sink := &fakeStreamSink{}

	result, err := client.InvokeStream(context.Background(), Upstream{Name: "shortcut", Mode: UpstreamModeHTTP, BaseURL: server.URL}, "stories.get", map[string]any{}, nil, nil, sink, StreamOptions{})
	if err != nil {
		t.Fatalf("InvokeStream returned error: %v", err)
	}
	if len(sink.events) != 2 {
		t.Fatalf("expected 2 relayed events (notification + server request), got %d: %#v", len(sink.events), sink.events)
	}
	if sink.events[0].ServerRequest {
		t.Fatalf("expected first event to be a notification, got %#v", sink.events[0])
	}
	if !sink.events[1].ServerRequest {
		t.Fatalf("expected second event to be flagged as a server request, got %#v", sink.events[1])
	}
	if !strings.Contains(string(result.Body), "done") {
		t.Fatalf("expected terminal result body, got %s", result.Body)
	}
}

func TestInvokeStreamJSONResponseNeverTouchesSink(t *testing.T) {
	server := invokeStreamTestServer(t, "sid-json", func(w http.ResponseWriter, r *http.Request, req Envelope) {
		writeTestRPC(w, req.ID, map[string]any{"content": []any{map[string]any{"type": "text", "text": "ok"}}}, nil)
	})
	defer server.Close()

	client := NewHTTPClient()
	client.httpClient = server.Client()
	sink := &fakeStreamSink{}

	result, err := client.InvokeStream(context.Background(), Upstream{Name: "shortcut", Mode: UpstreamModeHTTP, BaseURL: server.URL}, "stories.get", map[string]any{}, nil, nil, sink, StreamOptions{})
	if err != nil {
		t.Fatalf("InvokeStream returned error: %v", err)
	}
	if sink.started || len(sink.events) != 0 {
		t.Fatalf("expected sink to never be touched for a JSON response, got started=%t events=%#v", sink.started, sink.events)
	}
	if !strings.Contains(string(result.Body), `"text":"ok"`) {
		t.Fatalf("expected plain JSON result body, got %s", result.Body)
	}
}

// TestInvokeStreamHangingJSONBodyBoundedByIdleTimeout is a regression test:
// StreamOptions' idle/max-duration bounds must apply to every body-reading
// branch of doHTTPToolCallStream, not just the SSE relay. The per-call
// http.Client.Timeout that would normally catch a hanging JSON body is
// deliberately skipped in streaming mode (see doHTTPEnvelopeRaw's streaming
// param), so without this a slow-to-complete JSON response during a
// streaming call attempt would hang forever.
func TestInvokeStreamHangingJSONBodyBoundedByIdleTimeout(t *testing.T) {
	blockUntilTestDone := make(chan struct{})
	server := invokeStreamTestServer(t, "sid-slow-json", func(w http.ResponseWriter, r *http.Request, req Envelope) {
		w.Header().Set("Content-Type", "application/json")
		flusher := w.(http.Flusher)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":"1","result":`)) // deliberately incomplete
		flusher.Flush()
		<-blockUntilTestDone // never completes the body
	})
	t.Cleanup(func() {
		close(blockUntilTestDone)
		server.Close()
	})

	client := NewHTTPClient()
	client.httpClient = server.Client()
	sink := &fakeStreamSink{}

	start := time.Now()
	_, err := client.InvokeStream(context.Background(), Upstream{Name: "shortcut", Mode: UpstreamModeHTTP, BaseURL: server.URL}, "stories.get", map[string]any{}, nil, nil, sink, StreamOptions{IdleTimeout: 50 * time.Millisecond})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected a timeout error for the hanging JSON body read")
	}
	if !errors.Is(err, ErrStreamTimeout) {
		t.Fatalf("expected errors.Is(err, ErrStreamTimeout), got %v", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("took too long to abort: %s", elapsed)
	}
}

// TestInvokeStreamHangingSessionInitBoundedByHeaderTimeout is a regression
// test: the session-initialize POST happens before doHTTPToolCallStream's
// own header timeout is armed, and in streaming mode neither the per-call
// http.Client timeout (deliberately skipped) nor the caller's ctx (no
// deadline) bounds it. An upstream with no per-server timeout configured
// that hangs on initialize would block the call forever without
// runSessionInitBounded.
func TestInvokeStreamHangingSessionInitBoundedByHeaderTimeout(t *testing.T) {
	blockUntilTestDone := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req Envelope
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.Method != "initialize" {
			t.Fatalf("unexpected method %q before initialize completed", req.Method)
		}
		<-blockUntilTestDone // hang the initialize response forever
	}))
	t.Cleanup(func() {
		close(blockUntilTestDone)
		server.Close()
	})

	client := NewHTTPClient()
	client.httpClient = server.Client()
	sink := &fakeStreamSink{}

	start := time.Now()
	// upstream.Timeout deliberately zero: no per-server bound to fall back on.
	_, err := client.InvokeStream(context.Background(), Upstream{Name: "shortcut", Mode: UpstreamModeHTTP, BaseURL: server.URL}, "stories.get", map[string]any{}, nil, nil, sink, StreamOptions{HeaderTimeout: 50 * time.Millisecond})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected a session-init timeout error")
	}
	if !errors.Is(err, ErrStreamTimeout) {
		t.Fatalf("expected errors.Is(err, ErrStreamTimeout) for the hung session init, got %v", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("session-init timeout took too long to abort: %s", elapsed)
	}
	if sink.started || len(sink.events) != 0 {
		t.Fatalf("expected the sink never to be touched during a failed session init, got started=%t events=%d", sink.started, len(sink.events))
	}
}

func TestInvokeStreamMapsTerminalRPCErrorAfterRelayedEvents(t *testing.T) {
	server := invokeStreamTestServer(t, "sid-error", func(w http.ResponseWriter, r *http.Request, req Envelope) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		writeTestSSEEventFlush(w, flusher, `{"jsonrpc":"2.0","method":"notifications/progress","params":{"progress":1}}`)
		writeTestSSEEventFlush(w, flusher, `{"jsonrpc":"2.0","id":"1","error":{"code":-32000,"message":"tool exploded"}}`)
	})
	defer server.Close()

	client := NewHTTPClient()
	client.httpClient = server.Client()
	sink := &fakeStreamSink{}

	result, err := client.InvokeStream(context.Background(), Upstream{Name: "shortcut", Mode: UpstreamModeHTTP, BaseURL: server.URL}, "stories.get", map[string]any{}, nil, nil, sink, StreamOptions{})
	if err != nil {
		t.Fatalf("InvokeStream returned error: %v", err)
	}
	if !result.Failed {
		t.Fatalf("expected Failed result, got %#v", result)
	}
	if !strings.Contains(string(result.Body), "tool exploded") {
		t.Fatalf("expected error body, got %s", result.Body)
	}
	if len(sink.events) != 1 {
		t.Fatalf("expected the progress notification to have been relayed before the terminal error, got %d", len(sink.events))
	}
}

func TestInvokeStreamRetriesOnceWhenMissingSessionBeforeAnyEventRelayed(t *testing.T) {
	var sessions []string
	var toolsCallCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req Envelope
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch req.Method {
		case "initialize":
			sessionID := "sid-1"
			if len(sessions) > 0 {
				sessionID = "sid-2"
			}
			sessions = append(sessions, sessionID)
			w.Header().Set("Mcp-Session-Id", sessionID)
			writeTestRPC(w, req.ID, map[string]any{"protocolVersion": r.Header.Get("MCP-Protocol-Version"), "capabilities": map[string]any{}}, nil)
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/call":
			toolsCallCount++
			w.Header().Set("Content-Type", "text/event-stream")
			flusher := w.(http.Flusher)
			if toolsCallCount == 1 {
				writeTestSSEEventFlush(w, flusher, `{"jsonrpc":"2.0","id":"1","error":{"code":-32000,"message":"No session ID provided for non-initialization request"}}`)
				return
			}
			if got := r.Header.Get("Mcp-Session-Id"); got != "sid-2" {
				t.Fatalf("retry tools/call used session %q, want sid-2", got)
			}
			writeTestSSEEventFlush(w, flusher, `{"jsonrpc":"2.0","id":"1","result":{"content":[{"type":"text","text":"done"}]}}`)
		default:
			t.Fatalf("unexpected method %q", req.Method)
		}
	}))
	defer server.Close()

	client := NewHTTPClient()
	client.httpClient = server.Client()
	sink := &fakeStreamSink{}

	result, err := client.InvokeStream(context.Background(), Upstream{Name: "shortcut", Mode: UpstreamModeHTTP, BaseURL: server.URL}, "stories.get", map[string]any{}, nil, nil, sink, StreamOptions{})
	if err != nil {
		t.Fatalf("InvokeStream returned error: %v", err)
	}
	if toolsCallCount != 2 {
		t.Fatalf("tools/call count = %d, want 2", toolsCallCount)
	}
	if len(sessions) != 2 {
		t.Fatalf("initialize sessions = %#v, want two sessions", sessions)
	}
	if !strings.Contains(string(result.Body), "done") {
		t.Fatalf("expected terminal result body after retry, got %s", result.Body)
	}
	if sink.startedCount != 1 {
		t.Fatalf("expected StreamStarted to fire exactly once (for the successful retry, not the discarded missing-session attempt), got %d", sink.startedCount)
	}
}

func TestInvokeStreamRefusesRetryAfterEventsAlreadyRelayed(t *testing.T) {
	var initializeCount int
	var toolsCallCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req Envelope
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch req.Method {
		case "initialize":
			initializeCount++
			w.Header().Set("Mcp-Session-Id", "sid-1")
			writeTestRPC(w, req.ID, map[string]any{"protocolVersion": r.Header.Get("MCP-Protocol-Version"), "capabilities": map[string]any{}}, nil)
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/call":
			toolsCallCount++
			w.Header().Set("Content-Type", "text/event-stream")
			flusher := w.(http.Flusher)
			writeTestSSEEventFlush(w, flusher, `{"jsonrpc":"2.0","method":"notifications/progress","params":{"progress":1}}`)
			writeTestSSEEventFlush(w, flusher, `{"jsonrpc":"2.0","id":"1","error":{"code":-32000,"message":"No session ID provided for non-initialization request"}}`)
		default:
			t.Fatalf("unexpected method %q", req.Method)
		}
	}))
	defer server.Close()

	client := NewHTTPClient()
	client.httpClient = server.Client()
	sink := &fakeStreamSink{}

	_, err := client.InvokeStream(context.Background(), Upstream{Name: "shortcut", Mode: UpstreamModeHTTP, BaseURL: server.URL}, "stories.get", map[string]any{}, nil, nil, sink, StreamOptions{})
	if err == nil {
		t.Fatal("expected an error refusing to retry mid-stream")
	}
	if !strings.Contains(err.Error(), "already relayed") {
		t.Fatalf("expected a mid-stream retry refusal error, got %v", err)
	}
	if !errors.Is(err, ErrStreamSessionRetryRefused) {
		t.Fatalf("expected errors.Is(err, ErrStreamSessionRetryRefused) to hold, got %v", err)
	}
	if toolsCallCount != 1 {
		t.Fatalf("tools/call count = %d, want 1 (no retry once events were relayed)", toolsCallCount)
	}
	if initializeCount != 1 {
		t.Fatalf("initialize count = %d, want 1 (no reinitialize attempt)", initializeCount)
	}
	if len(sink.events) != 1 {
		t.Fatalf("expected the one notification before the terminal error to have been relayed, got %d", len(sink.events))
	}
}

func TestInvokeStreamIdleTimeoutAbortsRead(t *testing.T) {
	blockUntilTestDone := make(chan struct{})
	server := invokeStreamTestServer(t, "sid-idle", func(w http.ResponseWriter, r *http.Request, req Envelope) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		writeTestSSEEventFlush(w, flusher, `{"jsonrpc":"2.0","method":"notifications/progress","params":{"progress":1}}`)
		<-blockUntilTestDone // never send the terminal event
	})
	t.Cleanup(func() {
		close(blockUntilTestDone)
		server.Close()
	})

	client := NewHTTPClient()
	client.httpClient = server.Client()
	sink := &fakeStreamSink{}

	start := time.Now()
	_, err := client.InvokeStream(context.Background(), Upstream{Name: "shortcut", Mode: UpstreamModeHTTP, BaseURL: server.URL}, "stories.get", map[string]any{}, nil, nil, sink, StreamOptions{IdleTimeout: 50 * time.Millisecond})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected an idle timeout error")
	}
	if !strings.Contains(err.Error(), "idle timeout") {
		t.Fatalf("expected an idle timeout error, got %v", err)
	}
	if !errors.Is(err, ErrStreamTimeout) {
		t.Fatalf("expected errors.Is(err, ErrStreamTimeout) to hold so callers can distinguish it from other failures, got %v", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("idle timeout took too long to abort: %s", elapsed)
	}
	if !sink.started {
		t.Fatal("expected StreamStarted before the timeout")
	}
	if len(sink.events) != 1 {
		t.Fatalf("expected exactly one relayed event before the timeout, got %d", len(sink.events))
	}
}

// syncFakeStreamSink is fakeStreamSink's mutex-protected counterpart. It's
// needed wherever a test can have both relaySSEToolCall's own read loop and
// routeStandaloneEvent deliver to the same sink concurrently — the plain
// fakeStreamSink above assumes single-goroutine delivery and would race.
type syncFakeStreamSink struct {
	mu      sync.Mutex
	started bool
	events  []StreamEvent
}

func newSyncFakeStreamSink() *syncFakeStreamSink {
	return &syncFakeStreamSink{}
}

func (s *syncFakeStreamSink) StreamStarted() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.started = true
}

func (s *syncFakeStreamSink) Event(evt StreamEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, evt)
	return nil
}

func (s *syncFakeStreamSink) wasStarted() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.started
}

func (s *syncFakeStreamSink) snapshotEvents() []StreamEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]StreamEvent, len(s.events))
	copy(out, s.events)
	return out
}

func TestRewriteProgressToken(t *testing.T) {
	client := NewHTTPClient()
	if _, _, _, ok := client.rewriteProgressToken(nil); ok {
		t.Fatal("expected no rewrite when meta is nil")
	}
	if _, _, _, ok := client.rewriteProgressToken(map[string]any{"atryumRequestId": "x"}); ok {
		t.Fatal("expected no rewrite when meta has no progressToken")
	}

	rewritten, wireToken, original, ok := client.rewriteProgressToken(map[string]any{"progressToken": float64(7), "atryumRequestId": "req-1"})
	if !ok {
		t.Fatal("expected a rewrite when progressToken is present")
	}
	if original != float64(7) {
		t.Fatalf("expected original token 7, got %#v", original)
	}
	if rewritten["progressToken"] != wireToken {
		t.Fatalf("expected rewritten meta to carry the wire token, got %#v", rewritten["progressToken"])
	}
	if rewritten["atryumRequestId"] != "req-1" {
		t.Fatal("expected other meta keys preserved")
	}

	_, wireToken2, _, _ := client.rewriteProgressToken(map[string]any{"progressToken": "other"})
	if wireToken2 == wireToken {
		t.Fatal("expected distinct wire tokens across calls, so concurrent callers can't collide")
	}
}

func TestExtractAndRewriteProgressTokenInPayload(t *testing.T) {
	payload := []byte(`{"jsonrpc":"2.0","method":"notifications/progress","params":{"progressToken":"atryum-pt-3","progress":1,"total":3}}`)
	var msg map[string]json.RawMessage
	if err := json.Unmarshal(payload, &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	token, ok := extractProgressToken(msg)
	if !ok || token != "atryum-pt-3" {
		t.Fatalf("extractProgressToken = (%q, %v), want (atryum-pt-3, true)", token, ok)
	}

	rewritten, err := rewriteProgressTokenInPayload(payload, float64(42))
	if err != nil {
		t.Fatalf("rewriteProgressTokenInPayload: %v", err)
	}
	if !strings.Contains(string(rewritten), `"progressToken":42`) {
		t.Fatalf("expected original numeric token restored, got %s", rewritten)
	}
	if !strings.Contains(string(rewritten), `"progress":1`) {
		t.Fatalf("expected other params fields preserved, got %s", rewritten)
	}

	if _, ok := extractProgressToken(map[string]json.RawMessage{"method": json.RawMessage(`"notifications/message"`)}); ok {
		t.Fatal("expected no token when params is absent")
	}
}

// TestRouteStandaloneEventAttributesTokenlessNotificationOnlyWhenUnambiguous
// covers routeStandaloneEvent's fallback for messages with no progressToken
// (e.g. a plain logging notification): deliverable only when exactly one
// call is waiting on the stream, since guessing with several concurrent
// waiters would leak one caller's message to another.
func TestRouteStandaloneEventAttributesTokenlessNotificationOnlyWhenUnambiguous(t *testing.T) {
	client := NewHTTPClient()
	payload := []byte(`{"jsonrpc":"2.0","method":"notifications/message","params":{"level":"info","data":"hello"}}`)

	chA := make(chan StreamEvent, 1)
	lone := &standaloneStream{waiters: map[string]progressWaiter{"tok-a": {events: chA}}}
	client.routeStandaloneEvent(lone, payload)
	select {
	case <-chA:
	default:
		t.Fatal("expected the lone waiter to receive a tokenless notification")
	}

	chB, chC := make(chan StreamEvent, 1), make(chan StreamEvent, 1)
	ambiguous := &standaloneStream{waiters: map[string]progressWaiter{
		"tok-b": {events: chB},
		"tok-c": {events: chC},
	}}
	client.routeStandaloneEvent(ambiguous, payload)
	select {
	case <-chB:
		t.Fatal("expected a tokenless notification to be dropped, not guessed, with multiple concurrent waiters")
	case <-chC:
		t.Fatal("expected a tokenless notification to be dropped, not guessed, with multiple concurrent waiters")
	default:
	}
}

// TestInvokeStreamStandaloneStreamRelaysProgressNotification is the
// regression test for the real end-to-end gap this feature fixes: the
// reference MCP Python SDK's Context.report_progress sends progress
// notifications on the standalone GET stream, never on the tools/call POST
// response body, because it doesn't attribute the notification to the
// request that triggered it. Without a standalone-stream reader, Atryum
// would relay zero progress notifications for such a server even though the
// terminal response arrives correctly.
func TestInvokeStreamStandaloneStreamRelaysProgressNotification(t *testing.T) {
	tokenCh := make(chan string, 1)
	notifSent := make(chan struct{})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			if got := r.Header.Get("Last-Event-ID"); got != "" {
				t.Fatalf("standalone GET unexpectedly carried Last-Event-ID=%q (that's the resume path)", got)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			flusher := w.(http.Flusher)
			token := <-tokenCh
			writeTestSSEEventFlush(w, flusher, fmt.Sprintf(`{"jsonrpc":"2.0","method":"notifications/progress","params":{"progressToken":%q,"progress":1}}`, token))
			close(notifSent)
			<-r.Context().Done()
			return
		}

		var req Envelope
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch req.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "sid-standalone")
			writeTestRPC(w, req.ID, map[string]any{"protocolVersion": r.Header.Get("MCP-Protocol-Version"), "capabilities": map[string]any{}}, nil)
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/call":
			var params struct {
				Meta struct {
					ProgressToken string `json:"progressToken"`
				} `json:"_meta"`
			}
			_ = json.Unmarshal(req.Params, &params)
			tokenCh <- params.Meta.ProgressToken
			<-notifSent
			w.Header().Set("Content-Type", "text/event-stream")
			flusher := w.(http.Flusher)
			writeTestSSEEventFlush(w, flusher, `{"jsonrpc":"2.0","id":"1","result":{"content":[{"type":"text","text":"done"}]}}`)
		default:
			t.Fatalf("unexpected method %q", req.Method)
		}
	}))
	defer server.Close()

	client := NewHTTPClient()
	client.httpClient = server.Client()
	sink := newSyncFakeStreamSink()

	result, err := client.InvokeStream(context.Background(), Upstream{Name: "real", Mode: UpstreamModeHTTP, BaseURL: server.URL}, "slow_streaming_task", map[string]any{}, nil, map[string]any{"progressToken": "caller-token"}, sink, StreamOptions{})
	if err != nil {
		t.Fatalf("InvokeStream returned error: %v", err)
	}
	if !strings.Contains(string(result.Body), "done") {
		t.Fatalf("expected terminal result body, got %s", result.Body)
	}
	if !sink.wasStarted() {
		t.Fatal("expected StreamStarted to fire for a notification delivered only via the standalone stream")
	}
	events := sink.snapshotEvents()
	if len(events) != 1 {
		t.Fatalf("expected exactly one relayed event, got %d: %#v", len(events), events)
	}
	if !strings.Contains(string(events[0].Data), `"progressToken":"caller-token"`) {
		t.Fatalf("expected the caller's original progressToken restored, got %s", events[0].Data)
	}
}

// TestInvokeStreamRestoresCallerProgressTokenOnPOSTResponseStreamToo is a
// regression test: some upstreams echo a call's progress notifications on
// the tools/call POST response itself, not the standalone stream — that's
// actually the more spec-typical case for a request-scoped notification.
// The caller's original progressToken must be restored there too, not only
// on notifications that happen to arrive via the standalone stream.
func TestInvokeStreamRestoresCallerProgressTokenOnPOSTResponseStreamToo(t *testing.T) {
	server := invokeStreamTestServer(t, "sid-post-echo", func(w http.ResponseWriter, r *http.Request, req Envelope) {
		var params struct {
			Meta struct {
				ProgressToken string `json:"progressToken"`
			} `json:"_meta"`
		}
		_ = json.Unmarshal(req.Params, &params)

		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		writeTestSSEEventFlush(w, flusher, fmt.Sprintf(`{"jsonrpc":"2.0","method":"notifications/progress","params":{"progressToken":%q,"progress":1}}`, params.Meta.ProgressToken))
		writeTestSSEEventFlush(w, flusher, `{"jsonrpc":"2.0","id":"1","result":{"content":[{"type":"text","text":"done"}]}}`)
	})
	defer server.Close()

	client := NewHTTPClient()
	client.httpClient = server.Client()
	sink := newSyncFakeStreamSink()

	_, err := client.InvokeStream(context.Background(), Upstream{Name: "shortcut", Mode: UpstreamModeHTTP, BaseURL: server.URL}, "stories.get", map[string]any{}, nil, map[string]any{"progressToken": "caller-token"}, sink, StreamOptions{})
	if err != nil {
		t.Fatalf("InvokeStream returned error: %v", err)
	}
	events := sink.snapshotEvents()
	if len(events) != 1 {
		t.Fatalf("expected exactly one relayed event, got %d: %#v", len(events), events)
	}
	if !strings.Contains(string(events[0].Data), `"progressToken":"caller-token"`) {
		t.Fatalf("expected the caller's original progressToken restored on the POST-response stream, got %s", events[0].Data)
	}
	if strings.Contains(string(events[0].Data), "atryum-pt-") {
		t.Fatalf("expected Atryum's internal wire token never to leak to the agent, got %s", events[0].Data)
	}
}

// TestInvokeStreamStandaloneStreamAvoidsProgressTokenCollisionAcrossCalls
// proves two concurrent callers who happen to pick the same progressToken
// don't cross-deliver: Atryum multiplexes every caller of an upstream onto
// one shared session, so the standalone stream is shared too, and the only
// thing preventing a collision is the per-call wire-token rewrite.
func TestInvokeStreamStandaloneStreamAvoidsProgressTokenCollisionAcrossCalls(t *testing.T) {
	var mu sync.Mutex
	tokenFor := map[string]string{}
	postCount := 0
	getConnected := make(chan struct{})
	gotBothTokens := make(chan struct{})
	notifsDone := make(chan struct{})
	var closeGetConnectedOnce, closeGotBothTokensOnce sync.Once

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			closeGetConnectedOnce.Do(func() { close(getConnected) })
			w.Header().Set("Content-Type", "text/event-stream")
			flusher := w.(http.Flusher)
			<-gotBothTokens
			mu.Lock()
			tokA, tokB := tokenFor["tool-a"], tokenFor["tool-b"]
			mu.Unlock()
			writeTestSSEEventFlush(w, flusher, fmt.Sprintf(`{"jsonrpc":"2.0","method":"notifications/progress","params":{"progressToken":%q,"progress":1}}`, tokA))
			writeTestSSEEventFlush(w, flusher, fmt.Sprintf(`{"jsonrpc":"2.0","method":"notifications/progress","params":{"progressToken":%q,"progress":2}}`, tokB))
			close(notifsDone)
			<-r.Context().Done()
			return
		}

		var req Envelope
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch req.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "sid-collision")
			writeTestRPC(w, req.ID, map[string]any{"protocolVersion": r.Header.Get("MCP-Protocol-Version"), "capabilities": map[string]any{}}, nil)
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/call":
			var params struct {
				Name string `json:"name"`
				Meta struct {
					ProgressToken string `json:"progressToken"`
				} `json:"_meta"`
			}
			_ = json.Unmarshal(req.Params, &params)
			<-getConnected
			mu.Lock()
			tokenFor[params.Name] = params.Meta.ProgressToken
			postCount++
			ready := postCount == 2
			mu.Unlock()
			if ready {
				closeGotBothTokensOnce.Do(func() { close(gotBothTokens) })
			}
			<-notifsDone
			w.Header().Set("Content-Type", "text/event-stream")
			flusher := w.(http.Flusher)
			writeTestSSEEventFlush(w, flusher, fmt.Sprintf(`{"jsonrpc":"2.0","id":"1","result":{"content":[{"type":"text","text":"done-%s"}]}}`, params.Name))
		default:
			t.Fatalf("unexpected method %q", req.Method)
		}
	}))
	defer server.Close()

	client := NewHTTPClient()
	client.httpClient = server.Client()
	upstream := Upstream{Name: "real", Mode: UpstreamModeHTTP, BaseURL: server.URL}

	sinkA := newSyncFakeStreamSink()
	sinkB := newSyncFakeStreamSink()

	var wg sync.WaitGroup
	var errA, errB error
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, errA = client.InvokeStream(context.Background(), upstream, "tool-a", map[string]any{}, nil, map[string]any{"progressToken": float64(1)}, sinkA, StreamOptions{})
	}()
	go func() {
		defer wg.Done()
		_, errB = client.InvokeStream(context.Background(), upstream, "tool-b", map[string]any{}, nil, map[string]any{"progressToken": float64(1)}, sinkB, StreamOptions{})
	}()
	wg.Wait()

	if errA != nil {
		t.Fatalf("call A error: %v", errA)
	}
	if errB != nil {
		t.Fatalf("call B error: %v", errB)
	}

	eventsA, eventsB := sinkA.snapshotEvents(), sinkB.snapshotEvents()
	if len(eventsA) != 1 {
		t.Fatalf("call A: expected exactly 1 relayed event, got %d: %#v", len(eventsA), eventsA)
	}
	if len(eventsB) != 1 {
		t.Fatalf("call B: expected exactly 1 relayed event, got %d: %#v", len(eventsB), eventsB)
	}
	if !strings.Contains(string(eventsA[0].Data), `"progress":1`) || !strings.Contains(string(eventsA[0].Data), `"progressToken":1`) {
		t.Fatalf("call A got the wrong notification or token, want its own progress=1/token=1, got %s", eventsA[0].Data)
	}
	if !strings.Contains(string(eventsB[0].Data), `"progress":2`) || !strings.Contains(string(eventsB[0].Data), `"progressToken":1`) {
		t.Fatalf("call B got the wrong notification or token, want its own progress=2/token=1, got %s", eventsB[0].Data)
	}
}

func TestStandaloneStreamRefcountsSharedConnection(t *testing.T) {
	var connections int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("unexpected method %q", r.Method)
		}
		atomic.AddInt32(&connections, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()

	client := NewHTTPClient()
	client.httpClient = server.Client()
	upstream := Upstream{Name: "real", Mode: UpstreamModeHTTP, BaseURL: server.URL}

	s1 := client.acquireStandaloneStream(upstream)
	s2 := client.acquireStandaloneStream(upstream)
	if s1 != s2 {
		t.Fatal("expected the second acquire to reuse the same standaloneStream while the first is still active")
	}

	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(&connections) < 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := atomic.LoadInt32(&connections); got != 1 {
		t.Fatalf("expected exactly 1 standalone connection while both waiters are active, got %d", got)
	}

	client.releaseStandaloneStream(upstream, s1)
	if got := atomic.LoadInt32(&connections); got != 1 {
		t.Fatalf("releasing one of two references should not close the connection yet, got %d", got)
	}
	client.releaseStandaloneStream(upstream, s2)

	s3 := client.acquireStandaloneStream(upstream)
	if s3 == s1 {
		t.Fatal("expected a fresh standaloneStream after the previous one was fully released")
	}
	deadline = time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(&connections) < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := atomic.LoadInt32(&connections); got != 2 {
		t.Fatalf("expected a new connection after full release + reacquire, got %d", got)
	}
	client.releaseStandaloneStream(upstream, s3)
}

// TestInvokeStreamStandaloneStreamUnsupportedDoesNotFailCall covers an
// upstream that returns a plain error (e.g. 404/405, which some servers
// legitimately return for this endpoint per spec) for the standalone GET:
// the tools/call itself must still succeed normally via its own POST
// response stream.
func TestInvokeStreamStandaloneStreamUnsupportedDoesNotFailCall(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		var req Envelope
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch req.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "sid-unsupported")
			writeTestRPC(w, req.ID, map[string]any{"protocolVersion": r.Header.Get("MCP-Protocol-Version"), "capabilities": map[string]any{}}, nil)
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/call":
			w.Header().Set("Content-Type", "text/event-stream")
			flusher := w.(http.Flusher)
			writeTestSSEEventFlush(w, flusher, `{"jsonrpc":"2.0","id":"1","result":{"content":[{"type":"text","text":"done"}]}}`)
		default:
			t.Fatalf("unexpected method %q", req.Method)
		}
	}))
	defer server.Close()

	client := NewHTTPClient()
	client.httpClient = server.Client()
	sink := newSyncFakeStreamSink()

	result, err := client.InvokeStream(context.Background(), Upstream{Name: "real", Mode: UpstreamModeHTTP, BaseURL: server.URL}, "tool", map[string]any{}, nil, map[string]any{"progressToken": "tok"}, sink, StreamOptions{})
	if err != nil {
		t.Fatalf("InvokeStream returned error: %v", err)
	}
	if !strings.Contains(string(result.Body), "done") {
		t.Fatalf("expected terminal result body, got %s", result.Body)
	}
	if len(sink.snapshotEvents()) != 0 {
		t.Fatalf("expected no relayed events when the standalone stream is unsupported, got %#v", sink.snapshotEvents())
	}
}

// TestInvokeStreamStandaloneStreamWrongContentTypeDoesNotFailCall covers the
// other shape of "unsupported": a 200 response that isn't actually SSE
// (some servers, on a bare GET, just serve something unrelated rather than
// the expected 404/405). openStandaloneGET must reject it the same way it
// rejects an outright error status, without affecting the call itself.
func TestInvokeStreamStandaloneStreamWrongContentTypeDoesNotFailCall(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<html>not an SSE stream</html>"))
			return
		}
		var req Envelope
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch req.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "sid-wrong-content-type")
			writeTestRPC(w, req.ID, map[string]any{"protocolVersion": r.Header.Get("MCP-Protocol-Version"), "capabilities": map[string]any{}}, nil)
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/call":
			w.Header().Set("Content-Type", "text/event-stream")
			flusher := w.(http.Flusher)
			writeTestSSEEventFlush(w, flusher, `{"jsonrpc":"2.0","id":"1","result":{"content":[{"type":"text","text":"done"}]}}`)
		default:
			t.Fatalf("unexpected method %q", req.Method)
		}
	}))
	defer server.Close()

	client := NewHTTPClient()
	client.httpClient = server.Client()
	sink := newSyncFakeStreamSink()

	result, err := client.InvokeStream(context.Background(), Upstream{Name: "real", Mode: UpstreamModeHTTP, BaseURL: server.URL}, "tool", map[string]any{}, nil, map[string]any{"progressToken": "tok"}, sink, StreamOptions{})
	if err != nil {
		t.Fatalf("InvokeStream returned error: %v", err)
	}
	if !strings.Contains(string(result.Body), "done") {
		t.Fatalf("expected terminal result body, got %s", result.Body)
	}
	if len(sink.snapshotEvents()) != 0 {
		t.Fatalf("expected no relayed events when the standalone stream has the wrong content type, got %#v", sink.snapshotEvents())
	}
}

// writeFakeStdioServer writes an executable bash script implementing the
// initialize/notifications.initialized handshake and dispatching tools/call
// to script (a bash fragment appended verbatim, given $line as the raw
// incoming JSON and able to compute its id via `id=$(echo "$line" | grep -o
// '"id":[0-9]*' | head -1 | cut -d: -f2)`).
func writeFakeStdioServer(t *testing.T, toolsCallScript string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-mcp.sh")
	content := "#!/usr/bin/env bash\n" +
		"set -euo pipefail\n" +
		"while IFS= read -r line; do\n" +
		"  if [[ -z \"$line\" ]]; then continue; fi\n" +
		"  if [[ \"$line\" == *'\"method\":\"initialize\"'* ]]; then\n" +
		"    id=$(echo \"$line\" | grep -o '\"id\":[0-9]*' | head -1 | cut -d: -f2)\n" +
		"    printf '%s\\n' \"{\\\"jsonrpc\\\":\\\"2.0\\\",\\\"id\\\":${id},\\\"result\\\":{\\\"serverInfo\\\":{\\\"name\\\":\\\"fake\\\",\\\"version\\\":\\\"0.1.0\\\"},\\\"capabilities\\\":{}}}\"\n" +
		"  elif [[ \"$line\" == *'\"method\":\"notifications/initialized\"'* ]]; then\n" +
		"    continue\n" +
		"  elif [[ \"$line\" == *'\"method\":\"tools/call\"'* ]]; then\n" +
		toolsCallScript +
		"    exit 0\n" +
		"  fi\n" +
		"done\n"
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestInvokeStreamStdioRelaysEventsBeforeTerminalResponseExists(t *testing.T) {
	releaseFile := filepath.Join(t.TempDir(), "release")
	script := writeFakeStdioServer(t, ""+
		"    id=$(echo \"$line\" | grep -o '\"id\":[0-9]*' | head -1 | cut -d: -f2)\n"+
		"    printf '%s\\n' '{\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{\"progress\":1}}'\n"+
		"    while [ ! -f \"$RELEASE_FILE\" ]; do sleep 0.02; done\n"+
		"    printf '%s\\n' \"{\\\"jsonrpc\\\":\\\"2.0\\\",\\\"id\\\":${id},\\\"result\\\":{\\\"content\\\":[{\\\"type\\\":\\\"text\\\",\\\"text\\\":\\\"done\\\"}]}}\"\n",
	)

	client := NewHTTPClient()
	upstream := Upstream{Name: "fake-stdio", Mode: UpstreamModeStdio, Command: script, Env: map[string]string{"RELEASE_FILE": releaseFile}}
	sink := &fakeStreamSink{onEvent: func(StreamEvent) error {
		// The subprocess is blocked in its own `while [ ! -f ... ]` loop and
		// cannot write the terminal response until this file exists — it
		// only gets created here, inside the callback fired once the client
		// has actually delivered the notification to the sink.
		return os.WriteFile(releaseFile, []byte("go"), 0o644)
	}}

	result, err := client.InvokeStream(context.Background(), upstream, "demo", map[string]any{}, nil, nil, sink, StreamOptions{})
	if err != nil {
		t.Fatalf("InvokeStream returned error: %v", err)
	}
	if !sink.started {
		t.Fatal("expected StreamStarted to fire")
	}
	if len(sink.events) != 1 {
		t.Fatalf("expected exactly one relayed event, got %d", len(sink.events))
	}
	if !strings.Contains(string(sink.events[0].Data), "notifications/progress") {
		t.Fatalf("expected progress notification relayed, got %s", sink.events[0].Data)
	}
	if !strings.Contains(string(result.Body), "done") {
		t.Fatalf("expected terminal result body, got %s", result.Body)
	}
}

func TestInvokeStreamStdioTerminalOnlyResponseNeverTouchesSink(t *testing.T) {
	script := writeFakeStdioServer(t, ""+
		"    id=$(echo \"$line\" | grep -o '\"id\":[0-9]*' | head -1 | cut -d: -f2)\n"+
		"    printf '%s\\n' \"{\\\"jsonrpc\\\":\\\"2.0\\\",\\\"id\\\":${id},\\\"result\\\":{\\\"content\\\":[{\\\"type\\\":\\\"text\\\",\\\"text\\\":\\\"ok\\\"}]}}\"\n",
	)

	client := NewHTTPClient()
	upstream := Upstream{Name: "fake-stdio", Mode: UpstreamModeStdio, Command: script}
	sink := &fakeStreamSink{}

	result, err := client.InvokeStream(context.Background(), upstream, "demo", map[string]any{}, nil, nil, sink, StreamOptions{})
	if err != nil {
		t.Fatalf("InvokeStream returned error: %v", err)
	}
	if sink.started || len(sink.events) != 0 {
		t.Fatalf("expected sink to never be touched when the upstream emits nothing but its terminal response, got started=%t events=%#v", sink.started, sink.events)
	}
	if !strings.Contains(string(result.Body), `"text":"ok"`) {
		t.Fatalf("expected terminal result body, got %s", result.Body)
	}
}

func TestInvokeStreamStdioTerminalErrorAfterNotification(t *testing.T) {
	script := writeFakeStdioServer(t, ""+
		"    printf '%s\\n' '{\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{\"progress\":1}}'\n"+
		"    id=$(echo \"$line\" | grep -o '\"id\":[0-9]*' | head -1 | cut -d: -f2)\n"+
		"    printf '%s\\n' \"{\\\"jsonrpc\\\":\\\"2.0\\\",\\\"id\\\":${id},\\\"error\\\":{\\\"code\\\":-32000,\\\"message\\\":\\\"tool exploded\\\"}}\"\n",
	)

	client := NewHTTPClient()
	upstream := Upstream{Name: "fake-stdio", Mode: UpstreamModeStdio, Command: script}
	sink := &fakeStreamSink{}

	result, err := client.InvokeStream(context.Background(), upstream, "demo", map[string]any{}, nil, nil, sink, StreamOptions{})
	if err != nil {
		t.Fatalf("InvokeStream returned error: %v", err)
	}
	if !result.Failed {
		t.Fatalf("expected Failed result, got %#v", result)
	}
	if !strings.Contains(string(result.Body), "tool exploded") {
		t.Fatalf("expected error body, got %s", result.Body)
	}
	if len(sink.events) != 1 {
		t.Fatalf("expected the progress notification to have been relayed before the terminal error, got %d", len(sink.events))
	}
}

func TestInvokeStreamStdioIdleTimeoutAbortsRead(t *testing.T) {
	script := writeFakeStdioServer(t, ""+
		"    printf '%s\\n' '{\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{\"progress\":1}}'\n"+
		"    sleep 30\n", // never sends the terminal event; killed by the idle timeout well before this returns
	)

	client := NewHTTPClient()
	upstream := Upstream{Name: "fake-stdio", Mode: UpstreamModeStdio, Command: script}
	sink := &fakeStreamSink{}

	start := time.Now()
	_, err := client.InvokeStream(context.Background(), upstream, "demo", map[string]any{}, nil, nil, sink, StreamOptions{IdleTimeout: 50 * time.Millisecond})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected an idle timeout error")
	}
	if !errors.Is(err, ErrStreamTimeout) {
		t.Fatalf("expected errors.Is(err, ErrStreamTimeout) to hold, got %v", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("idle timeout took too long to abort: %s", elapsed)
	}
	if len(sink.events) != 1 {
		t.Fatalf("expected exactly one relayed event before the timeout, got %d", len(sink.events))
	}
}

// TestInvokeStreamStdioSinkErrorDoesNotDeadlockOnPersistentServer is a
// regression test for the cleanup-defer ordering: when the sink aborts the
// relay (agent disconnected) with no timeout having fired, the cleanup
// defer runs cmd.Wait() — and a stdio server that keeps running (its outer
// read loop is stuck inside an inner emit loop, so it never notices
// stdin-close) would block Wait forever unless guard.stop() cancels the
// context (killing the process group) BEFORE the Wait. With separate
// defers in the natural order, LIFO ran Wait first — a deadlock this test
// would catch by hanging.
func TestInvokeStreamStdioSinkErrorDoesNotDeadlockOnPersistentServer(t *testing.T) {
	script := writeFakeStdioServer(t, ""+
		"    while true; do\n"+
		"      printf '%s\\n' '{\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{\"progress\":1}}'\n"+
		"      sleep 0.05\n"+
		"    done\n",
	)

	client := NewHTTPClient()
	upstream := Upstream{Name: "fake-stdio", Mode: UpstreamModeStdio, Command: script}
	sink := &fakeStreamSink{onEvent: func(StreamEvent) error {
		return errors.New("downstream connection closed")
	}}

	start := time.Now()
	_, err := client.InvokeStream(context.Background(), upstream, "demo", map[string]any{}, nil, nil, sink, StreamOptions{})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected the sink's error to be returned")
	}
	if !strings.Contains(err.Error(), "downstream connection closed") {
		t.Fatalf("expected the sink's error, got %v", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("expected a prompt return after the sink aborted (process-group kill before Wait), took %s", elapsed)
	}
}

// TestInvokeStreamStdioHandshakeHangBoundedByHeaderTimeout is a regression
// test: the stdio initialize handshake happens before body timeouts are
// armed, so it needs the header-phase bound — without it, a subprocess
// that starts but never answers initialize blocks the call with no bound
// of its own.
func TestInvokeStreamStdioHandshakeHangBoundedByHeaderTimeout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hang-mcp.sh")
	content := "#!/usr/bin/env bash\n" +
		"set -euo pipefail\n" +
		"while IFS= read -r line; do\n" +
		"  if [[ \"$line\" == *'\"method\":\"initialize\"'* ]]; then\n" +
		"    sleep 30\n" + // never answers initialize; killed by the header timeout
		"  fi\n" +
		"done\n"
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}

	client := NewHTTPClient()
	upstream := Upstream{Name: "fake-stdio", Mode: UpstreamModeStdio, Command: path}
	sink := &fakeStreamSink{}

	start := time.Now()
	_, err := client.InvokeStream(context.Background(), upstream, "demo", map[string]any{}, nil, nil, sink, StreamOptions{HeaderTimeout: 50 * time.Millisecond})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected a handshake timeout error")
	}
	if !errors.Is(err, ErrStreamTimeout) {
		t.Fatalf("expected errors.Is(err, ErrStreamTimeout) for the hung handshake, got %v", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("handshake timeout took too long to abort: %s", elapsed)
	}
	if sink.started || len(sink.events) != 0 {
		t.Fatalf("expected the sink never to be touched during a failed handshake, got started=%t events=%d", sink.started, len(sink.events))
	}
}

// TestInvokeStdioSkipsStrayServerRequestBeforeTerminalResponse is a
// regression test for the readRPC correctness fix: a "first message with
// any id/result/error wins" reader would misinterpret a stray incoming
// server-to-client request (it has an id, but no result/error — readRPC's
// old check only looked for "any of id/result/error present") as the
// answer to our own call, before the real response ever arrives. This uses
// the plain buffered Invoke (not InvokeStream) since the fix applies
// there too.
func TestInvokeStdioSkipsStrayServerRequestBeforeTerminalResponse(t *testing.T) {
	script := writeFakeStdioServer(t, ""+
		"    printf '%s\\n' '{\"jsonrpc\":\"2.0\",\"id\":\"srv-1\",\"method\":\"sampling/createMessage\",\"params\":{}}'\n"+
		"    id=$(echo \"$line\" | grep -o '\"id\":[0-9]*' | head -1 | cut -d: -f2)\n"+
		"    printf '%s\\n' \"{\\\"jsonrpc\\\":\\\"2.0\\\",\\\"id\\\":${id},\\\"result\\\":{\\\"content\\\":[{\\\"type\\\":\\\"text\\\",\\\"text\\\":\\\"real answer\\\"}]}}\"\n",
	)

	client := NewHTTPClient()
	upstream := Upstream{Name: "fake-stdio", Mode: UpstreamModeStdio, Command: script}

	result, err := client.Invoke(context.Background(), upstream, "demo", map[string]any{}, nil, nil)
	if err != nil {
		t.Fatalf("Invoke returned error: %v", err)
	}
	if !strings.Contains(string(result.Body), "real answer") {
		t.Fatalf("expected the real terminal response (readRPC must skip the stray server-to-client request), got %s", result.Body)
	}
}

func TestCallTimeoutGuardIdleAndMaxDuration(t *testing.T) {
	idle := newCallTimeoutGuard(context.Background())
	defer idle.stop()
	idle.armBodyTimeouts(10*time.Millisecond, 0)
	<-idle.ctx.Done()
	if reason := idle.reason(); !strings.Contains(reason, "idle timeout") {
		t.Fatalf("expected idle timeout reason, got %q", reason)
	}

	max := newCallTimeoutGuard(context.Background())
	defer max.stop()
	max.armBodyTimeouts(0, 10*time.Millisecond)
	<-max.ctx.Done()
	if reason := max.reason(); !strings.Contains(reason, "max stream duration") {
		t.Fatalf("expected max duration reason, got %q", reason)
	}
}

func TestCallTimeoutGuardHeaderTimeoutDisarmedAfterHeadersArrive(t *testing.T) {
	g := newCallTimeoutGuard(context.Background())
	defer g.stop()
	g.armHeaderTimeout(10 * time.Millisecond)
	g.disarmHeaderTimeout()
	time.Sleep(30 * time.Millisecond)
	if reason := g.reason(); reason != "" {
		t.Fatalf("expected a disarmed header timeout not to fire, got %q", reason)
	}
}

// TestCallTimeoutGuardCheckIdleReschedulesOnRecentActivity is a
// deterministic regression test for the idle-timer reset race: time.Timer's
// docs explicitly warn that Reset racing with the timer's own firing is
// unsafe to reason about naively (the AfterFunc callback may already be
// running by the time Reset takes effect). checkIdle closes that race by
// re-deriving real elapsed time from lastActivity instead of trusting that
// "the timer fired" means "genuinely idle". This calls checkIdle directly
// with a lastActivity timestamp from a moment ago — simulating the timer
// firing at the exact instant resetIdle recorded fresh activity — and
// verifies it reschedules rather than tripping.
func TestCallTimeoutGuardCheckIdleReschedulesOnRecentActivity(t *testing.T) {
	g := newCallTimeoutGuard(context.Background())
	defer g.stop()
	g.idleTimeout = 100 * time.Millisecond
	g.lastActivity.Store(time.Now().UnixNano())
	g.idleTimer = time.NewTimer(time.Hour) // dummy target for checkIdle's Reset call

	g.checkIdle()

	if reason := g.reason(); reason != "" {
		t.Fatalf("expected checkIdle to reschedule (not trip) when real elapsed time is well under idleTimeout, got %q", reason)
	}
}

func TestCallTimeoutGuardCheckIdleTripsWhenElapsedExceedsTimeout(t *testing.T) {
	g := newCallTimeoutGuard(context.Background())
	defer g.stop()
	g.idleTimeout = 10 * time.Millisecond
	g.lastActivity.Store(time.Now().Add(-time.Hour).UnixNano())

	g.checkIdle()

	if reason := g.reason(); !strings.Contains(reason, "idle timeout") {
		t.Fatalf("expected checkIdle to trip when elapsed time genuinely exceeds idleTimeout, got %q", reason)
	}
}

// TestCallTimeoutGuardCheckIdleIsNoOpAfterStop is a regression test for the
// stop/checkIdle race: a checkIdle firing that loses the race with stop()
// must neither trip the guard nor re-arm the timer. Simulated directly by
// calling checkIdle after stop() with an ancient lastActivity — without
// the stopped check it would trip.
func TestCallTimeoutGuardCheckIdleIsNoOpAfterStop(t *testing.T) {
	g := newCallTimeoutGuard(context.Background())
	g.armBodyTimeouts(time.Hour, 0)
	g.lastActivity.Store(time.Now().Add(-2 * time.Hour).UnixNano())
	g.stop()

	g.checkIdle()

	if reason := g.reason(); reason != "" {
		t.Fatalf("expected checkIdle after stop to be a no-op, got %q", reason)
	}
}

// TestCallTimeoutGuardSurvivesContinuousResetIdlePressure is a stress test:
// hammering resetIdle from a tight loop must never spuriously trip the
// idle timer, even though the timer's own firing schedule and the reset
// calls are running on different goroutines with no shared lock between
// them (by design — resetIdle only writes an atomic timestamp).
func TestCallTimeoutGuardSurvivesContinuousResetIdlePressure(t *testing.T) {
	g := newCallTimeoutGuard(context.Background())
	defer g.stop()
	g.armBodyTimeouts(5*time.Millisecond, 0)

	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		g.resetIdle()
	}

	if reason := g.reason(); reason != "" {
		t.Fatalf("expected the idle timer never to trip while resetIdle is called continuously, got %q", reason)
	}
}

func TestListToolsDecodesMultilineSSEData(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req Envelope
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch req.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "sid-multiline")
			writeTestRPC(w, req.ID, map[string]any{"protocolVersion": r.Header.Get("MCP-Protocol-Version"), "capabilities": map[string]any{}}, nil)
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			writeTestSSEEvents(w, []string{
				`{"jsonrpc":"2.0",`,
				`"id":1,`,
				`"result":{"tools":[{"name":"stories.multiline"}]}}`,
			})
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
	if len(tools) != 1 || tools[0].Name != "stories.multiline" {
		t.Fatalf("unexpected tools: %#v", tools)
	}
}

func TestSSEEventReaderSaturatesOversizedRetryWithoutOverflow(t *testing.T) {
	reader := newSSEEventReader(strings.NewReader("retry: 9223372036854775807\n\n"))
	evt, err := reader.NextEvent()
	if err != nil {
		t.Fatal(err)
	}
	if !evt.HasRetry {
		t.Fatal("expected retry field to be parsed")
	}
	if evt.Retry <= 0 {
		t.Fatalf("oversized retry overflowed to %s; want a positive saturated duration", evt.Retry)
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

func writeTestRPCSSE(w http.ResponseWriter, id json.RawMessage, result any, rpcErr any) {
	resp := map[string]any{"jsonrpc": "2.0", "id": id}
	if rpcErr != nil {
		resp["error"] = rpcErr
	} else {
		resp["result"] = result
	}
	payload, err := json.Marshal(resp)
	if err != nil {
		panic(err)
	}
	writeTestSSEEvents(w, []string{string(payload)})
}

func writeTestSSEEvents(w http.ResponseWriter, events ...[]string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, lines := range events {
		_, _ = w.Write([]byte("event: message\n"))
		for _, line := range lines {
			_, _ = w.Write([]byte("data: " + line + "\n"))
		}
		_, _ = w.Write([]byte("\n"))
	}
}

// writeTestSSEEventFlush writes and flushes one SSE event immediately, so a
// test server can hold a stream open between events (unlike writeTestSSEEvents,
// which writes every event in one shot with no flush in between).
func writeTestSSEEventFlush(w http.ResponseWriter, flusher http.Flusher, data string) {
	_, _ = w.Write([]byte("event: message\ndata: " + data + "\n\n"))
	flusher.Flush()
}

// fakeStreamSink is a test StreamSink that records what it received. onEvent,
// when set, lets a test hook into delivery (e.g. to unblock a fake upstream
// only after confirming an event was actually delivered incrementally).
type fakeStreamSink struct {
	started      bool
	startedCount int
	events       []StreamEvent
	onEvent      func(StreamEvent) error
}

func (s *fakeStreamSink) StreamStarted() {
	s.started = true
	s.startedCount++
}

func (s *fakeStreamSink) Event(evt StreamEvent) error {
	s.events = append(s.events, evt)
	if s.onEvent != nil {
		return s.onEvent(evt)
	}
	return nil
}
