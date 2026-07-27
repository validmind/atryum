package api

import (
	"context"
	"database/sql"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/validmind/atryum/internal/config"
	"github.com/validmind/atryum/internal/invocation"
	"github.com/validmind/atryum/internal/invocation/policy"
	"github.com/validmind/atryum/internal/mcp"
	"github.com/validmind/atryum/internal/store"
)

// pollUntil calls cond repeatedly, sleeping interval between tries, until it
// returns true or timeout elapses. Shared by every test in this package that
// needs to wait for asynchronous state (a port accepting connections, a row
// reaching a given status) instead of sleeping a fixed guess.
func pollUntil(t *testing.T, timeout, interval time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(interval)
	}
	return cond()
}

// newTestAgentServer builds the db -> resolver -> invocation.Service ->
// Handler -> httptest.Server stack backing a single HTTP upstream, and
// registers cleanup for the db and the server. timeoutSeconds drives both
// the upstream's own request timeout and the service's default timeout, as
// every existing call site already did identically. enableStreaming
// installs the same stream options and audit limits every existing
// streaming e2e test used; pass false for tests that don't exercise
// streaming at all.
func newTestAgentServer(t *testing.T, upstreamName, upstreamURL string, timeoutSeconds int, enableStreaming bool) (*httptest.Server, *invocation.Service) {
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
	resolver := mcp.NewResolver(serverRepo, config.Config{
		Upstreams: []config.UpstreamConfig{{Name: upstreamName, Mode: "http", BaseURL: upstreamURL, Enabled: true, TimeoutSeconds: timeoutSeconds}},
	})
	if err := resolver.BootstrapIfEmpty(context.Background()); err != nil {
		t.Fatal(err)
	}
	svc := invocation.NewService(
		store.NewInvocationRepo(db), store.NewEventRepo(db), resolver, mcp.NewHTTPClient(),
		policy.AlwaysApproveProvider{}, time.Duration(timeoutSeconds)*time.Second, nil, nil, nil, nil,
	)
	if enableStreaming {
		svc.SetStreamOptions(
			mcp.StreamOptions{HeaderTimeout: 10 * time.Second, IdleTimeout: 10 * time.Second, MaxDuration: 60 * time.Second},
			invocation.StreamAuditLimits{MaxEvents: 100, MaxEventBytes: 4096},
		)
	}

	h := NewHandler(svc, stubServerService{}, nil, nil, nil, nil, nil, nil, nil, nil)
	agentServer := httptest.NewServer(h.Routes())
	t.Cleanup(agentServer.Close)

	return agentServer, svc
}
