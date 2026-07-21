package store

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/validmind/atryum/internal/invocation"
	"github.com/validmind/atryum/internal/mcp"

	_ "modernc.org/sqlite"
)

func openTestDB(t *testing.T) (*sql.DB, func()) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	return db, func() {
		db.Close()
		os.RemoveAll(dir)
	}
}

func TestInitDB_FreshDatabase(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	if err := InitDB(db); err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	// Verify schema_migrations table has 3 entries
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if count != 27 {
		t.Fatalf("expected 27 migrations, got %d", count)
	}

	// Verify all tables exist
	tables := []string{"invocations", "invocation_events", "mcp_servers", "oauth_credentials", "oauth_connect_sessions", "approval_rules", "managed_agent_sessions", "managed_agent_bindings", "external_sessions", "plans", "plan_events"}
	for _, table := range tables {
		var name string
		if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name); err != nil {
			t.Errorf("table %s not found: %v", table, err)
		}
	}

}

func TestInitDB_Idempotent(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	if err := InitDB(db); err != nil {
		t.Fatalf("first InitDB: %v", err)
	}
	if err := InitDB(db); err != nil {
		t.Fatalf("second InitDB: %v", err)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if count != 27 {
		t.Fatalf("expected 27 migrations after double init, got %d", count)
	}
}

func TestInvocationRepo_CRUD(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	if err := InitDB(db); err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	repo := NewInvocationRepo(db)
	ctx := context.Background()

	inv := invocation.Invocation{
		InvocationID: "inv-1",
		RequestID:    strPtr("req-1"),
		Tool:         "read_file",
		Upstream:     "github",
		Status:       "pending",
		Input:        []byte(`{"path": "/tmp/test"}`),
		SubmittedAt:  time.Now().UTC(),
	}

	// Create
	if err := repo.Create(ctx, inv); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Get
	got, err := repo.Get(ctx, "inv-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.InvocationID != "inv-1" || got.Tool != "read_file" {
		t.Errorf("Get mismatch: %+v", got)
	}
	if string(got.Input) != `{"path": "/tmp/test"}` {
		t.Errorf("expected request_json to round-trip as input, got %s", string(got.Input))
	}
	if got.Upstream != "github" {
		t.Errorf("expected upstream github, got %s", got.Upstream)
	}

	// Update
	inv.Status = "completed"
	resp := []byte(`{"content": "hello"}`)
	inv.Response = resp
	now := time.Now().UTC()
	inv.CompletedAt = &now
	if err := repo.UpdateResult(ctx, inv); err != nil {
		t.Fatalf("UpdateResult: %v", err)
	}

	got2, err := repo.Get(ctx, "inv-1")
	if err != nil {
		t.Fatalf("Get after update: %v", err)
	}
	if got2.Status != "completed" {
		t.Errorf("expected completed, got %s", got2.Status)
	}

	// List
	invs, total, err := repo.List(ctx, invocation.InvocationListFilter{Limit: 10})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 1 || len(invs) != 1 {
		t.Errorf("List: total=%d len=%d", total, len(invs))
	}

	// List with filter
	invs, total, err = repo.List(ctx, invocation.InvocationListFilter{Server: "github", Limit: 10})
	if err != nil {
		t.Fatalf("List with filter: %v", err)
	}
	if total != 1 {
		t.Errorf("filtered List: total=%d", total)
	}

	// GetByIdempotencyKey
	inv2 := invocation.Invocation{
		InvocationID:   "inv-2",
		Tool:           "write_file",
		Upstream:       "shortcut",
		Status:         "pending",
		Input:          []byte(`{"path": "/tmp/out"}`),
		IdempotencyKey: strPtr("idem-abc"),
		SubmittedAt:    time.Now().UTC(),
	}
	if err := repo.Create(ctx, inv2); err != nil {
		t.Fatalf("Create inv2: %v", err)
	}
	got3, err := repo.GetByIdempotencyKey(ctx, "idem-abc")
	if err != nil {
		t.Fatalf("GetByIdempotencyKey: %v", err)
	}
	if got3.InvocationID != "inv-2" {
		t.Errorf("idem key mismatch: %s", got3.InvocationID)
	}
}

func TestInvocationRepo_UpdateSummary(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	if err := InitDB(db); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	repo := NewInvocationRepo(db)
	ctx := context.Background()
	inv := invocation.Invocation{
		InvocationID:   "inv-summary",
		Tool:           "read_file",
		Upstream:       "github",
		Status:         "completed",
		Input:          []byte(`{"path":"/tmp/test"}`),
		IdempotencyKey: strPtr("summary-key"),
		SubmittedAt:    time.Now().UTC(),
	}
	if err := repo.Create(ctx, inv); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := repo.UpdateSummary(ctx, "inv-summary", "Read /tmp/test."); err != nil {
		t.Fatalf("UpdateSummary: %v", err)
	}

	got, err := repo.Get(ctx, "inv-summary")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Summary == nil || *got.Summary != "Read /tmp/test." {
		t.Fatalf("Get summary = %#v", got.Summary)
	}
	byKey, err := repo.GetByIdempotencyKey(ctx, "summary-key")
	if err != nil {
		t.Fatalf("GetByIdempotencyKey: %v", err)
	}
	if byKey.Summary == nil || *byKey.Summary != "Read /tmp/test." {
		t.Fatalf("GetByIdempotencyKey summary = %#v", byKey.Summary)
	}
	items, total, err := repo.List(ctx, invocation.InvocationListFilter{Limit: 10})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 1 || len(items) != 1 {
		t.Fatalf("List total=%d len=%d", total, len(items))
	}
	if items[0].Summary == nil || *items[0].Summary != "Read /tmp/test." {
		t.Fatalf("List summary = %#v", items[0].Summary)
	}

	if err := repo.UpdateSummary(ctx, "inv-summary", ""); err != nil {
		t.Fatalf("UpdateSummary clear: %v", err)
	}
	cleared, err := repo.Get(ctx, "inv-summary")
	if err != nil {
		t.Fatalf("Get after clear: %v", err)
	}
	if cleared.Summary != nil {
		t.Fatalf("expected cleared summary, got %#v", cleared.Summary)
	}
}

func TestInvocationRepo_AgentIDPersistedAndFilterable(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	if err := InitDB(db); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	repo := NewInvocationRepo(db)
	ctx := context.Background()

	// One row with an agent, one without — confirms NULL-friendly column.
	withAgent := invocation.Invocation{
		InvocationID: "inv-agent",
		Tool:         "t", Upstream: "u", Status: "received",
		Input:       []byte(`{}`),
		AgentID:     strPtr("agent-007"),
		SubmittedAt: time.Now().UTC(),
	}
	noAgent := invocation.Invocation{
		InvocationID: "inv-anon",
		Tool:         "t", Upstream: "u", Status: "received",
		Input:       []byte(`{}`),
		SubmittedAt: time.Now().UTC(),
	}
	if err := repo.Create(ctx, withAgent); err != nil {
		t.Fatalf("create withAgent: %v", err)
	}
	if err := repo.Create(ctx, noAgent); err != nil {
		t.Fatalf("create noAgent: %v", err)
	}

	got, err := repo.Get(ctx, "inv-agent")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.AgentID == nil || *got.AgentID != "agent-007" {
		t.Fatalf("agent_id round-trip failed: %+v", got.AgentID)
	}
	gotAnon, err := repo.Get(ctx, "inv-anon")
	if err != nil {
		t.Fatalf("Get anon: %v", err)
	}
	if gotAnon.AgentID != nil {
		t.Fatalf("expected nil agent_id, got %q", *gotAnon.AgentID)
	}

	// Filter by agent_id returns only the matching row.
	invs, total, err := repo.List(ctx, invocation.InvocationListFilter{AgentIDs: []string{"agent-007"}, Limit: 10})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 1 || len(invs) != 1 || invs[0].InvocationID != "inv-agent" {
		t.Fatalf("unexpected filter result: total=%d items=%+v", total, invs)
	}
}

func TestEventRepo_CRUD(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	if err := InitDB(db); err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	invRepo := NewInvocationRepo(db)
	evtRepo := NewEventRepo(db)
	ctx := context.Background()

	// Need an invocation first
	inv := invocation.Invocation{
		InvocationID: "inv-e1",
		Tool:         "test",
		Upstream:     "test",
		Status:       "pending",
		Input:        []byte(`{}`),
		SubmittedAt:  time.Now().UTC(),
	}
	if err := invRepo.Create(ctx, inv); err != nil {
		t.Fatalf("Create invocation: %v", err)
	}

	evt := invocation.Event{
		InvocationID: "inv-e1",
		EventType:    "approved",
		Payload:      []byte(`{"user": "admin"}`),
		CreatedAt:    time.Now().UTC(),
	}
	if err := evtRepo.Create(ctx, evt); err != nil {
		t.Fatalf("Create event: %v", err)
	}

	evts, total, err := evtRepo.ListByInvocation(ctx, "inv-e1", invocation.EventListFilter{Limit: 100})
	if err != nil {
		t.Fatalf("ListByInvocation: %v", err)
	}
	if total != 1 || len(evts) != 1 {
		t.Errorf("events: total=%d len=%d", total, len(evts))
	}
	if evts[0].EventType != "approved" {
		t.Errorf("event type: %s", evts[0].EventType)
	}
}

func TestServerRepo_CRUD(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	if err := InitDB(db); err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	repo := NewServerRepo(db)
	ctx := context.Background()

	upstream := mcp.Upstream{
		Name:    "test-server",
		Mode:    mcp.UpstreamModeHTTP,
		BaseURL: "http://localhost:8080",
		Timeout: 30 * time.Second,
		Enabled: true,
		Status: mcp.ServerStatus{
			AuthType:         mcp.AuthTypeNone,
			ConnectionStatus: mcp.ConnectionStatusReady,
			AuthStatus:       mcp.AuthStatusUnknown,
		},
	}

	// Create
	if err := repo.CreateServer(ctx, upstream); err != nil {
		t.Fatalf("CreateServer: %v", err)
	}

	// Count
	count, err := repo.CountServers(ctx)
	if err != nil {
		t.Fatalf("CountServers: %v", err)
	}
	if count != 1 {
		t.Errorf("count: %d", count)
	}

	// Get (enabled only)
	got, err := repo.GetServer(ctx, "test-server")
	if err != nil {
		t.Fatalf("GetServer: %v", err)
	}
	if got.Name != "test-server" || got.Mode != mcp.UpstreamModeHTTP {
		t.Errorf("GetServer mismatch: %+v", got)
	}
	if got.EndpointSlug != "test-server" {
		t.Errorf("endpoint slug: %s", got.EndpointSlug)
	}

	// GetServerAny
	got2, err := repo.GetServerAny(ctx, "test-server")
	if err != nil {
		t.Fatalf("GetServerAny: %v", err)
	}
	if got2.Name != "test-server" {
		t.Errorf("GetServerAny: %s", got2.Name)
	}

	gotBySlug, err := repo.GetServerByEndpointSlug(ctx, "test-server")
	if err != nil {
		t.Fatalf("GetServerByEndpointSlug: %v", err)
	}
	if gotBySlug.Name != "test-server" {
		t.Errorf("GetServerByEndpointSlug: %s", gotBySlug.Name)
	}

	// List
	servers, total, err := repo.ListServers(ctx, mcp.ServerFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListServers: %v", err)
	}
	if total != 1 || len(servers) != 1 {
		t.Errorf("ListServers: total=%d len=%d", total, len(servers))
	}

	// UpdateServerStatus
	newStatus := mcp.ServerStatus{
		AuthType:         mcp.AuthTypeHosted,
		ConnectionStatus: mcp.ConnectionStatusReady,
		AuthStatus:       mcp.AuthStatusReady,
		ReauthNeeded:     false,
		LastCheckOK:      true,
	}
	if err := repo.UpdateServerStatus(ctx, "test-server", newStatus); err != nil {
		t.Fatalf("UpdateServerStatus: %v", err)
	}

	got3, err := repo.GetServerAny(ctx, "test-server")
	if err != nil {
		t.Fatalf("Get after status update: %v", err)
	}
	if got3.Status.AuthType != mcp.AuthTypeHosted {
		t.Errorf("status auth_type: %s", got3.Status.AuthType)
	}

	// UpsertServer (update existing)
	upstream.BaseURL = "http://localhost:9090"
	if err := repo.UpsertServer(ctx, upstream); err != nil {
		t.Fatalf("UpsertServer: %v", err)
	}
	got4, err := repo.GetServerAny(ctx, "test-server")
	if err != nil {
		t.Fatalf("Get after upsert: %v", err)
	}
	if got4.BaseURL != "http://localhost:9090" {
		t.Errorf("upsert base_url: %s", got4.BaseURL)
	}

	// DisableServer
	if err := repo.DisableServer(ctx, "test-server"); err != nil {
		t.Fatalf("DisableServer: %v", err)
	}

	// GetServer should fail now (only enabled)
	_, err = repo.GetServer(ctx, "test-server")
	if err == nil {
		t.Error("expected error for disabled server")
	}

	// DeleteServer
	if err := repo.DeleteServer(ctx, "test-server"); err != nil {
		t.Fatalf("DeleteServer: %v", err)
	}
	count, _ = repo.CountServers(ctx)
	if count != 0 {
		t.Errorf("count after delete: %d", count)
	}
}

func TestServerRepoEndpointSlugIsUnique(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	if err := InitDB(db); err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	repo := NewServerRepo(db)
	ctx := context.Background()
	base := mcp.Upstream{
		Name:    "Slack Local",
		Mode:    mcp.UpstreamModeHTTP,
		BaseURL: "http://localhost:8080",
		Timeout: 30 * time.Second,
		Enabled: true,
	}
	if err := repo.CreateServer(ctx, base); err != nil {
		t.Fatalf("CreateServer first: %v", err)
	}
	duplicate := base
	duplicate.Name = "slack-local"
	if err := repo.CreateServer(ctx, duplicate); err == nil {
		t.Fatalf("expected duplicate endpoint slug error")
	}
}

func TestServerRepoUpsertKeepsEndpointSlugImmutable(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	if err := InitDB(db); err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	repo := NewServerRepo(db)
	ctx := context.Background()
	upstream := mcp.Upstream{
		Name:         "Slack Local",
		EndpointSlug: "slack-local-2",
		Mode:         mcp.UpstreamModeHTTP,
		BaseURL:      "http://localhost:8080",
		Timeout:      30 * time.Second,
		Enabled:      true,
	}
	if err := repo.UpsertServer(ctx, upstream); err != nil {
		t.Fatalf("UpsertServer insert: %v", err)
	}
	upstream.BaseURL = "http://localhost:9090"
	upstream.EndpointSlug = "slack-local"
	if err := repo.UpsertServer(ctx, upstream); err != nil {
		t.Fatalf("UpsertServer update: %v", err)
	}
	got, err := repo.GetServerAny(ctx, "Slack Local")
	if err != nil {
		t.Fatalf("GetServerAny: %v", err)
	}
	if got.EndpointSlug != "slack-local-2" {
		t.Fatalf("endpoint slug = %q, want immutable slack-local-2", got.EndpointSlug)
	}
	if got.BaseURL != "http://localhost:9090" {
		t.Fatalf("base_url = %q, want updated URL", got.BaseURL)
	}
}

func TestInitDBBackfillsEndpointSlugCollisionsDeterministically(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	if _, err := db.Exec(`
		CREATE TABLE schema_migrations (
			version INTEGER PRIMARY KEY,
			name    TEXT NOT NULL,
			applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
		CREATE TABLE mcp_servers (
			name TEXT PRIMARY KEY
		);
		INSERT INTO mcp_servers(name) VALUES ('Slack Local'), ('slack-local'), ('!!!');
	`); err != nil {
		t.Fatalf("seed old schema: %v", err)
	}
	// Isolate migration 024 (server endpoint_slug backfill): mark every other
	// migration as already applied so InitDB runs only 024 against this minimal
	// seed. Later migrations (025 external_sessions + expires_at) touch tables
	// this fixture never creates, so they must stay marked-applied here.
	for _, m := range migrations {
		// Leave only migration 024 pending — it's the one under test; the
		// seeded schema is too minimal for later migrations to apply.
		if m.Version == 24 {
			continue
		}
		if _, err := db.Exec(`INSERT INTO schema_migrations(version, name) VALUES (?, ?)`, m.Version, m.Name); err != nil {
			t.Fatalf("seed migration %d: %v", m.Version, err)
		}
	}

	if err := InitDB(db); err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	got := map[string]string{}
	rows, err := db.Query(`SELECT name, endpoint_slug FROM mcp_servers`)
	if err != nil {
		t.Fatalf("query slugs: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name, slug string
		if err := rows.Scan(&name, &slug); err != nil {
			t.Fatalf("scan slug: %v", err)
		}
		got[name] = slug
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}

	want := map[string]string{
		"!!!":         "server",
		"Slack Local": "slack-local",
		"slack-local": "slack-local-2",
	}
	for name, wantSlug := range want {
		if got[name] != wantSlug {
			t.Fatalf("slug for %q = %q, want %q (all slugs: %+v)", name, got[name], wantSlug, got)
		}
	}
}

func TestServerRepo_UpsertNew(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	if err := InitDB(db); err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	repo := NewServerRepo(db)
	ctx := context.Background()

	upstream := mcp.Upstream{
		Name:    "new-via-upsert",
		Mode:    mcp.UpstreamModeStdio,
		Command: "/usr/bin/test-mcp",
		Args:    []string{"--flag"},
		Timeout: 15 * time.Second,
		Enabled: true,
		Status: mcp.ServerStatus{
			AuthType:         mcp.AuthTypeNone,
			ConnectionStatus: mcp.ConnectionStatusUnknown,
			AuthStatus:       mcp.AuthStatusUnknown,
		},
	}

	if err := repo.UpsertServer(ctx, upstream); err != nil {
		t.Fatalf("UpsertServer new: %v", err)
	}

	got, err := repo.GetServerAny(ctx, "new-via-upsert")
	if err != nil {
		t.Fatalf("Get after upsert new: %v", err)
	}
	if got.Name != "new-via-upsert" || got.Command != "/usr/bin/test-mcp" {
		t.Errorf("upsert new mismatch: %+v", got)
	}
	if len(got.Args) != 1 || got.Args[0] != "--flag" {
		t.Errorf("args: %+v", got.Args)
	}
}

func TestServerRepo_OAuthFields(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	if err := InitDB(db); err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	repo := NewServerRepo(db)
	ctx := context.Background()

	upstream := mcp.Upstream{
		Name:               "oauth-server",
		Mode:               mcp.UpstreamModeHTTP,
		BaseURL:            "http://localhost:8080",
		Timeout:            30 * time.Second,
		Enabled:            true,
		OAuthProviderID:    "github",
		OAuthProviderLabel: "GitHub",
		OAuthAuthorizeURL:  "https://github.com/login/oauth/authorize",
		OAuthTokenURL:      "https://github.com/login/oauth/access_token",
		OAuthClientID:      "client-123",
		OAuthClientSecret:  "secret-456",
		OAuthScopes:        "repo,read:org",
		Status: mcp.ServerStatus{
			AuthType:         mcp.AuthTypeHosted,
			ConnectionStatus: mcp.ConnectionStatusReady,
			AuthStatus:       mcp.AuthStatusReady,
		},
	}

	if err := repo.UpsertServer(ctx, upstream); err != nil {
		t.Fatalf("UpsertServer: %v", err)
	}

	got, err := repo.GetServerAny(ctx, "oauth-server")
	if err != nil {
		t.Fatalf("GetServerAny: %v", err)
	}

	if got.OAuthProviderID != "github" {
		t.Errorf("provider_id: %s", got.OAuthProviderID)
	}
	if got.OAuthClientID != "client-123" {
		t.Errorf("client_id: %s", got.OAuthClientID)
	}
	if got.OAuthScopes != "repo,read:org" {
		t.Errorf("scopes: %s", got.OAuthScopes)
	}
}

func TestOAuthRepo_CredentialCRUD(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	if err := InitDB(db); err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	serverRepo := NewServerRepo(db)
	oauthRepo := NewOAuthRepo(db)
	ctx := context.Background()

	// Need a server first (FK constraint)
	if err := serverRepo.CreateServer(ctx, mcp.Upstream{
		Name:    "oauth-srv",
		Mode:    mcp.UpstreamModeHTTP,
		BaseURL: "http://localhost",
		Timeout: 30 * time.Second,
		Enabled: true,
		Status: mcp.ServerStatus{
			AuthType:         mcp.AuthTypeHosted,
			ConnectionStatus: mcp.ConnectionStatusUnknown,
			AuthStatus:       mcp.AuthStatusUnknown,
		},
	}); err != nil {
		t.Fatalf("CreateServer: %v", err)
	}

	expires := time.Now().Add(1 * time.Hour).UTC()
	cred := OAuthCredential{
		ServerName:  "oauth-srv",
		AccessToken: "access-123",
		ExpiresAt:   &expires,
	}

	// Upsert
	if err := oauthRepo.UpsertCredential(ctx, cred); err != nil {
		t.Fatalf("UpsertCredential: %v", err)
	}

	// Get
	got, err := oauthRepo.GetCredential(ctx, "oauth-srv")
	if err != nil {
		t.Fatalf("GetCredential: %v", err)
	}
	if got.AccessToken != "access-123" {
		t.Errorf("access_token: %s", got.AccessToken)
	}
	if got.ExpiresAt == nil {
		t.Error("expires_at should not be nil")
	}

	// Upsert (update)
	cred.AccessToken = "access-456"
	if err := oauthRepo.UpsertCredential(ctx, cred); err != nil {
		t.Fatalf("UpsertCredential update: %v", err)
	}
	got2, err := oauthRepo.GetCredential(ctx, "oauth-srv")
	if err != nil {
		t.Fatalf("GetCredential after update: %v", err)
	}
	if got2.AccessToken != "access-456" {
		t.Errorf("updated access_token: %s", got2.AccessToken)
	}

	// Delete
	if err := oauthRepo.DeleteCredential(ctx, "oauth-srv"); err != nil {
		t.Fatalf("DeleteCredential: %v", err)
	}
	_, err = oauthRepo.GetCredential(ctx, "oauth-srv")
	if err == nil {
		t.Error("expected error after delete")
	}
}

func TestRefreshingOAuthCredentialStoreRefreshesExpiredCredential(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	if err := InitDB(db); err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	var form url.Values
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		form = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"access-new","token_type":"Bearer","scope":"openid","expires_in":3600}`))
	}))
	defer tokenServer.Close()

	serverRepo := NewServerRepo(db)
	oauthRepo := NewOAuthRepo(db)
	ctx := context.Background()
	upstream := mcp.Upstream{
		Name:              "oauth-refresh-srv",
		Mode:              mcp.UpstreamModeHTTP,
		BaseURL:           "http://localhost",
		OAuthTokenURL:     tokenServer.URL,
		OAuthClientID:     "client-id",
		OAuthClientSecret: "client-secret",
		Timeout:           30 * time.Second,
		Enabled:           true,
		Status: mcp.ServerStatus{
			AuthType:         mcp.AuthTypeHosted,
			ConnectionStatus: mcp.ConnectionStatusUnknown,
			AuthStatus:       mcp.AuthStatusUnknown,
		},
	}
	if err := serverRepo.CreateServer(ctx, upstream); err != nil {
		t.Fatalf("CreateServer: %v", err)
	}
	expired := time.Now().Add(-1 * time.Minute).UTC()
	if err := oauthRepo.UpsertCredential(ctx, OAuthCredential{
		ServerName:   upstream.Name,
		AccessToken:  "access-old",
		RefreshToken: "refresh-old",
		TokenType:    "Bearer",
		Scope:        "openid",
		ExpiresAt:    &expired,
	}); err != nil {
		t.Fatalf("UpsertCredential: %v", err)
	}

	client := mcp.NewHTTPClient()
	store := NewRefreshingOAuthCredentialStoreWithSkew(oauthRepo, client, 0)
	view, err := store.GetCredential(ctx, upstream)
	if err != nil {
		t.Fatalf("GetCredential: %v", err)
	}
	if view.AccessToken != "access-new" {
		t.Fatalf("access token = %q, want access-new", view.AccessToken)
	}
	for key, value := range map[string]string{
		"grant_type":    "refresh_token",
		"refresh_token": "refresh-old",
		"client_id":     "client-id",
		"client_secret": "client-secret",
	} {
		if got := form.Get(key); got != value {
			t.Fatalf("form[%s] = %q, want %q; form=%v", key, got, value, form)
		}
	}

	got, err := oauthRepo.GetCredential(ctx, upstream.Name)
	if err != nil {
		t.Fatalf("GetCredential after refresh: %v", err)
	}
	if got.AccessToken != "access-new" {
		t.Fatalf("stored access token = %q, want access-new", got.AccessToken)
	}
	if got.RefreshToken != "refresh-old" {
		t.Fatalf("stored refresh token = %q, want preserved refresh-old", got.RefreshToken)
	}
	if got.ExpiresAt == nil || !got.ExpiresAt.After(time.Now().UTC()) {
		t.Fatalf("stored expires_at was not updated: %#v", got.ExpiresAt)
	}
}

func TestOAuthRepo_ConnectSessionCRUD(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	if err := InitDB(db); err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	serverRepo := NewServerRepo(db)
	oauthRepo := NewOAuthRepo(db)
	ctx := context.Background()

	// Need a server first
	if err := serverRepo.CreateServer(ctx, mcp.Upstream{
		Name:    "session-srv",
		Mode:    mcp.UpstreamModeHTTP,
		BaseURL: "http://localhost",
		Timeout: 30 * time.Second,
		Enabled: true,
		Status: mcp.ServerStatus{
			AuthType:         mcp.AuthTypeHosted,
			ConnectionStatus: mcp.ConnectionStatusUnknown,
			AuthStatus:       mcp.AuthStatusUnknown,
		},
	}); err != nil {
		t.Fatalf("CreateServer: %v", err)
	}

	session := OAuthConnectSession{
		State:        "state-abc",
		ServerName:   "session-srv",
		Status:       "pending",
		CodeVerifier: "verifier-123",
		RedirectURI:  "http://localhost/callback",
		StartedAt:    time.Now().UTC(),
	}

	// Upsert
	if err := oauthRepo.UpsertConnectSession(ctx, session); err != nil {
		t.Fatalf("UpsertConnectSession: %v", err)
	}

	// Get
	got, err := oauthRepo.GetConnectSession(ctx, "state-abc")
	if err != nil {
		t.Fatalf("GetConnectSession: %v", err)
	}
	if got.ServerName != "session-srv" || got.Status != "pending" {
		t.Errorf("session mismatch: %+v", got)
	}
	if got.CodeVerifier != "verifier-123" {
		t.Errorf("code_verifier: %s", got.CodeVerifier)
	}

	// Update status
	errMsg := "user denied"
	session.Status = "failed"
	session.ErrorMessage = &errMsg
	if err := oauthRepo.UpsertConnectSession(ctx, session); err != nil {
		t.Fatalf("UpsertConnectSession update: %v", err)
	}
	got2, err := oauthRepo.GetConnectSession(ctx, "state-abc")
	if err != nil {
		t.Fatalf("GetConnectSession after update: %v", err)
	}
	if got2.Status != "failed" {
		t.Errorf("updated status: %s", got2.Status)
	}

	// GetLatestConnectSessionByServer
	got3, err := oauthRepo.GetLatestConnectSessionByServer(ctx, "session-srv")
	if err != nil {
		t.Fatalf("GetLatestConnectSessionByServer: %v", err)
	}
	if got3.State != "state-abc" {
		t.Errorf("latest session state: %s", got3.State)
	}

	// Add another session and verify latest
	session2 := OAuthConnectSession{
		State:        "state-def",
		ServerName:   "session-srv",
		Status:       "completed",
		CodeVerifier: "verifier-456",
		RedirectURI:  "http://localhost/callback",
		StartedAt:    time.Now().Add(1 * time.Minute).UTC(),
	}
	if err := oauthRepo.UpsertConnectSession(ctx, session2); err != nil {
		t.Fatalf("UpsertConnectSession second: %v", err)
	}
	got4, err := oauthRepo.GetLatestConnectSessionByServer(ctx, "session-srv")
	if err != nil {
		t.Fatalf("GetLatestConnectSessionByServer second: %v", err)
	}
	if got4.State != "state-def" {
		t.Errorf("latest should be state-def, got %s", got4.State)
	}
}

func TestServerRepo_DeleteNoRows(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	if err := InitDB(db); err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	repo := NewServerRepo(db)
	ctx := context.Background()

	err := repo.DeleteServer(ctx, "nonexistent")
	if err != sql.ErrNoRows {
		t.Errorf("expected sql.ErrNoRows, got: %v", err)
	}
}

func strPtr(s string) *string { return &s }

func TestAgentSyncSettingsRepo_UpsertOnEmptyTable(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	if err := InitDB(db); err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	// Delete the seed row if it exists
	db.Exec(`DELETE FROM agent_sync_settings`)

	repo := NewAgentSyncSettingsRepo(db)
	ctx := context.Background()

	// Get on empty table should return empty settings, not an error
	s, err := repo.Get(ctx)
	if err != nil {
		t.Fatalf("Get on empty table: %v", err)
	}
	if s.OrgCUID != "" {
		t.Fatalf("expected empty OrgCUID, got %q", s.OrgCUID)
	}

	// Save should create the row
	err = repo.Save(ctx, AgentSyncSettings{
		OrgCUID:                "org-abc",
		AgentRecordTypeSlug:    "ai-agents",
		CharterFieldKey:        "charter",
		SummaryModelConfigCUID: "model-abc",
		DefaultAgentVMCUID:     "agent-vm-abc",
	})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Get should now return the saved values
	s, err = repo.Get(ctx)
	if err != nil {
		t.Fatalf("Get after Save: %v", err)
	}
	if s.OrgCUID != "org-abc" {
		t.Fatalf("expected OrgCUID=org-abc, got %q", s.OrgCUID)
	}
	if s.AgentRecordTypeSlug != "ai-agents" {
		t.Fatalf("expected AgentRecordTypeSlug=ai-agents, got %q", s.AgentRecordTypeSlug)
	}
	if s.SummaryModelConfigCUID != "model-abc" {
		t.Fatalf("expected SummaryModelConfigCUID=model-abc, got %q", s.SummaryModelConfigCUID)
	}
	if s.DefaultAgentVMCUID != "agent-vm-abc" {
		t.Fatalf("expected DefaultAgentVMCUID=agent-vm-abc, got %q", s.DefaultAgentVMCUID)
	}
}

// TestInitDBToleratesRenumberedMigrationStamps pins the failure mode a
// rebase actually produced: main's 024_server_endpoint_slug landed ahead of
// this branch's session migrations, shifting them from 024/025 to 025/026.
// A long-lived dev DB stamped under the old numbering re-runs whichever
// migrations now occupy versions >= 24 under the new numbering, even though
// their ADD COLUMN steps already executed once under the old version
// numbers. Simulate that by wiping the schema_migrations rows for versions
// >= 24 (as if the DB had been stamped by an earlier numbering) and
// re-running InitDB: it must succeed, not fail with a "column already
// exists" style error, and the schema must remain intact.
func TestInitDBToleratesRenumberedMigrationStamps(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	if err := InitDB(db); err != nil {
		t.Fatalf("InitDB (first run): %v", err)
	}

	if _, err := db.Exec(`DELETE FROM schema_migrations WHERE version >= 24`); err != nil {
		t.Fatalf("simulate stale stamps: %v", err)
	}

	if err := InitDB(db); err != nil {
		t.Fatalf("InitDB (re-run against renumbered stamps): %v", err)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if count != 27 {
		t.Fatalf("expected 27 migrations recorded, got %d", count)
	}

	// The columns/tables the renumbered migrations add must still be
	// present and singular, not duplicated or missing.
	for _, table := range []string{"mcp_servers", "invocations", "external_sessions"} {
		if !sqliteTableHasColumn(t, db, table, tableColumnUnderTest[table]) {
			t.Fatalf("table %s missing column %s after renumbered re-run", table, tableColumnUnderTest[table])
		}
	}

	var indexCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_mcp_servers_endpoint_slug'`).Scan(&indexCount); err != nil {
		t.Fatalf("count index: %v", err)
	}
	if indexCount != 1 {
		t.Fatalf("expected exactly one idx_mcp_servers_endpoint_slug index, got %d", indexCount)
	}
}

var tableColumnUnderTest = map[string]string{
	"mcp_servers":       "endpoint_slug",
	"invocations":       "session_id",
	"external_sessions": "expires_at",
}

func sqliteTableHasColumn(t *testing.T, db *sql.DB, table, column string) bool {
	t.Helper()
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		t.Fatalf("PRAGMA table_info(%s): %v", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid        int
			name       string
			colType    string
			notNull    int
			defaultVal sql.NullString
			pk         int
		)
		if err := rows.Scan(&cid, &name, &colType, &notNull, &defaultVal, &pk); err != nil {
			t.Fatalf("scan table_info(%s): %v", table, err)
		}
		if name == column {
			return true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err table_info(%s): %v", table, err)
	}
	return false
}
