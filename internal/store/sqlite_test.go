package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"atryum/internal/invocation"
	"atryum/internal/mcp"

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
	if count != 3 {
		t.Fatalf("expected 3 migrations, got %d", count)
	}

	// Verify all tables exist
	tables := []string{"invocations", "invocation_events", "mcp_servers", "oauth_credentials", "oauth_connect_sessions"}
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
	if count != 3 {
		t.Fatalf("expected 3 migrations after double init, got %d", count)
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

	// GetServerAny
	got2, err := repo.GetServerAny(ctx, "test-server")
	if err != nil {
		t.Fatalf("GetServerAny: %v", err)
	}
	if got2.Name != "test-server" {
		t.Errorf("GetServerAny: %s", got2.Name)
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
