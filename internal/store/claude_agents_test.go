package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func TestClaudeAgentsRepo_AgentCRUD(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	if err := InitDB(db); err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	repo := NewClaudeAgentsRepo(db)
	ctx := context.Background()

	agent := ClaudeAgent{
		ID:          "cagent_test1",
		Name:        "PR Reviewer",
		AgentID:     "agent_abc",
		Description: "reviews PRs",
		Enabled:     true,
	}
	if err := repo.CreateAgent(ctx, agent); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	got, err := repo.GetAgent(ctx, agent.ID)
	if err != nil {
		t.Fatalf("GetAgent: %v", err)
	}
	if got.Name != agent.Name || got.AgentID != agent.AgentID || got.Description != agent.Description || !got.Enabled {
		t.Fatalf("agent mismatch: %+v", got)
	}

	byName, err := repo.GetAgentByName(ctx, agent.Name)
	if err != nil {
		t.Fatalf("GetAgentByName: %v", err)
	}
	if byName.ID != agent.ID {
		t.Fatalf("GetAgentByName id = %q, want %q", byName.ID, agent.ID)
	}

	got.Name = "PR Reviewer v2"
	got.Enabled = false
	if err := repo.UpdateAgent(ctx, got); err != nil {
		t.Fatalf("UpdateAgent: %v", err)
	}
	updated, err := repo.GetAgent(ctx, agent.ID)
	if err != nil {
		t.Fatalf("GetAgent after update: %v", err)
	}
	if updated.Name != "PR Reviewer v2" || updated.Enabled {
		t.Fatalf("updated agent = %+v", updated)
	}

	list, err := repo.ListAgents(ctx)
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("ListAgents len = %d", len(list))
	}

	if err := repo.DeleteAgent(ctx, agent.ID); err != nil {
		t.Fatalf("DeleteAgent: %v", err)
	}
	if _, err := repo.GetAgent(ctx, agent.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected ErrNoRows after delete, got %v", err)
	}
}

func TestClaudeAgentsRepo_SessionCRUDAndCascade(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	if err := InitDB(db); err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	repo := NewClaudeAgentsRepo(db)
	ctx := context.Background()

	if err := repo.CreateAgent(ctx, ClaudeAgent{
		ID: "cagent_a", Name: "A", AgentID: "agent_a", Enabled: true,
	}); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	session := ClaudeAgentSession{
		ID:        "cagsess_1",
		AgentID:   "cagent_a",
		SessionID: "sess_xyz",
	}
	if err := repo.CreateSession(ctx, session); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	gotSession, err := repo.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if gotSession.SessionID != "sess_xyz" || gotSession.AgentID != "cagent_a" {
		t.Fatalf("session mismatch: %+v", gotSession)
	}
	if gotSession.LastEventCursor != "" {
		t.Fatalf("expected empty cursor, got %q", gotSession.LastEventCursor)
	}

	if err := repo.UpdateSessionCursor(ctx, session.ID, "evt_42"); err != nil {
		t.Fatalf("UpdateSessionCursor: %v", err)
	}
	gotSession, err = repo.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("GetSession after cursor update: %v", err)
	}
	if gotSession.LastEventCursor != "evt_42" {
		t.Fatalf("cursor = %q, want %q", gotSession.LastEventCursor, "evt_42")
	}

	byAgent, err := repo.ListSessionsByAgent(ctx, "cagent_a")
	if err != nil {
		t.Fatalf("ListSessionsByAgent: %v", err)
	}
	if len(byAgent) != 1 {
		t.Fatalf("expected 1 session, got %d", len(byAgent))
	}

	all, err := repo.ListAllSessions(ctx)
	if err != nil {
		t.Fatalf("ListAllSessions: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("ListAllSessions len = %d", len(all))
	}

	// Deleting the parent agent should cascade to the session row.
	if err := repo.DeleteAgent(ctx, "cagent_a"); err != nil {
		t.Fatalf("DeleteAgent: %v", err)
	}
	if _, err := repo.GetSession(ctx, session.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected session to be cascaded; got err=%v", err)
	}
}

func TestClaudeAgentsRepo_ListWatchableSessions(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	if err := InitDB(db); err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	repo := NewClaudeAgentsRepo(db)
	ctx := context.Background()

	if err := repo.CreateAgent(ctx, ClaudeAgent{ID: "a1", Name: "Enabled", AgentID: "x", Enabled: true}); err != nil {
		t.Fatalf("create enabled agent: %v", err)
	}
	if err := repo.CreateAgent(ctx, ClaudeAgent{ID: "a2", Name: "Disabled", AgentID: "y", Enabled: false}); err != nil {
		t.Fatalf("create disabled agent: %v", err)
	}
	if err := repo.CreateSession(ctx, ClaudeAgentSession{ID: "s1", AgentID: "a1", SessionID: "sess_keep"}); err != nil {
		t.Fatalf("create session 1: %v", err)
	}
	if err := repo.CreateSession(ctx, ClaudeAgentSession{ID: "s2", AgentID: "a2", SessionID: "sess_skip"}); err != nil {
		t.Fatalf("create session 2: %v", err)
	}

	out, err := repo.ListWatchableSessions(ctx)
	if err != nil {
		t.Fatalf("ListWatchableSessions: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 watchable session (disabled agent skipped), got %d", len(out))
	}
	if out[0].SessionID != "sess_keep" || out[0].AgentName != "Enabled" {
		t.Fatalf("wrong session: %+v", out[0])
	}
}
