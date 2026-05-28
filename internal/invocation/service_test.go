package invocation_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"atryum/internal/config"
	"atryum/internal/invocation"
	"atryum/internal/invocation/policy"
	"atryum/internal/mcp"
	"atryum/internal/store"

	_ "modernc.org/sqlite"
)

func TestInvokeAndInspectHTTP(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		switch body["method"] {
		case "initialize":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      body["id"],
				"result":  map[string]any{"serverInfo": map[string]any{"name": "fake-github", "version": "0.1.0"}, "capabilities": map[string]any{}},
			})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/call":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"content": []map[string]any{{"type": "text", "text": "ok"}}}})
		default:
			t.Errorf("unexpected method: %v", body["method"])
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer upstream.Close()

	service := newTestService(t, config.Config{
		Defaults:  config.DefaultsConfig{RequestTimeoutSeconds: 5},
		Upstreams: []config.UpstreamConfig{{Name: "github", Mode: "http", BaseURL: upstream.URL, Enabled: true, TimeoutSeconds: 5}},
	})
	go approveNextInvocation(t, service, 50*time.Millisecond)
	assertInvokeLifecycle(t, service, "github", "github.issue.get")
}

func TestInvokeAndInspectStdio(t *testing.T) {
	script := filepath.Join(t.TempDir(), "fake-mcp.sh")
	content := `#!/usr/bin/env bash
set -euo pipefail
while IFS= read -r line; do
  if [[ -z "$line" ]]; then
    continue
  fi
  if [[ "$line" == *'"method":"initialize"'* ]]; then
    printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"serverInfo":{"name":"fake-shortcut","version":"0.1.0"},"capabilities":{}}}'
  elif [[ "$line" == *'"method":"notifications/initialized"'* ]]; then
    continue
  elif [[ "$line" == *'"method":"tools/call"'* ]]; then
    printf '%s\n' '{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"ok"}]}}'
    exit 0
  fi
done
`
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}

	service := newTestService(t, config.Config{
		Defaults:  config.DefaultsConfig{RequestTimeoutSeconds: 5},
		Upstreams: []config.UpstreamConfig{{Name: "shortcut", Mode: "stdio", Command: script, Enabled: true, TimeoutSeconds: 5}},
	})
	go approveNextInvocation(t, service, 50*time.Millisecond)
	assertInvokeLifecycle(t, service, "shortcut", "stories-search")
}

