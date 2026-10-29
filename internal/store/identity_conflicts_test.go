package store

import (
	"context"
	"testing"
)

func TestIdentityConflictsFindsAnAliasOnAnotherAgentsBinding(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	if err := InitDB(db); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	ctx := context.Background()
	agents := NewAgentsRepo(db)
	bindings := NewManagedAgentBindingRepo(db)
	for _, a := range []AgentRecord{
		{ID: "payments", VMCUID: "payments", VMName: "Payments bot", AgentIDs: `["agent_01own"]`},
		{ID: "helper", VMCUID: "helper", VMName: "Helper", AgentIDs: `["agent_01payments","free-id"]`},
	} {
		if err := agents.Create(ctx, a); err != nil {
			t.Fatalf("Create %s: %v", a.ID, err)
		}
	}
	// Payments bot is bound to agent_01payments (Helper's alias) and to its own alias.
	if err := bindings.ReplaceForAgent(ctx, "payments", []ManagedAgentBinding{
		{ID: "b1", AgentCUID: "payments", Account: "default", ClaudeAgentID: "agent_01payments"},
		{ID: "b2", AgentCUID: "payments", Account: "default", ClaudeAgentID: "agent_01own"},
	}); err != nil {
		t.Fatalf("ReplaceForAgent: %v", err)
	}

	got, err := IdentityConflicts(ctx, agents, bindings)
	if err != nil {
		t.Fatalf("IdentityConflicts: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 conflict, got %+v", got)
	}
	c := got[0]
	if c.Identity != "agent_01payments" || c.AliasAgent.VMName != "Helper" || c.BindingAgent.VMName != "Payments bot" {
		t.Fatalf("wrong conflict: identity=%s alias=%s bound=%s", c.Identity, c.AliasAgent.VMName, c.BindingAgent.VMName)
	}
}
