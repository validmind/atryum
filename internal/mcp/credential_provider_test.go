package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/validmind/atryum/internal/config"
)

type callerKey struct{}

// sessionPerTokenServer issues a session per bearer token and records, for
// each tools/call, the token and session it arrived with.
type sessionPerTokenServer struct {
	mu    sync.Mutex
	calls []string // "token|session"
	deny  map[string]bool
}

func (s *sessionPerTokenServer) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		s.mu.Lock()
		denied := s.deny[token]
		s.mu.Unlock()
		if denied {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var req Envelope
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode: %v", err)
		}
		switch req.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "sid-"+token)
			writeTestRPC(w, req.ID, map[string]any{"protocolVersion": r.Header.Get("MCP-Protocol-Version"), "capabilities": map[string]any{}}, nil)
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/call":
			s.mu.Lock()
			s.calls = append(s.calls, token+"|"+r.Header.Get("Mcp-Session-Id"))
			s.mu.Unlock()
			writeTestRPC(w, req.ID, map[string]any{"content": []map[string]any{{"type": "text", "text": "ok"}}}, nil)
		default:
			t.Fatalf("unexpected method %q", req.Method)
		}
	})
}

type staticCredentials struct{ token string }

func (s staticCredentials) GetCredential(context.Context, Upstream) (AccessTokenView, error) {
	return AccessTokenView{AccessToken: s.token}, nil
}

func TestCredentialProviderGivesEachCallerItsOwnTokenAndSession(t *testing.T) {
	upstreamServer := &sessionPerTokenServer{}
	server := httptest.NewServer(upstreamServer.handler(t))
	defer server.Close()

	resolver := NewResolver(fakeServerStore{upstreams: []Upstream{{
		Name: "shortcut", Mode: UpstreamModeHTTP, BaseURL: server.URL, Enabled: true,
	}}}, config.Config{}).
		WithCredentials(staticCredentials{token: "stored-shared"}).
		WithCredentialProvider(func(ctx context.Context, u Upstream) (Upstream, error) {
			caller, _ := ctx.Value(callerKey{}).(string)
			if caller == "" {
				return u, nil // zero credential: the row's own token only
			}
			u.AuthToken = "tok-" + caller
			u.SessionKey = u.Name + "/" + caller
			return u, nil
		})
	client := NewHTTPClient()
	client.httpClient = server.Client()

	for _, caller := range []string{"sam", "priya", "sam"} {
		ctx := context.WithValue(context.Background(), callerKey{}, caller)
		upstream, err := resolver.ResolveContext(ctx, "shortcut")
		if err != nil {
			t.Fatalf("resolve %s: %v", caller, err)
		}
		if !upstream.ResolvedByProvider {
			t.Fatal("upstream not marked as resolved by the provider")
		}
		if _, err := client.Invoke(ctx, upstream, "echo", map[string]any{}, nil); err != nil {
			t.Fatalf("invoke %s: %v", caller, err)
		}
	}
	want := []string{"tok-sam|sid-tok-sam", "tok-priya|sid-tok-priya", "tok-sam|sid-tok-sam"}
	if strings.Join(upstreamServer.calls, ",") != strings.Join(want, ",") {
		t.Fatalf("calls = %v, want %v", upstreamServer.calls, want)
	}

	// A zero credential never falls back to the stored OAuth token.
	upstream, err := resolver.ResolveContext(context.Background(), "shortcut")
	if err != nil {
		t.Fatalf("resolve without caller: %v", err)
	}
	if upstream.AuthToken != "" {
		t.Fatalf("zero credential carried token %q; the stored overlay must not apply", upstream.AuthToken)
	}
}

func TestCredentialProviderErrorAbortsResolve(t *testing.T) {
	sentinel := errors.New("upstream_not_connected: Sam hasn't connected Shortcut")
	resolver := NewResolver(fakeServerStore{upstreams: []Upstream{{
		Name: "shortcut", Mode: UpstreamModeHTTP, BaseURL: "https://mcp.shortcut.test", Enabled: true,
	}}}, config.Config{}).
		WithCredentials(staticCredentials{token: "stored-shared"}).
		WithCredentialProvider(func(context.Context, Upstream) (Upstream, error) { return Upstream{}, sentinel })
	if _, err := resolver.ResolveContext(context.Background(), "shortcut"); !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want the provider's error", err)
	}
	all, err := resolver.ListAll(context.Background())
	if err != nil || len(all) != 0 {
		t.Fatalf("ListAll = %v, %v; want the server skipped", all, err)
	}
}

