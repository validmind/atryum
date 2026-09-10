package atryum

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/validmind/atryum/internal/api"
)

// fakeIdP implements just enough of OIDC discovery plus the device and
// refresh grants for the CLI to log in.
type fakeIdP struct {
	srv       *httptest.Server
	polls     atomic.Int32
	refreshes atomic.Int32
}

func newFakeIdP(t *testing.T) *fakeIdP {
	t.Helper()
	f := &fakeIdP{}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"device_authorization_endpoint": f.srv.URL + "/device",
			"token_endpoint":                f.srv.URL + "/token",
		})
	})
	mux.HandleFunc("/device", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil || r.Form.Get("client_id") != "atryum-cli" {
			// Mirror Auth0: a client without the device grant gets a JSON error.
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized_client", "error_description": "Grant type 'urn:ietf:params:oauth:grant-type:device_code' not allowed for the client."})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"device_code":               "dev-123",
			"user_code":                 "ABCD-EFGH",
			"verification_uri":          f.srv.URL + "/verify",
			"verification_uri_complete": f.srv.URL + "/verify?user_code=ABCD-EFGH",
			"expires_in":                60,
			"interval":                  0,
		})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		switch r.Form.Get("grant_type") {
		case "urn:ietf:params:oauth:grant-type:device_code":
			if r.Form.Get("device_code") != "dev-123" {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
				return
			}
			if f.polls.Add(1) == 1 {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "authorization_pending"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "access-1", "refresh_token": "refresh-1", "expires_in": 300, "token_type": "Bearer",
			})
		case "refresh_token":
			f.refreshes.Add(1)
			if r.Form.Get("refresh_token") != "refresh-1" {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "access-2", "refresh_token": "refresh-2", "expires_in": 300, "token_type": "Bearer",
			})
		default:
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "unsupported_grant_type"})
		}
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// fakeAtryum is a minimal operator API: auth config, /me, agents, keys.
type fakeAtryum struct {
	srv       *httptest.Server
	providers []api.AuthProvider
	agents    []api.OperatorAgent
	keys      map[string][]api.OperatorAPIKey
	accepted  map[string]bool // bearer tokens accepted
}

