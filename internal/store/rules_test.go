package store

import (
	"context"
	"testing"
)

// TestRulesRepo_CreateVMPathAIEvaluationRule is a regression test for the
// VM-backend ai_evaluation rule path. Such rules set only ModelConfigCUID and
// leave AtryumLLMConfigID empty. Migration 018 added atryum_llm_config_id as
// TEXT NOT NULL DEFAULT ”, so writing SQL NULL (the old emptyToNil behavior)
// for an empty value violated the NOT NULL constraint and made VM-path rules
// impossible to create. The empty string must be persisted directly instead.
func TestRulesRepo_CreateVMPathAIEvaluationRule(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	if err := InitDB(db); err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	repo := NewRulesRepo(db)
	ctx := context.Background()

	rule := Rule{
		ID:              "rule-vm-path",
		Action:          "ai_evaluation",
		ServerPatterns:  []string{},
		ToolPatterns:    []string{},
		ModelConfigCUID: "model-config-cuid-123",
		// AtryumLLMConfigID intentionally left empty: VM backend path.
		Enabled: true,
		Order:   0,
	}

	if err := repo.Create(ctx, rule); err != nil {
		t.Fatalf("Create VM-path ai_evaluation rule: %v", err)
	}

	got, err := repo.Get(ctx, "rule-vm-path")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Action != "ai_evaluation" {
		t.Errorf("Action = %q, want ai_evaluation", got.Action)
	}
	if got.ModelConfigCUID != "model-config-cuid-123" {
		t.Errorf("ModelConfigCUID = %q, want model-config-cuid-123", got.ModelConfigCUID)
	}
	if got.AtryumLLMConfigID != "" {
		t.Errorf("AtryumLLMConfigID = %q, want empty string", got.AtryumLLMConfigID)
	}
}

// TestRulesRepo_CorruptPatternJSONFailsClosed is a regression test for
// scanRule silently defaulting a corrupt server_pattern/tool_pattern/
// agent_cuids column to an empty slice. Per matchPatterns/matchAgentCUIDs, an
// empty slice means "match all" — so a decode failure used to silently widen
// a narrowly-scoped rule (e.g. an auto_approve limited to one server) into
// one that matches every server, tool, or agent. Get/List must now surface
// the decode error instead of returning a rule with a silently expanded
// blast radius.
func TestRulesRepo_CorruptPatternJSONFailsClosed(t *testing.T) {
	for _, column := range []string{"server_pattern", "tool_pattern", "agent_cuids"} {
		t.Run(column, func(t *testing.T) {
			db, cleanup := openTestDB(t)
			defer cleanup()
			if err := InitDB(db); err != nil {
				t.Fatalf("InitDB: %v", err)
			}

			repo := NewRulesRepo(db)
			ctx := context.Background()

			rule := Rule{
				ID:             "rule-corrupt-" + column,
				Action:         "auto_approve",
				ServerPatterns: []string{"staging-db"},
				ToolPatterns:   []string{"read_only_query"},
				Enabled:        true,
				Order:          0,
			}
			if err := repo.Create(ctx, rule); err != nil {
				t.Fatalf("Create: %v", err)
			}

			// Simulate corrupted data (bad manual edit, partial write, disk
			// corruption) by writing invalid JSON directly, bypassing the
			// repo's own encoder.
			if _, err := db.ExecContext(ctx, `UPDATE approval_rules SET `+column+` = ? WHERE id = ?`, "{not valid json", rule.ID); err != nil {
				t.Fatalf("corrupt %s: %v", column, err)
			}

			if _, err := repo.Get(ctx, rule.ID); err == nil {
				t.Fatalf("Get succeeded despite corrupt %s JSON; want an error rather than silently defaulting to \"match all\"", column)
			}
			if _, err := repo.List(ctx); err == nil {
				t.Fatalf("List succeeded despite corrupt %s JSON; want an error", column)
			}
		})
	}
}
