package store

import (
	"context"
	"encoding/json"
	"sort"
)

// IdentityConflict is an identity that one agent has as an alias while a
// Claude Managed Agent binding gives it to another. Atryum resolves an
// identity by alias before binding, so the alias holder gets its calls, under
// its rules and charter.
type IdentityConflict struct {
	Identity     string
	AliasAgent   AgentRecord
	BindingAgent AgentRecord // VMName is empty when the bound agent is gone
}

// IdentityConflicts lists every identity claimed both ways by different
// agents. The API refuses new ones; this finds those made before it did.
func IdentityConflicts(ctx context.Context, agents *AgentsRepo, bindings *ManagedAgentBindingRepo) ([]IdentityConflict, error) {
	records, err := agents.List(ctx)
	if err != nil {
		return nil, err
	}
	all, err := bindings.List(ctx)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]AgentRecord, len(records))
	for _, rec := range records {
		byID[rec.ID] = rec
	}
	boundTo := make(map[string]string, len(all))
	for _, b := range all {
		boundTo[b.ClaudeAgentID] = b.AgentCUID
	}
	var out []IdentityConflict
	for _, rec := range records {
		var aliases []string
		if err := json.Unmarshal([]byte(rec.AgentIDs), &aliases); err != nil {
			continue
		}
		for _, alias := range aliases {
			owner, ok := boundTo[alias]
			if !ok || owner == rec.ID {
				continue
			}
			bound, ok := byID[owner]
			if !ok {
				bound = AgentRecord{ID: owner}
			}
			out = append(out, IdentityConflict{Identity: alias, AliasAgent: rec, BindingAgent: bound})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Identity < out[j].Identity })
	return out, nil
}
