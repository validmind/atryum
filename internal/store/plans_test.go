package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"atryum/internal/invocation"
)

func newPlanFixture(id, agentID string, status invocation.PlanStatus, submittedAt time.Time) invocation.Plan {
	return invocation.Plan{
		PlanID:  id,
		AgentID: agentID,
		Source:  "external",
		Goal:    "upgrade dependency",
		Actions: []invocation.PlanAction{
			{Tool: "Bash", Description: "npm audit"},
			{Tool: "read_file", Server: "fs"},
		},
		Status:      status,
		Revision:    1,
		TTLSeconds:  3600,
		SubmittedAt: submittedAt,
	}
}

func TestPlansRepo_CRUD(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	if err := InitDB(db); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	repo := NewPlansRepo(db)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Second)
	plan := newPlanFixture("plan_1", "agent-a", invocation.PlanStatusReceived, now)
	if err := repo.Create(ctx, plan); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := repo.Get(ctx, "plan_1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.AgentID != "agent-a" || got.Goal != "upgrade dependency" || len(got.Actions) != 2 {
		t.Fatalf("unexpected plan: %+v", got)
	}
	if got.Actions[1].Server != "fs" {
		t.Fatalf("actions round-trip failed: %+v", got.Actions)
	}
	if got.TTLSeconds != 3600 {
		t.Fatalf("ttl = %d", got.TTLSeconds)
	}

	exp := now.Add(time.Hour)
	decided := now.Add(time.Minute)
	got.Status = invocation.PlanStatusApproved
	got.Approval = &invocation.Approval{Status: "approved"}
	got.ExpiresAt = &exp
	got.DecidedAt = &decided
	got.Feedback = ""
	if err := repo.Update(ctx, got); err != nil {
		t.Fatalf("Update: %v", err)
	}
	updated, err := repo.Get(ctx, "plan_1")
	if err != nil {
		t.Fatalf("Get after update: %v", err)
	}
	if updated.Status != invocation.PlanStatusApproved || updated.Approval == nil || updated.Approval.Status != "approved" {
		t.Fatalf("update not persisted: %+v", updated)
	}
	if updated.ExpiresAt == nil || !updated.ExpiresAt.Equal(exp) {
		t.Fatalf("expires_at = %v, want %v", updated.ExpiresAt, exp)
	}

	if err := repo.Update(ctx, newPlanFixture("plan_missing", "x", invocation.PlanStatusDenied, now)); err != sql.ErrNoRows {
		t.Fatalf("Update missing plan err = %v, want sql.ErrNoRows", err)
	}
	if _, err := repo.Get(ctx, "plan_missing"); err != sql.ErrNoRows {
		t.Fatalf("Get missing plan err = %v, want sql.ErrNoRows", err)
	}
}

func TestPlansRepo_ListAndFilters(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	if err := InitDB(db); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	repo := NewPlansRepo(db)
	ctx := context.Background()

	base := time.Now().UTC().Truncate(time.Second)
	fixtures := []invocation.Plan{
		newPlanFixture("plan_a1", "agent-a", invocation.PlanStatusApproved, base.Add(-3*time.Minute)),
		newPlanFixture("plan_a2", "agent-a", invocation.PlanStatusApproved, base.Add(-1*time.Minute)),
		newPlanFixture("plan_a3", "agent-a", invocation.PlanStatusDenied, base.Add(-2*time.Minute)),
		newPlanFixture("plan_b1", "agent-b", invocation.PlanStatusApproved, base),
	}
	for _, p := range fixtures {
		if err := repo.Create(ctx, p); err != nil {
			t.Fatalf("Create %s: %v", p.PlanID, err)
		}
	}

	items, total, err := repo.List(ctx, invocation.PlanListFilter{Status: "approved"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 3 || len(items) != 3 {
		t.Fatalf("approved list = %d items total %d", len(items), total)
	}

	items, total, err = repo.List(ctx, invocation.PlanListFilter{AgentID: "agent-a"})
	if err != nil {
		t.Fatalf("List by agent: %v", err)
	}
	if total != 3 || len(items) != 3 {
		t.Fatalf("agent-a list = %d items total %d", len(items), total)
	}

	active, err := repo.ListActiveByAgent(ctx, []string{"agent-a"})
	if err != nil {
		t.Fatalf("ListActiveByAgent: %v", err)
	}
	if len(active) != 2 {
		t.Fatalf("active count = %d, want 2", len(active))
	}
	// Newest first.
	if active[0].PlanID != "plan_a2" || active[1].PlanID != "plan_a1" {
		t.Fatalf("active order = %s, %s", active[0].PlanID, active[1].PlanID)
	}

	// Widening the lookup to several ids at once (an agent record's
	// registered aliases) returns approved plans owned by any of them.
	widened, err := repo.ListActiveByAgent(ctx, []string{"agent-a", "agent-b"})
	if err != nil {
		t.Fatalf("ListActiveByAgent widened: %v", err)
	}
	if len(widened) != 3 {
		t.Fatalf("widened active count = %d, want 3", len(widened))
	}
}

func TestPlansRepo_Revisions(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	if err := InitDB(db); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	repo := NewPlansRepo(db)
	ctx := context.Background()

	base := time.Now().UTC().Truncate(time.Second)
	parent := newPlanFixture("plan_parent", "agent-a", invocation.PlanStatusNeedsRevision, base.Add(-time.Minute))
	if err := repo.Create(ctx, parent); err != nil {
		t.Fatalf("Create parent: %v", err)
	}
	child := newPlanFixture("plan_child", "agent-a", invocation.PlanStatusPendingApproval, base)
	parentID := parent.PlanID
	child.ParentPlanID = &parentID
	child.Revision = 2
	if err := repo.Create(ctx, child); err != nil {
		t.Fatalf("Create child: %v", err)
	}

	revs, err := repo.ListRevisions(ctx, "plan_parent")
	if err != nil {
		t.Fatalf("ListRevisions: %v", err)
	}
	if len(revs) != 1 || revs[0].PlanID != "plan_child" || revs[0].Revision != 2 {
		t.Fatalf("revisions = %+v", revs)
	}
	if revs[0].ParentPlanID == nil || *revs[0].ParentPlanID != "plan_parent" {
		t.Fatalf("parent link = %v", revs[0].ParentPlanID)
	}
}

func TestPlanEventsRepo(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	if err := InitDB(db); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	plans := NewPlansRepo(db)
	events := NewPlanEventsRepo(db)
	ctx := context.Background()

	plan := newPlanFixture("plan_evt", "agent-a", invocation.PlanStatusReceived, time.Now().UTC())
	if err := plans.Create(ctx, plan); err != nil {
		t.Fatalf("Create plan: %v", err)
	}
	for _, typ := range []string{"plan.received", "plan.pending_approval"} {
		if err := events.Create(ctx, invocation.PlanEvent{
			PlanID:    "plan_evt",
			EventType: typ,
			Payload:   []byte(`{"status":"x"}`),
			CreatedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatalf("Create event %s: %v", typ, err)
		}
	}

	items, total, err := events.ListByPlan(ctx, "plan_evt", invocation.EventListFilter{})
	if err != nil {
		t.Fatalf("ListByPlan: %v", err)
	}
	if total != 2 || len(items) != 2 {
		t.Fatalf("events = %d total %d", len(items), total)
	}
	if items[0].EventType != "plan.received" || items[1].EventType != "plan.pending_approval" {
		t.Fatalf("event order: %s, %s", items[0].EventType, items[1].EventType)
	}
}