func newFakeAtryum(t *testing.T, providers []api.AuthProvider, accepted ...string) *fakeAtryum {
	t.Helper()
	f := &fakeAtryum{providers: providers, keys: map[string][]api.OperatorAPIKey{}, accepted: map[string]bool{}}
	for _, tok := range accepted {
		f.accepted[tok] = true
	}
	mux := http.NewServeMux()
	authed := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if len(f.providers) == 0 {
				next(w, r)
				return
			}
			tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !f.accepted[tok] {
				w.WriteHeader(http.StatusUnauthorized)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"message": "invalid token"}})
				return
			}
			next(w, r)
		}
	}
	mux.HandleFunc("/api/v1/auth/config", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(api.AuthConfigResponse{Providers: f.providers})
	})
	mux.HandleFunc("/api/v1/me", authed(func(w http.ResponseWriter, r *http.Request) {
		me := api.MeResponse{Role: "member", Method: "jwt", Email: "dev@example.com", UserID: "u1", AgentIDs: []string{}}
		if len(f.providers) == 0 {
			me = api.MeResponse{Role: "admin", Method: "none", AgentIDs: []string{}}
		}
		_ = json.NewEncoder(w).Encode(me)
	}))
	mux.HandleFunc("/api/v1/agents", authed(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(api.AgentListResponse{Items: f.agents})
		case http.MethodPost:
			var in struct{ Name string }
			_ = json.NewDecoder(r.Body).Decode(&in)
			a := api.OperatorAgent{CUID: "agent-" + strings.ToLower(strings.ReplaceAll(in.Name, " ", "-")), Name: in.Name, Enabled: true}
			f.agents = append(f.agents, a)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(a)
		}
	}))
	mux.HandleFunc("/api/v1/agents/", authed(func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/api/v1/agents/")
		agentID, tail, _ := strings.Cut(rest, "/keys")
		tail = strings.Trim(tail, "/")
		switch {
		case r.Method == http.MethodGet && tail == "":
			_ = json.NewEncoder(w).Encode(api.APIKeyListResponse{Items: f.keys[agentID]})
		case r.Method == http.MethodPost && tail == "":
			var in struct{ Name string }
			_ = json.NewDecoder(r.Body).Decode(&in)
			k := api.OperatorAPIKey{ID: "key-1", AgentID: agentID, Name: in.Name, KeyPrefix: "atr_fake1234", CreatedAt: time.Now(), Active: true}
			f.keys[agentID] = append(f.keys[agentID], k)
			k.Token = "atr_fake1234SECRETSECRETSECRETSECRETSECRET"
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(k)
		case r.Method == http.MethodDelete && tail != "":
			for i, k := range f.keys[agentID] {
				if k.ID == tail {
					f.keys[agentID][i].Active = false
				}
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func isolateHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ATRYUM_HOME", filepath.Join(home, ".atryum"))
	t.Setenv("ATRYUM_URL", "")
	return home
}

func fastDeviceFlow(t *testing.T) {
	t.Helper()
	oldMin, oldDef := deviceFlowMinInterval, deviceFlowDefaultInterval
	deviceFlowMinInterval, deviceFlowDefaultInterval = 5*time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() { deviceFlowMinInterval, deviceFlowDefaultInterval = oldMin, oldDef })
}

func TestSetupClaudeLogsInIssuesKeyAndInstallsHooks(t *testing.T) {
	home := isolateHome(t)
	fastDeviceFlow(t)
	idp := newFakeIdP(t)
	provider := api.AuthProvider{ID: "keycloak-atryum-admin", Name: "keycloak", Provider: "keycloak", Issuer: idp.srv.URL, ClientID: "atryum-admin", CLIClientID: "atryum-cli", Scopes: "openid profile email offline_access", Audience: "atryum"}
	srv := newFakeAtryum(t, []api.AuthProvider{provider}, "access-1", "access-2")

	var out bytes.Buffer
	io := cliIO{out: &out, in: bufio.NewReader(strings.NewReader(""))}

	// Without a CLI client the IdP refuses the grant; the error must name the
	// client and surface the IdP's description instead of a bare "no device_code".
	noCLI := newFakeAtryum(t, []api.AuthProvider{{ID: "kc", Name: "keycloak", Issuer: idp.srv.URL, ClientID: "atryum-admin"}})
	err := runSetupClaude([]string{"--url", noCLI.srv.URL, "--agent", "x", "-y"}, io)
	if err == nil || !strings.Contains(err.Error(), `client "atryum-admin"`) || !strings.Contains(err.Error(), "unauthorized_client") || !strings.Contains(err.Error(), "cli_client_id") {
		t.Fatalf("expected a descriptive device-grant error, got %v", err)
	}
	out.Reset()
	if err := runSetupClaude([]string{"--url", srv.srv.URL, "--agent", "Claude Laptop", "-y"}, io); err != nil {
		t.Fatalf("setup claude: %v\n%s", err, out.String())
	}
	text := out.String()
	for _, want := range []string{"/verify?user_code=ABCD-EFGH", "Logged in.", "Signed in as dev@example.com (member)", `Created agent "Claude Laptop"`, "Saved API key atr_fake1234", "installed hooks for claude-code"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected output to contain %q, got:\n%s", want, text)
		}
	}
	if idp.polls.Load() < 2 {
		t.Fatalf("expected the device flow to poll past authorization_pending, polls=%d", idp.polls.Load())
	}

	// Credentials stored with 0600 and keyed by server.
	credPath := filepath.Join(home, ".atryum", "credentials.json")
	info, err := os.Stat(credPath)
	if err != nil {
		t.Fatalf("credentials file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("credentials perms = %o, want 600", info.Mode().Perm())
	}
	store, _, err := loadCredentials()
	if err != nil || store.Servers[srv.srv.URL].AccessToken != "access-1" || store.Servers[srv.srv.URL].RefreshToken != "refresh-1" || store.Servers[srv.srv.URL].ClientID != "atryum-cli" {
		t.Fatalf("stored credentials wrong: %+v err=%v", store, err)
	}

	// Agent key on disk with 0600.
	keyPath := filepath.Join(home, ".atryum", "agent-key")
	raw, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("agent key: %v", err)
	}
	if strings.TrimSpace(string(raw)) != "atr_fake1234SECRETSECRETSECRETSECRETSECRET" {
		t.Fatalf("unexpected key file content %q", raw)
	}
	if info, _ := os.Stat(keyPath); info.Mode().Perm() != 0o600 {
		t.Fatalf("agent key perms = %o, want 600", info.Mode().Perm())
	}

	// Claude Code hooks point at the server and the key file via a token command.
	settingsRaw, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if err != nil {
		t.Fatalf("settings.json: %v", err)
	}
	settings := string(settingsRaw)
	if !strings.Contains(settings, "ATRYUM_URL="+srv.srv.URL+" ") {
		t.Fatalf("hooks should carry ATRYUM_URL, got %s", settings)
	}
	if !strings.Contains(settings, "ATRYUM_TOKEN_COMMAND='cat "+keyPath+"'") && !strings.Contains(settings, "ATRYUM_TOKEN_COMMAND=cat "+keyPath) {
		t.Fatalf("hooks should carry ATRYUM_TOKEN_COMMAND, got %s", settings)
	}
	if strings.Count(settings, `"PreToolUse": [`) != 1 {
		t.Fatalf("expected a single PreToolUse block, got %s", settings)
	}
	// Re-running replaces the hook commands instead of duplicating them.
	out.Reset()
	if err := runSetupClaude([]string{"--url", srv.srv.URL, "--agent", "Claude Laptop", "-y"}, io); err != nil {
		t.Fatalf("second setup claude: %v\n%s", err, out.String())
	}
	settingsRaw, _ = os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if n := strings.Count(string(settingsRaw), "atryum-hook.mjs"); n != 3 {
		t.Fatalf("expected exactly 3 hook commands after re-run, got %d:\n%s", n, settingsRaw)
	}
	if !strings.Contains(out.String(), `Using agent "Claude Laptop"`) {
		t.Fatalf("second run should reuse the agent, got:\n%s", out.String())
	}

	// agent key list / revoke through the same session.
	out.Reset()
	if err := runAgent([]string{"key", "list", "Claude Laptop", "--url", srv.srv.URL}, io); err != nil {
		t.Fatalf("key list: %v", err)
	}
	if !strings.Contains(out.String(), "key-1") || !strings.Contains(out.String(), "active") {
		t.Fatalf("key list output unexpected:\n%s", out.String())
	}
	out.Reset()
	if err := runAgent([]string{"key", "revoke", "Claude Laptop", "key-1", "--url", srv.srv.URL}, io); err != nil {
		t.Fatalf("key revoke: %v", err)
	}
	if !strings.Contains(out.String(), "revoked key key-1") {
		t.Fatalf("revoke output unexpected:\n%s", out.String())
	}

	// whoami and logout.
	out.Reset()
	if err := runWhoami([]string{"--url", srv.srv.URL}, io); err != nil {
		t.Fatalf("whoami: %v", err)
	}
	if !strings.Contains(out.String(), "user:    dev@example.com") {
		t.Fatalf("whoami output unexpected:\n%s", out.String())
	}
	if err := runLogout([]string{"--url", srv.srv.URL}, io); err != nil {
		t.Fatalf("logout: %v", err)
	}
	if err := runWhoami([]string{"--url", srv.srv.URL}, io); err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Fatalf("expected not-logged-in error after logout, got %v", err)
	}
}

