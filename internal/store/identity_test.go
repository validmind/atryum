package store

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

func seedAgent(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	repo := NewAgentsRepo(db)
	if err := repo.Create(context.Background(), AgentRecord{ID: id, VMCUID: id, VMName: "Agent " + id, Enabled: true}); err != nil {
		t.Fatalf("seed agent %s: %v", id, err)
	}
}

func TestUsersRepoUpsertLoginIsIdempotentOnIssuerSubject(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	if err := InitDB(db); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	ctx := context.Background()
	users := NewUsersRepo(db)

	first, err := users.UpsertLogin(ctx, "https://idp.example/", "sub-1", "a@example.com", "Alice", UserRoleMember)
	if err != nil {
		t.Fatalf("first UpsertLogin: %v", err)
	}
	if first.Role != UserRoleMember || first.Issuer != "https://idp.example" {
		t.Fatalf("unexpected first user: %+v", first)
	}
	// Same (issuer, subject) with a changed email and promoted role: same row.
	second, err := users.UpsertLogin(ctx, "https://idp.example", "sub-1", "alice@example.com", "Alice A", UserRoleAdmin)
	if err != nil {
		t.Fatalf("second UpsertLogin: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("expected same user id, got %s vs %s", first.ID, second.ID)
	}
	if second.Email != "alice@example.com" || second.Role != UserRoleAdmin {
		t.Fatalf("expected refreshed email/role, got %+v", second)
	}
	// Different subject is a different user even with the same email.
	third, err := users.UpsertLogin(ctx, "https://idp.example", "sub-2", "alice@example.com", "Other", UserRoleMember)
	if err != nil {
		t.Fatalf("third UpsertLogin: %v", err)
	}
	if third.ID == first.ID {
		t.Fatalf("expected distinct user for distinct subject")
	}
	all, err := users.List(ctx)
	if err != nil || len(all) != 2 {
		t.Fatalf("List: %v (n=%d)", err, len(all))
	}
}

func TestAPIKeysResolveActiveHonoursRevokeExpiryAndDisabledCreator(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	if err := InitDB(db); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	ctx := context.Background()
	now := time.Now().UTC()
	users := NewUsersRepo(db)
	keys := NewAPIKeysRepo(db)
	seedAgent(t, db, "agent-1")

	alice, err := users.UpsertLogin(ctx, "https://idp.example", "sub-1", "a@example.com", "Alice", UserRoleMember)
	if err != nil {
		t.Fatalf("UpsertLogin: %v", err)
	}

	live, err := keys.Create(ctx, APIKey{AgentID: "agent-1", CreatedBy: alice.ID, Name: "laptop", KeyHash: "hash-live", KeyPrefix: "atr_live"})
	if err != nil {
		t.Fatalf("Create live: %v", err)
	}
	past := now.Add(-time.Hour)
	if _, err := keys.Create(ctx, APIKey{AgentID: "agent-1", CreatedBy: alice.ID, Name: "old", KeyHash: "hash-expired", KeyPrefix: "atr_old", ExpiresAt: &past}); err != nil {
		t.Fatalf("Create expired: %v", err)
	}
	// A key with no creator (no-auth deployment) must still resolve.
	if _, err := keys.Create(ctx, APIKey{AgentID: "agent-1", Name: "anon", KeyHash: "hash-anon", KeyPrefix: "atr_anon"}); err != nil {
		t.Fatalf("Create anon: %v", err)
	}

	got, err := keys.ResolveActive(ctx, "hash-live", now)
	if err != nil {
		t.Fatalf("ResolveActive live: %v", err)
	}
	if got.ID != live.ID || got.AgentID != "agent-1" || got.CreatedBy != alice.ID {
		t.Fatalf("unexpected resolved key: %+v", got)
	}
	if _, err := keys.ResolveActive(ctx, "hash-expired", now); err != sql.ErrNoRows {
		t.Fatalf("expected expired key to miss, got %v", err)
	}
	if _, err := keys.ResolveActive(ctx, "hash-anon", now); err != nil {
		t.Fatalf("expected creator-less key to resolve, got %v", err)
	}
	if _, err := keys.ResolveActive(ctx, "hash-unknown", now); err != sql.ErrNoRows {
		t.Fatalf("expected unknown key to miss, got %v", err)
	}

	// Revoke: stops resolving, row remains for audit.
	if err := keys.Revoke(ctx, live.ID, alice.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if _, err := keys.ResolveActive(ctx, "hash-live", now); err != sql.ErrNoRows {
		t.Fatalf("expected revoked key to miss, got %v", err)
	}
	revoked, err := keys.Get(ctx, live.ID)
	if err != nil || revoked.RevokedAt == nil || revoked.RevokedBy != alice.ID {
		t.Fatalf("expected revoked row with audit fields, got %+v err=%v", revoked, err)
	}
	if err := keys.Revoke(ctx, "nope", alice.ID); err != sql.ErrNoRows {
		t.Fatalf("expected ErrNoRows revoking unknown key, got %v", err)
	}

	// Disabled creator: keys stop resolving even without explicit revocation.
	fresh, err := keys.Create(ctx, APIKey{AgentID: "agent-1", CreatedBy: alice.ID, Name: "fresh", KeyHash: "hash-fresh", KeyPrefix: "atr_fresh"})
	if err != nil {
		t.Fatalf("Create fresh: %v", err)
	}
	if err := users.SetDisabled(ctx, alice.ID, true); err != nil {
		t.Fatalf("SetDisabled: %v", err)
	}
	if _, err := keys.ResolveActive(ctx, "hash-fresh", now); err != sql.ErrNoRows {
		t.Fatalf("expected disabled creator's key to miss, got %v", err)
	}
	// Cascade revoke for audit: only the still-active key is affected.
	n, err := keys.RevokeByCreator(ctx, alice.ID, "admin-1")
	if err != nil || n != 2 { // fresh + expired (expired was never revoked)
		t.Fatalf("RevokeByCreator: n=%d err=%v", n, err)
	}
	if k, _ := keys.Get(ctx, fresh.ID); k.RevokedAt == nil || k.RevokedBy != "admin-1" {
		t.Fatalf("expected cascade revoke on fresh key, got %+v", k)
	}
	// Re-enable: keys stay revoked (revocation is durable).
	if err := users.SetDisabled(ctx, alice.ID, false); err != nil {
		t.Fatalf("SetDisabled(false): %v", err)
	}
	if _, err := keys.ResolveActive(ctx, "hash-fresh", now); err != sql.ErrNoRows {
		t.Fatalf("expected revoked key to stay revoked after re-enable, got %v", err)
	}
}

func TestAgentMembersRepoAndKeyCascadeOnRemoval(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	if err := InitDB(db); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	ctx := context.Background()
	users := NewUsersRepo(db)
	members := NewAgentMembersRepo(db)
	keys := NewAPIKeysRepo(db)
	seedAgent(t, db, "agent-1")
	seedAgent(t, db, "agent-2")

	alice, _ := users.UpsertLogin(ctx, "https://idp.example", "sub-1", "a@example.com", "Alice", UserRoleMember)
	if err := members.Add(ctx, "agent-1", alice.ID, ""); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := members.Add(ctx, "agent-1", alice.ID, ""); err != nil {
		t.Fatalf("Add (dup) should be no-op: %v", err)
	}
	if ok, err := members.IsMember(ctx, "agent-1", alice.ID); err != nil || !ok {
		t.Fatalf("IsMember agent-1: ok=%v err=%v", ok, err)
	}
	if ok, err := members.IsMember(ctx, "agent-2", alice.ID); err != nil || ok {
		t.Fatalf("IsMember agent-2: ok=%v err=%v", ok, err)
	}
	list, err := members.ListByAgent(ctx, "agent-1")
	if err != nil || len(list) != 1 || list[0].UserEmail != "a@example.com" || list[0].Role != AgentMemberRoleOwner {
		t.Fatalf("ListByAgent: %+v err=%v", list, err)
	}
	ids, err := members.AgentIDsForUser(ctx, alice.ID)
	if err != nil || len(ids) != 1 || ids[0] != "agent-1" {
		t.Fatalf("AgentIDsForUser: %v err=%v", ids, err)
	}

	if _, err := keys.Create(ctx, APIKey{AgentID: "agent-1", CreatedBy: alice.ID, KeyHash: "h1", KeyPrefix: "p1"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := keys.Create(ctx, APIKey{AgentID: "agent-2", CreatedBy: alice.ID, KeyHash: "h2", KeyPrefix: "p2"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	n, err := keys.RevokeByCreatorForAgent(ctx, alice.ID, "agent-1", "admin")
	if err != nil || n != 1 {
		t.Fatalf("RevokeByCreatorForAgent: n=%d err=%v", n, err)
	}
	if _, err := keys.ResolveActive(ctx, "h2", time.Now()); err != nil {
		t.Fatalf("expected agent-2 key untouched, got %v", err)
	}
	if err := members.Remove(ctx, "agent-1", alice.ID); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if err := members.Remove(ctx, "agent-1", alice.ID); err != sql.ErrNoRows {
		t.Fatalf("expected ErrNoRows on second Remove, got %v", err)
	}
}
