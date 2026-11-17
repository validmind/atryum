package atryum

import (
	"bufio"
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/validmind/atryum/internal/config"
)

func TestResolveOperatorTokenPrefersConfiguredToken(t *testing.T) {
	dir := t.TempDir()
	token, source, path, err := resolveOperatorToken(config.ServerConfig{
		OperatorToken: "  chosen-token  ",
		DatabasePath:  filepath.Join(dir, "atryum.db"),
	})
	if err != nil || token != "chosen-token" || source != operatorTokenFromConfig || path != "" {
		t.Fatalf("got (%q, %v, %q, %v), want the configured token", token, source, path, err)
	}
	if _, err := os.Stat(filepath.Join(dir, operatorTokenFileName)); !os.IsNotExist(err) {
		t.Fatalf("a token file was written although a token was configured")
	}
}

func TestResolveOperatorTokenGeneratesOnceAndReuses(t *testing.T) {
	dir := t.TempDir()
	server := config.ServerConfig{DatabasePath: filepath.Join(dir, "atryum.db")}

	first, source, path, err := resolveOperatorToken(server)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if source != operatorTokenGenerated || path != filepath.Join(dir, operatorTokenFileName) {
		t.Fatalf("source=%v path=%q, want generated beside the database", source, path)
	}
	if len(first) < 40 {
		t.Fatalf("token %q is too short", first)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("token file mode = %o, want 600", perm)
	}

	second, source, _, err := resolveOperatorToken(server)
	if err != nil || second != first || source != operatorTokenFromFile {
		t.Fatalf("second resolve = (%q, %v, %v), want the stored token", second, source, err)
	}
}

func TestResolveOperatorTokenHonoursTokenFileAndRejectsEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "token")
	token, _, got, err := resolveOperatorToken(config.ServerConfig{OperatorTokenFile: path, DatabaseURL: "postgres://x"})
	if err != nil || got != path || token == "" {
		t.Fatalf("got (%q, %q, %v), want a token in %q", token, got, err, path)
	}
	if err := os.WriteFile(path, []byte("\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, _, _, err := resolveOperatorToken(config.ServerConfig{OperatorTokenFile: path}); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("empty token file: err = %v, want an error", err)
	}
}

func TestOperatorSignInURL(t *testing.T) {
	cases := []struct {
		server config.ServerConfig
		want   string
	}{
		{config.ServerConfig{PublicBaseURL: "https://atryum.example/", ListenAddr: ":8080"}, "https://atryum.example/ui/?token=t"},
		{config.ServerConfig{ListenAddr: "127.0.0.1:9090"}, "http://localhost:9090/ui/?token=t"},
		{config.ServerConfig{ListenAddr: ":8080"}, "http://localhost:8080/ui/?token=t"},
	}
	for _, tc := range cases {
		if got := operatorSignInURL(tc.server, "t"); got != tc.want {
			t.Fatalf("operatorSignInURL(%+v) = %q, want %q", tc.server, got, tc.want)
		}
	}
}

func TestCLISessionSendsOperatorTokenFromEnv(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/config":
			_, _ = w.Write([]byte(`{"providers":[],"operator_token_required":true}`))
		case "/api/v1/me":
			gotAuth = r.Header.Get("Authorization")
			_, _ = w.Write([]byte(`{"role":"admin","method":"operator_token","agent_ids":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	io := cliIO{out: &bytes.Buffer{}, in: bufio.NewReader(strings.NewReader(""))}

	t.Setenv("ATRYUM_OPERATOR_TOKEN", "")
	if _, err := sessionFor(context.Background(), srv.URL, io, false); err == nil || !strings.Contains(err.Error(), "ATRYUM_OPERATOR_TOKEN") {
		t.Fatalf("without the env var: err = %v, want a hint to set ATRYUM_OPERATOR_TOKEN", err)
	}

	t.Setenv("ATRYUM_OPERATOR_TOKEN", "cli-operator-token")
	client, err := sessionFor(context.Background(), srv.URL, io, false)
	if err != nil {
		t.Fatalf("sessionFor: %v", err)
	}
	if _, err := client.me(context.Background()); err != nil {
		t.Fatalf("me: %v", err)
	}
	if gotAuth != "Bearer cli-operator-token" {
		t.Fatalf("Authorization = %q, want the operator token", gotAuth)
	}
}