func TestSessionRefreshesExpiredAccessToken(t *testing.T) {
	isolateHome(t)
	idp := newFakeIdP(t)
	provider := api.AuthProvider{ID: "kc", Name: "keycloak", Issuer: idp.srv.URL, ClientID: "atryum-admin", CLIClientID: "atryum-cli"}
	srv := newFakeAtryum(t, []api.AuthProvider{provider}, "access-2")

	store, _, _ := loadCredentials()
	store.Servers[srv.srv.URL] = serverCredentials{
		ProviderID: "kc", Issuer: idp.srv.URL, ClientID: "atryum-cli", TokenEndpoint: idp.srv.URL + "/token",
		AccessToken: "access-1", RefreshToken: "refresh-1", ExpiresAt: time.Now().Add(-time.Minute),
	}
	if err := saveCredentials(store); err != nil {
		t.Fatalf("save: %v", err)
	}
	var out bytes.Buffer
	io := cliIO{out: &out, in: bufio.NewReader(strings.NewReader(""))}
	if err := runWhoami([]string{"--url", srv.srv.URL}, io); err != nil {
		t.Fatalf("whoami with expired token: %v", err)
	}
	if idp.refreshes.Load() != 1 {
		t.Fatalf("expected one refresh, got %d", idp.refreshes.Load())
	}
	store, _, _ = loadCredentials()
	if got := store.Servers[srv.srv.URL]; got.AccessToken != "access-2" || got.RefreshToken != "refresh-2" {
		t.Fatalf("refreshed credentials not persisted: %+v", got)
	}
}

