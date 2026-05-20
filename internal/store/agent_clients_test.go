package store_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"atryum/internal/store"

	_ "modernc.org/sqlite"
)

func TestAgentClientRepo_UpsertAndLookup(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := store.InitDB(db); err != nil {
		t.Fatal(err)
	}
	repo := store.NewAgentClientRepo(db)
	ctx := context.Background()

	// Initial insert.
	if err := repo.Upsert(ctx, store.AgentClient{
		AgentID:       "agent-007",
		ClientName:    "amp",
		ClientVersion: "0.0.1234",
		Subject:       "user-42",
		Issuer:        "https://idp.test",
		LastSeenAt:    time.Now().UTC(),
	}); err != nil {
		t.Fatalf("upsert insert: %v", err)
	}

	got, err := repo.Get(ctx, "agent-007")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ClientName != "amp" || got.ClientVersion != "0.0.1234" || got.Subject != "user-42" || got.Issuer != "https://idp.test" {
		t.Fatalf("get returned %+v", got)
	}

	// Partial upsert: a later initialize without clientInfo must not wipe
	// previously captured name/version.
	if err := repo.Upsert(ctx, store.AgentClient{
		AgentID:    "agent-007",
		Subject:    "user-42",
		Issuer:     "https://idp.test",
		LastSeenAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("upsert update: %v", err)
	}
	got, err = repo.Get(ctx, "agent-007")
	if err != nil {
		t.Fatalf("get after partial upsert: %v", err)
	}
	if got.ClientName != "amp" || got.ClientVersion != "0.0.1234" {
		t.Fatalf("partial upsert clobbered clientInfo: %+v", got)
	}

	// Lookup adapter used by invocation.Service should return the same data.
	info, ok := repo.LookupAgentClient(ctx, "agent-007")
	if !ok {
		t.Fatal("LookupAgentClient missed known agent")
	}
	if info.ClientName != "amp" || info.ClientVersion != "0.0.1234" || info.Subject != "user-42" {
		t.Fatalf("LookupAgentClient = %+v", info)
	}

	// Unknown agents must yield (zero, false), not an error.
	if _, ok := repo.LookupAgentClient(ctx, "nobody"); ok {
		t.Fatal("LookupAgentClient returned true for unknown agent")
	}
}
