package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/validmind/atryum/internal/mcp"
	"github.com/validmind/atryum/internal/store"
)

const (
	testBearerToken = "sk-upstream-bearer-secret"
	testHeaderValue = "hdr-upstream-secret"
	testEnvValue    = "env-upstream-secret"
)

func newCredentialTestService(t *testing.T) (*ServerOperatorService, *store.ServerRepo) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.InitDB(db); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	serverRepo := store.NewServerRepo(db)
	return NewServerOperatorService(serverRepo, store.NewOAuthRepo(db), nil, 5*time.Second, ""), serverRepo
}

// assertNoSecrets fails if any test secret appears in the JSON the operator
// API would write for v.
func assertNoSecrets(t *testing.T, v any) {
	t.Helper()
	body, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, secret := range []string{testBearerToken, testHeaderValue, testEnvValue} {
		if strings.Contains(string(body), secret) {
			t.Fatalf("operator response returns stored secret %q: %s", secret, body)
		}
	}
	if strings.Contains(string(body), `"auth_token"`) {
		t.Fatalf("operator response contains auth_token: %s", body)
	}
}

func storedServer(t *testing.T, repo *store.ServerRepo, name string) mcp.Upstream {
	t.Helper()
	upstream, err := repo.GetServerAny(context.Background(), name)
	if err != nil {
		t.Fatalf("GetServerAny(%q): %v", name, err)
	}
	return upstream
}

func headerValue(headers []mcp.AuthHeader, name string) string {
	for _, h := range headers {
		if strings.EqualFold(h.Name, name) {
			return h.Value
		}
	}
	return ""
}

func createHTTPServerWithSecrets(t *testing.T, svc *ServerOperatorService) OperatorServer {
	t.Helper()
	server, err := svc.Upsert(context.Background(), "", OperatorServerUpsertRequest{
		Name:        "echo",
		Mode:        string(mcp.UpstreamModeHTTP),
		BaseURL:     "https://echo.example/mcp",
		AuthToken:   testBearerToken,
		AuthHeaders: []mcp.AuthHeader{{Name: "X-Upstream-Key", Value: testHeaderValue}},
	})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	return server
}