func TestSetupClaudeWorksWithoutIdP(t *testing.T) {
	home := isolateHome(t)
	srv := newFakeAtryum(t, nil)
	var out bytes.Buffer
	io := cliIO{out: &out, in: bufio.NewReader(strings.NewReader(""))}
	if err := runSetupClaude([]string{"--url", srv.srv.URL, "--agent", "Solo", "--yes"}, io); err != nil {
		t.Fatalf("setup claude (no auth): %v\n%s", err, out.String())
	}
	if strings.Contains(out.String(), "Logging in") {
		t.Fatalf("no-auth deployment should not attempt login:\n%s", out.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".atryum", "credentials.json")); !os.IsNotExist(err) {
		t.Fatalf("no credentials file expected without an IdP, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".atryum", "agent-key")); err != nil {
		t.Fatalf("agent key should still be issued: %v", err)
	}
}

func TestResolveAgentByIDAndName(t *testing.T) {
	agents := []api.OperatorAgent{{CUID: "a1", Name: "Alpha"}, {CUID: "a2", Name: "beta"}, {CUID: "a3", Name: "Beta"}}
	if a, err := resolveAgent(agents, "a2"); err != nil || a.CUID != "a2" {
		t.Fatalf("by id: %+v %v", a, err)
	}
	if a, err := resolveAgent(agents, "alpha"); err != nil || a.CUID != "a1" {
		t.Fatalf("case-insensitive name: %+v %v", a, err)
	}
	if a, err := resolveAgent(agents, "Beta"); err != nil || a.CUID != "a3" {
		t.Fatalf("exact name should win over loose match: %+v %v", a, err)
	}
	if _, err := resolveAgent(agents, "BETA"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("expected ambiguity error, got %v", err)
	}
	if _, err := resolveAgent(agents, "nope"); err == nil {
		t.Fatalf("expected not-found error")
	}
}

func TestHookEnvPrefixQuotesAndSorts(t *testing.T) {
	got := hookEnvPrefix(map[string]string{"ZZ": "plain", "ATRYUM_TOKEN_COMMAND": "cat /home/me/.atryum/agent-key", "B": "it's"})
	want := `ATRYUM_TOKEN_COMMAND='cat /home/me/.atryum/agent-key' B='it'\''s' ZZ=plain `
	if got != want {
		t.Fatalf("hookEnvPrefix = %q, want %q", got, want)
	}
	if hookEnvPrefix(nil) != "" {
		t.Fatalf("empty env should yield empty prefix")
	}
}
