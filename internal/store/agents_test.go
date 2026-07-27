package store

import (
	"context"
	"reflect"
	"testing"
)

func TestAgentsRepoUpsertPreservesTagsAcrossResync(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	if err := InitDB(db); err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	repo := NewAgentsRepo(db)
	ctx := context.Background()

	// First sync: a brand-new agent, no tags yet.
	if err := repo.Upsert(ctx, AgentRecord{
		ID:                 "agent-1",
		VMOrganizationCUID: "org-1",
		VMOrganizationName: "Org One",
		VMCUID:             "vm-cuid-1",
		VMName:             "Agent One",
	}); err != nil {
		t.Fatalf("first Upsert: %v", err)
	}

	rec, err := repo.Get(ctx, "agent-1")
	if err != nil {
		t.Fatalf("Get after first upsert: %v", err)
	}
	if len(rec.Tags) != 0 {
		t.Fatalf("expected no tags on fresh sync, got %v", rec.Tags)
	}

	if err := repo.UpdateTags(ctx, "agent-1", []string{"billing", "prod"}); err != nil {
		t.Fatalf("UpdateTags: %v", err)
	}

	// Re-sync (e.g. the next periodic VM sync): same vm_cuid, refreshed name.
	if err := repo.Upsert(ctx, AgentRecord{
		ID:                 "agent-1",
		VMOrganizationCUID: "org-1",
		VMOrganizationName: "Org One",
		VMCUID:             "vm-cuid-1",
		VMName:             "Agent One (renamed)",
	}); err != nil {
		t.Fatalf("second Upsert (resync): %v", err)
	}

	rec, err = repo.Get(ctx, "agent-1")
	if err != nil {
		t.Fatalf("Get after resync: %v", err)
	}
	if rec.VMName != "Agent One (renamed)" {
		t.Fatalf("expected resync to update vm_name, got %q", rec.VMName)
	}
	want := []string{"billing", "prod"}
	if !reflect.DeepEqual(rec.Tags, want) {
		t.Fatalf("tags not preserved across resync: got %v, want %v", rec.Tags, want)
	}
}

func TestAgentsRepoUpsertNewAgentDefaultsToEmptyTags(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	if err := InitDB(db); err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	repo := NewAgentsRepo(db)
	ctx := context.Background()

	if err := repo.Upsert(ctx, AgentRecord{
		ID:                 "agent-2",
		VMOrganizationCUID: "org-1",
		VMCUID:             "vm-cuid-2",
		VMName:             "Agent Two",
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	rec, err := repo.Get(ctx, "agent-2")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(rec.Tags) != 0 {
		t.Fatalf("expected empty tags on a fresh insert, got %v", rec.Tags)
	}
}