func TestOperatorServerViewNeverReturnsCredentials(t *testing.T) {
	ctx := context.Background()
	svc, _ := newCredentialTestService(t)

	created := createHTTPServerWithSecrets(t, svc)
	assertNoSecrets(t, created)
	if !created.HasAuthToken {
		t.Fatalf("has_auth_token = false, want true")
	}
	if len(created.AuthHeaders) != 1 || created.AuthHeaders[0].Name != "X-Upstream-Key" ||
		created.AuthHeaders[0].Value != "" || !created.AuthHeaders[0].HasValue {
		t.Fatalf("auth_headers = %+v, want one redacted X-Upstream-Key with has_value", created.AuthHeaders)
	}
	// HTTP custom headers are stored in the env column; that copy is write-only too.
	if got := created.Env["X-Upstream-Key"]; got != "" {
		t.Fatalf("env[X-Upstream-Key] = %q, want redacted", got)
	}

	got, err := svc.Get(ctx, "echo")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	assertNoSecrets(t, got)

	list, err := svc.List(ctx, mcp.ServerFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if list.Total != 1 {
		t.Fatalf("list total = %d, want 1", list.Total)
	}
	assertNoSecrets(t, list)
}

func TestOperatorUpsertWithBlankSecretsKeepsStoredOnes(t *testing.T) {
	ctx := context.Background()
	svc, repo := newCredentialTestService(t)
	createHTTPServerWithSecrets(t, svc)

	// What a form re-save sends: the secrets it was never shown, left blank.
	saved, err := svc.Upsert(ctx, "echo", OperatorServerUpsertRequest{
		Mode:        string(mcp.UpstreamModeHTTP),
		BaseURL:     "https://echo.example/mcp",
		AuthHeaders: []mcp.AuthHeader{{Name: "x-upstream-key", Value: ""}},
	})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	assertNoSecrets(t, saved)

	stored := storedServer(t, repo, "echo")
	if stored.AuthToken != testBearerToken {
		t.Fatalf("stored auth_token = %q, want it kept", stored.AuthToken)
	}
	if got := headerValue(stored.AuthHeaders, "X-Upstream-Key"); got != testHeaderValue {
		t.Fatalf("stored header = %q, want it kept", got)
	}
}

func TestOperatorUpsertReplacesAndClearsSecrets(t *testing.T) {
	ctx := context.Background()
	svc, repo := newCredentialTestService(t)
	createHTTPServerWithSecrets(t, svc)

	if _, err := svc.Upsert(ctx, "echo", OperatorServerUpsertRequest{
		Mode:        string(mcp.UpstreamModeHTTP),
		BaseURL:     "https://echo.example/mcp",
		AuthToken:   "sk-rotated",
		AuthHeaders: []mcp.AuthHeader{{Name: "X-Upstream-Key", Value: "hdr-rotated"}},
	}); err != nil {
		t.Fatalf("Upsert rotate: %v", err)
	}
	stored := storedServer(t, repo, "echo")
	if stored.AuthToken != "sk-rotated" || headerValue(stored.AuthHeaders, "X-Upstream-Key") != "hdr-rotated" {
		t.Fatalf("rotate: stored token=%q header=%q", stored.AuthToken, headerValue(stored.AuthHeaders, "X-Upstream-Key"))
	}

	cleared, err := svc.Upsert(ctx, "echo", OperatorServerUpsertRequest{
		Mode:           string(mcp.UpstreamModeHTTP),
		BaseURL:        "https://echo.example/mcp",
		ClearAuthToken: true,
		AuthHeaders:    []mcp.AuthHeader{{Name: "X-Upstream-Key"}},
	})
	if err != nil {
		t.Fatalf("Upsert clear: %v", err)
	}
	if cleared.HasAuthToken {
		t.Fatalf("has_auth_token = true after clear_auth_token")
	}
	stored = storedServer(t, repo, "echo")
	if stored.AuthToken != "" {
		t.Fatalf("stored auth_token = %q, want cleared", stored.AuthToken)
	}
	if got := headerValue(stored.AuthHeaders, "X-Upstream-Key"); got != "hdr-rotated" {
		t.Fatalf("stored header = %q, want kept while clearing the token", got)
	}
}

func TestOperatorUpsertDropsOmittedAndUnknownBlankHeaders(t *testing.T) {
	ctx := context.Background()
	svc, repo := newCredentialTestService(t)
	createHTTPServerWithSecrets(t, svc)

	if _, err := svc.Upsert(ctx, "echo", OperatorServerUpsertRequest{
		Mode:        string(mcp.UpstreamModeHTTP),
		BaseURL:     "https://echo.example/mcp",
		AuthHeaders: []mcp.AuthHeader{{Name: "X-New-Header", Value: ""}, {Name: "X-Other", Value: "set"}},
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	stored := storedServer(t, repo, "echo")
	if headerValue(stored.AuthHeaders, "X-Upstream-Key") != "" {
		t.Fatalf("omitted header was kept: %+v", stored.AuthHeaders)
	}
	if headerValue(stored.AuthHeaders, "X-New-Header") != "" {
		t.Fatalf("blank header with nothing stored was saved: %+v", stored.AuthHeaders)
	}
	if headerValue(stored.AuthHeaders, "X-Other") != "set" {
		t.Fatalf("new header not saved: %+v", stored.AuthHeaders)
	}
	if stored.AuthToken != testBearerToken {
		t.Fatalf("stored auth_token = %q, want it kept", stored.AuthToken)
	}
}

func TestOperatorStdioEnvIsWriteOnly(t *testing.T) {
	ctx := context.Background()
	svc, repo := newCredentialTestService(t)

	created, err := svc.Upsert(ctx, "", OperatorServerUpsertRequest{
		Name:    "local-tool",
		Mode:    string(mcp.UpstreamModeStdio),
		Command: "some-mcp",
		Env:     map[string]string{"API_KEY": testEnvValue, "LOG_LEVEL": "debug"},
	})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	assertNoSecrets(t, created)
	if len(created.Env) != 2 || created.Env["API_KEY"] != "" || created.Env["LOG_LEVEL"] != "" {
		t.Fatalf("env = %+v, want both keys with blank values", created.Env)
	}

	if _, err := svc.Upsert(ctx, "local-tool", OperatorServerUpsertRequest{
		Mode:    string(mcp.UpstreamModeStdio),
		Command: "some-mcp",
		Env:     map[string]string{"API_KEY": "", "NEW_VAR": "x"},
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	stored := storedServer(t, repo, "local-tool")
	want := map[string]string{"API_KEY": testEnvValue, "NEW_VAR": "x"}
	if len(stored.Env) != len(want) || stored.Env["API_KEY"] != want["API_KEY"] || stored.Env["NEW_VAR"] != want["NEW_VAR"] {
		t.Fatalf("stored env = %+v, want %+v (blank kept, omitted LOG_LEVEL removed)", stored.Env, want)
	}
}

func TestOperatorModeSwitchDoesNotCarryHTTPCredentials(t *testing.T) {
	ctx := context.Background()
	svc, repo := newCredentialTestService(t)
	createHTTPServerWithSecrets(t, svc)

	if _, err := svc.Upsert(ctx, "echo", OperatorServerUpsertRequest{
		Mode:    string(mcp.UpstreamModeStdio),
		Command: "some-mcp",
		Env:     map[string]string{"X-Upstream-Key": ""},
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	stored := storedServer(t, repo, "echo")
	if stored.AuthToken != "" {
		t.Fatalf("stdio server kept the HTTP bearer token")
	}
	if stored.Env["X-Upstream-Key"] != "" {
		t.Fatalf("stdio env picked up the HTTP header secret")
	}
}