func TestUnauthorizedClearsSessionAndCallsBackFirst(t *testing.T) {
	upstreamServer := &sessionPerTokenServer{deny: map[string]bool{}}
	server := httptest.NewServer(upstreamServer.handler(t))
	defer server.Close()
	client := NewHTTPClient()
	client.httpClient = server.Client()

	called := 0
	upstream := Upstream{
		Name: "shortcut", Mode: UpstreamModeHTTP, BaseURL: server.URL, Enabled: true,
		AuthToken: "tok-sam", SessionKey: "shortcut/sam",
		OnUnauthorized: func(context.Context) { called++ },
	}
	if _, err := client.Invoke(context.Background(), upstream, "echo", map[string]any{}, nil); err != nil {
		t.Fatalf("first invoke: %v", err)
	}
	if client.getSession(sessionKey(context.Background(), upstream)) == "" {
		t.Fatal("session not cached under its key")
	}
	upstreamServer.mu.Lock()
	upstreamServer.deny["tok-sam"] = true
	upstreamServer.mu.Unlock()
	result, err := client.Invoke(context.Background(), upstream, "echo", map[string]any{}, nil)
	if err == nil && !result.Failed {
		t.Fatal("invoke after revocation succeeded")
	}
	if err == nil || !strings.Contains(err.Error(), "rejected the credential (HTTP 401)") {
		t.Fatalf("err = %v, want it to say the upstream rejected the credential", err)
	}
	if called != 1 {
		t.Fatalf("OnUnauthorized called %d times, want 1 before the error returned", called)
	}
	if client.getSession(sessionKey(context.Background(), upstream)) != "" {
		t.Fatal("session for the rejected credential was kept")
	}
}

func TestPurposeAndInvocationOnContext(t *testing.T) {
	if got := PurposeFromContext(context.Background()); got != PurposeCall {
		t.Fatalf("default purpose = %q", got)
	}
	ctx := WithInvocationID(WithPurpose(context.Background(), PurposeExecute), "inv_1")
	if PurposeFromContext(ctx) != PurposeExecute || InvocationIDFromContext(ctx) != "inv_1" {
		t.Fatal("purpose or invocation lost")
	}
}

// The provider is asked about HTTP upstreams only: a stdio server has no
// request to carry a token, so a provider refusing a caller (no account
// yet) must not keep them from a local server that needs none.
func TestProviderSkipsStdioUpstreams(t *testing.T) {
	cfg := config.Config{Upstreams: []config.UpstreamConfig{
		{Name: "local", Mode: "stdio", Command: "cat", Enabled: true},
		{Name: "hosted", Mode: "http", BaseURL: "http://127.0.0.1:1", Enabled: true},
	}}
	var asked []string
	r := NewResolver(nil, cfg).WithCredentialProvider(func(ctx context.Context, u Upstream) (Upstream, error) {
		asked = append(asked, u.Name)
		return Upstream{}, errors.New("no account")
	})
	if u, err := r.ResolveContext(context.Background(), "local"); err != nil || u.Command != "cat" || u.ResolvedByProvider {
		t.Fatalf("stdio upstream = %+v, %v", u, err)
	}
	if _, err := r.ResolveContext(context.Background(), "hosted"); err == nil {
		t.Fatal("the provider's refusal of the HTTP upstream was dropped")
	}
	if strings.Join(asked, ",") != "hosted" {
		t.Fatalf("provider asked about %v, want hosted only", asked)
	}
	if u, err := r.Lookup(context.Background(), "hosted"); err != nil || u.ResolvedByProvider || len(asked) != 1 {
		t.Fatalf("Lookup = %+v, %v, asked %v; want the server with no provider call", u, err, asked)
	}
}