func TestBusinessToolErrorMarksInvocationFailed(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"result": map[string]any{
				"content": []map[string]any{{"type": "text", "text": "Tool stories-does-not-exist not found"}},
			},
		})
	}))
	defer upstream.Close()

	service := newTestService(t, config.Config{
		Defaults:  config.DefaultsConfig{RequestTimeoutSeconds: 5},
		Upstreams: []config.UpstreamConfig{{Name: "shortcut", Mode: "http", BaseURL: upstream.URL, Enabled: true, TimeoutSeconds: 5}},
	})
	go approveNextInvocation(t, service, 50*time.Millisecond)
	resp, err := service.Invoke(context.Background(), invocation.CreateInvocationRequest{Server: "shortcut", Tool: "stories-does-not-exist", Input: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != invocation.StatusFailed {
		t.Fatalf("expected failed status, got %s", resp.Status)
	}
	if len(resp.Error) == 0 {
		t.Fatal("expected error payload")
	}
}

func TestResolverBootstrapsServersFromConfigWhenDBEmpty(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := store.InitDB(db); err != nil {
		t.Fatal(err)
	}
	repo := store.NewServerRepo(db)
	resolver := mcp.NewResolver(repo, config.Config{Upstreams: []config.UpstreamConfig{{Name: "bootstrap", Mode: "http", BaseURL: "http://example.com", Enabled: true, TimeoutSeconds: 7}}})
	if err := resolver.BootstrapIfEmpty(context.Background()); err != nil {
		t.Fatal(err)
	}
	upstream, err := repo.GetServer(context.Background(), "bootstrap")
	if err != nil {
		t.Fatal(err)
	}
	if upstream.BaseURL != "http://example.com" {
		t.Fatalf("unexpected base url %q", upstream.BaseURL)
	}
	if upstream.Status.ConnectionStatus != mcp.ConnectionStatusUnknown {
		t.Fatalf("unexpected initial connection status %q", upstream.Status.ConnectionStatus)
	}
}

func TestServerStatusPersistsAfterUpdate(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := store.InitDB(db); err != nil {
		t.Fatal(err)
	}
	repo := store.NewServerRepo(db)
	upstream := mcp.FromConfig(config.UpstreamConfig{Name: "status-demo", Mode: "http", BaseURL: "http://example.com", Enabled: true, TimeoutSeconds: 5})
	if err := repo.CreateServer(context.Background(), upstream); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	errSummary := "unauthorized"
	action := "refresh credentials"
	status := mcp.ServerStatus{
		AuthType:         mcp.AuthTypeBearer,
		ConnectionStatus: mcp.ConnectionStatusNeedsAttention,
		AuthStatus:       mcp.AuthStatusInvalid,
		ReauthNeeded:     true,
		LastCheckedAt:    &now,
		LastCheckOK:      false,
		LastErrorSummary: &errSummary,
		ActionRequired:   &action,
	}
	if err := repo.UpdateServerStatus(context.Background(), "status-demo", status); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.GetServerAny(context.Background(), "status-demo")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status.AuthStatus != mcp.AuthStatusInvalid {
		t.Fatalf("unexpected auth status %q", stored.Status.AuthStatus)
	}
	if !stored.Status.ReauthNeeded {
		t.Fatal("expected reauth needed")
	}
	if stored.Status.LastErrorSummary == nil || *stored.Status.LastErrorSummary != errSummary {
		t.Fatal("expected last error summary")
	}
	if stored.Status.ActionRequired == nil || *stored.Status.ActionRequired != action {
		t.Fatal("expected action required")
	}
}

func newTestService(t *testing.T, cfg config.Config) *invocation.Service {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.InitDB(db); err != nil {
		t.Fatal(err)
	}
	serverRepo := store.NewServerRepo(db)
	resolver := mcp.NewResolver(serverRepo, cfg)
	if err := resolver.BootstrapIfEmpty(context.Background()); err != nil {
		t.Fatal(err)
	}
	return invocation.NewService(store.NewInvocationRepo(db), store.NewEventRepo(db), resolver, mcp.NewHTTPClient(), policy.AlwaysApproveProvider{}, 5*time.Second, nil, nil, nil, nil)
}

func approveNextInvocation(t *testing.T, service *invocation.Service, delay time.Duration) {
	time.Sleep(delay)
	list, err := service.List(context.Background(), invocation.InvocationListFilter{Limit: 10})
	// Return silently when there are no pending invocations — this is expected
	// when AlwaysApproveProvider completes the invocation before this goroutine
	// runs. Calling t.Errorf after the test has completed causes a panic.
	if err != nil || len(list.Items) == 0 {
		return
	}
	pending := list.Items[0]
	if pending.Status != invocation.StatusPendingApproval {
		return
	}
	if err := service.Approve(context.Background(), pending.InvocationID); err != nil {
		t.Errorf("approve invocation: %v", err)
	}
}

func jsonContains(raw json.RawMessage, needle string) bool {
	return string(raw) != "" && strings.Contains(string(raw), needle)
}

func assertInvokeLifecycle(t *testing.T, service *invocation.Service, server, tool string) {
	t.Helper()
	key := "demo-key-" + server + "-" + tool
	resp, err := service.Invoke(context.Background(), invocation.CreateInvocationRequest{Server: server, Tool: tool, Input: map[string]any{"n": 1}, IdempotencyKey: &key})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != invocation.StatusSucceeded {
		t.Fatalf("got status %s", resp.Status)
	}
	if resp.InvocationID == "" {
		t.Fatal("expected invocation id")
	}
	if resp.ServerName != server {
		t.Fatalf("expected server name %q, got %q", server, resp.ServerName)
	}
	if resp.ToolName != tool {
		t.Fatalf("expected tool name %q, got %q", tool, resp.ToolName)
	}
	if resp.Approval == nil {
		t.Fatal("expected approval to be set")
	}
	if resp.Approval.Status != "auto_approved" {
		t.Fatalf("expected auto_approved status, got %q", resp.Approval.Status)
	}
	if string(resp.Input) != `{"n":1}` {
		t.Fatalf("expected input to be surfaced, got %s", string(resp.Input))
	}
	if resp.SubmittedAt.IsZero() {
		t.Fatal("expected submitted_at")
	}
	if resp.CompletedAt == nil {
		t.Fatal("expected completed_at")
	}
	if len(resp.Result) == 0 {
		t.Fatal("expected result")
	}

	again, err := service.Invoke(context.Background(), invocation.CreateInvocationRequest{Server: server, Tool: tool, Input: map[string]any{"n": 1}, IdempotencyKey: &key})
	if err != nil {
		t.Fatal(err)
	}
	if again.InvocationID != resp.InvocationID {
		t.Fatal("expected idempotent replay to return same invocation")
	}

	events, err := service.Events(context.Background(), resp.InvocationID, invocation.EventListFilter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(events.Items) < 2 {
		t.Fatalf("expected events, got %d", len(events.Items))
	}
	if !jsonContains(events.Items[0].Data, `"input":{"n":1}`) || !jsonContains(events.Items[0].Data, `"arguments":{"n":1}`) {
		t.Fatalf("expected surfaced input/arguments in event payload, got %s", string(events.Items[0].Data))
	}
	if events.Items[0].Type == "" {
		t.Fatal("expected event type")
	}
	if events.Items[0].Timestamp.IsZero() {
		t.Fatal("expected event timestamp")
	}
}
