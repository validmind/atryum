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
