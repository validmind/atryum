package store

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/validmind/atryum/internal/config"
	"github.com/validmind/atryum/internal/mcp"
)

// Replicas starting together on an empty database each bootstrap the
// configured [[upstreams]]: one creates them, the rest find them there and
// start without an error, whatever the database's default isolation level.
func TestPostgresBootstrapReplicasStartTogether(t *testing.T) {
	if os.Getenv("ATRYUM_POSTGRES_TESTS") != "1" {
		t.Skip("set ATRYUM_POSTGRES_TESTS=1 to run against local PostgreSQL")
	}
	dsn := os.Getenv("ATRYUM_POSTGRES_TEST_DSN")
	if dsn == "" {
		dsn = localPostgresTestDSN
	}
	for _, isolation := range []string{"", "repeatable read", "serializable"} {
		name := isolation
		if name == "" {
			name = "server default"
		}
		t.Run(name, func(t *testing.T) {
			dsn := dsn
			if isolation != "" {
				dsn = withDSNParam(dsn, "default_transaction_isolation", url.QueryEscape(isolation))
			}
			testBootstrapReplicasStartTogether(t, dsn)
		})
	}
}

func withDSNParam(dsn, key, value string) string {
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	return dsn + sep + key + "=" + value
}

func testBootstrapReplicasStartTogether(t *testing.T, dsn string) {
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close() })
	schema := fmt.Sprintf("atryum_bootstrap_%d", time.Now().UnixNano())
	if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`) })
	dsn = withDSNParam(dsn, "search_path", schema)

	open := func() *sql.DB {
		db, err := OpenPostgres(dsn)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		return db
	}
	if err := InitDBWithDialect(open(), DialectPostgres); err != nil {
		t.Fatal(err)
	}

	cfg := config.Config{Upstreams: []config.UpstreamConfig{
		{Name: "payments", Mode: "http", BaseURL: "https://payments.example/mcp", Enabled: true, TimeoutSeconds: 5},
		{Name: "ledger", Mode: "http", BaseURL: "https://ledger.example/mcp", Enabled: true, TimeoutSeconds: 5},
		{Name: "crm", Mode: "http", BaseURL: "https://crm.example/mcp", Enabled: true, TimeoutSeconds: 5},
	}}
	const replicas = 10
	resolvers := make([]*mcp.Resolver, replicas)
	for i := range resolvers {
		db := open()
		if err := db.Ping(); err != nil { // connect before the start line
			t.Fatal(err)
		}
		resolvers[i] = mcp.NewResolver(NewServerRepoWithDialect(db, DialectPostgres), cfg)
	}

	start := make(chan struct{})
	errs := make([]error, replicas)
	var wg sync.WaitGroup
	for i, r := range resolvers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs[i] = r.BootstrapIfEmpty(context.Background())
		}()
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("replica %d: %v", i, err)
		}
	}

	servers, total, err := NewServerRepoWithDialect(open(), DialectPostgres).ListServers(context.Background(), mcp.ServerFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if total != len(cfg.Upstreams) {
		t.Fatalf("%d servers after bootstrap, want %d: %v", total, len(cfg.Upstreams), servers)
	}
}
