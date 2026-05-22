package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	sq "github.com/Masterminds/squirrel"

	"atryum/internal/invocation"
)

// Rule is the store-level representation of an approval rule.
// ServerPatterns and ToolPatterns are serialized as JSON arrays in the
// server_pattern / tool_pattern TEXT columns; an empty slice means "match all".
// AgentIDPattern is the literal authenticated agent_id (or "*"/"" for any).
// ModelConfigCUID references the VM agent model configuration for ai_evaluation rules.
// AgentCUIDs is a JSON-encoded list of Atryum agent CUIDs the rule applies to;
// an empty slice means "match all agents".
type Rule struct {
	ID              string
	Action          string
	ServerPatterns  []string
	ToolPatterns    []string
	AgentIDPattern  string
	ModelConfigCUID string
	AgentCUIDs      []string
	Description     string
	Enabled         bool
	Order           int
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// RulesRepo provides CRUD and ordering operations for approval_rules.
type RulesRepo struct {
	db *sql.DB
	sb sq.StatementBuilderType
}

func NewRulesRepo(db *sql.DB) *RulesRepo {
	return NewRulesRepoWithDialect(db, DialectSQLite)
}

func NewRulesRepoWithDialect(db *sql.DB, dialect Dialect) *RulesRepo {
	return &RulesRepo{db: db, sb: statementBuilderForDialect(dialect)}
}

var ruleColumns = []string{
	"id", "action", "server_pattern", "tool_pattern", "agent_id_pattern",
	"model_config_cuid", "agent_cuids",
	"description", "enabled", "rule_order", "created_at", "updated_at",
}

func (r *RulesRepo) Create(ctx context.Context, rule Rule) error {
	now := time.Now().UTC()
	serverJSON, toolJSON, agentCUIDsJSON, err := encodeRulePatterns(rule)
	if err != nil {
		return err
	}
	query, args, err := r.sb.Insert("approval_rules").
		Columns(ruleColumns...).
		Values(
			rule.ID, rule.Action, serverJSON, toolJSON, rule.AgentIDPattern,
			emptyToNil(rule.ModelConfigCUID), agentCUIDsJSON,
			emptyToNil(rule.Description), boolToInt(rule.Enabled), rule.Order, now, now,
		).ToSql()
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, query, args...)
	return err
}

func (r *RulesRepo) Get(ctx context.Context, id string) (Rule, error) {
	query, args, err := r.sb.Select(ruleColumns...).
		From("approval_rules").
		Where(sq.Eq{"id": id}).
		ToSql()
	if err != nil {
		return Rule{}, err
	}
	return scanRule(r.db.QueryRowContext(ctx, query, args...))
}

func (r *RulesRepo) List(ctx context.Context) ([]Rule, error) {
	query, args, err := r.sb.Select(ruleColumns...).
		From("approval_rules").
		OrderBy("rule_order ASC").
		ToSql()
	if err != nil {
		return nil, err
	}
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Rule
	for rows.Next() {
		rule, err := scanRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rule)
	}
	return out, rows.Err()
}

// NextOrder returns MAX(rule_order)+1 so new rules are appended at the end.
func (r *RulesRepo) NextOrder(ctx context.Context) (int, error) {
	query, args, err := r.sb.Select("COALESCE(MAX(rule_order) + 1, 0)").
		From("approval_rules").
		ToSql()
	if err != nil {
		return 0, err
	}
	var order int
	if err := r.db.QueryRowContext(ctx, query, args...).Scan(&order); err != nil {
		return 0, err
	}
	return order, nil
}

func (r *RulesRepo) Update(ctx context.Context, rule Rule) error {
	now := time.Now().UTC()
	serverJSON, toolJSON, agentCUIDsJSON, err := encodeRulePatterns(rule)
	if err != nil {
		return err
	}
	query, args, err := r.sb.Update("approval_rules").
		Set("action", rule.Action).
		Set("server_pattern", serverJSON).
		Set("tool_pattern", toolJSON).
		Set("agent_id_pattern", rule.AgentIDPattern).
		Set("model_config_cuid", emptyToNil(rule.ModelConfigCUID)).
		Set("agent_cuids", agentCUIDsJSON).
		Set("description", emptyToNil(rule.Description)).
		Set("enabled", boolToInt(rule.Enabled)).
		Set("updated_at", now).
		Where(sq.Eq{"id": rule.ID}).
		ToSql()
	if err != nil {
		return err
	}
	result, err := r.db.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n == 0 {
		return sql.ErrNoRows
	}
	return err
}

func (r *RulesRepo) Delete(ctx context.Context, id string) error {
	query, args, err := r.sb.Delete("approval_rules").
		Where(sq.Eq{"id": id}).
		ToSql()
	if err != nil {
		return err
	}
	result, err := r.db.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n == 0 {
		return sql.ErrNoRows
	}
	return err
}

// Move swaps the rule_order of the target rule with its neighbor in the given
// direction ("up" or "down") and returns the full re-ordered list.
func (r *RulesRepo) Move(ctx context.Context, id string, direction string) ([]Rule, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck

	query, args, err := r.sb.Select("id", "rule_order").
		From("approval_rules").
		OrderBy("rule_order ASC").
		ToSql()
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	type idOrder struct {
		id    string
		order int
	}
	var items []idOrder
	for rows.Next() {
		var item idOrder
		if err := rows.Scan(&item.id, &item.order); err != nil {
			rows.Close()
			return nil, err
		}
		items = append(items, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	idx := -1
	for i, item := range items {
		if item.id == id {
			idx = i
			break
		}
	}
	if idx == -1 {
		return nil, sql.ErrNoRows
	}

	var neighborIdx int
	switch direction {
	case "up":
		if idx == 0 {
			return nil, fmt.Errorf("rule is already first")
		}
		neighborIdx = idx - 1
	case "down":
		if idx == len(items)-1 {
			return nil, fmt.Errorf("rule is already last")
		}
		neighborIdx = idx + 1
	default:
		return nil, fmt.Errorf("direction must be 'up' or 'down'")
	}

	now := time.Now().UTC()

	swapA, swapAArgs, err := r.sb.Update("approval_rules").
		Set("rule_order", items[neighborIdx].order).
		Set("updated_at", now).
		Where(sq.Eq{"id": items[idx].id}).
		ToSql()
	if err != nil {
		return nil, err
	}
	swapB, swapBArgs, err := r.sb.Update("approval_rules").
		Set("rule_order", items[idx].order).
		Set("updated_at", now).
		Where(sq.Eq{"id": items[neighborIdx].id}).
		ToSql()
	if err != nil {
		return nil, err
	}

	if _, err := tx.ExecContext(ctx, swapA, swapAArgs...); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, swapB, swapBArgs...); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return r.List(ctx)
}

func scanRule(scanner interface{ Scan(dest ...any) error }) (Rule, error) {
	var rule Rule
	var serverJSON, toolJSON, agentCUIDsJSON string
	var modelConfigCUID, description sql.NullString
	var enabled int
	if err := scanner.Scan(
		&rule.ID, &rule.Action, &serverJSON, &toolJSON, &rule.AgentIDPattern,
		&modelConfigCUID, &agentCUIDsJSON,
		&description, &enabled, &rule.Order, &rule.CreatedAt, &rule.UpdatedAt,
	); err != nil {
		return Rule{}, err
	}
	rule.Enabled = enabled == 1
	rule.Description = description.String
	rule.ModelConfigCUID = modelConfigCUID.String
	if err := json.Unmarshal([]byte(serverJSON), &rule.ServerPatterns); err != nil {
		rule.ServerPatterns = []string{}
	}
	if rule.ServerPatterns == nil {
		rule.ServerPatterns = []string{}
	}
	if err := json.Unmarshal([]byte(toolJSON), &rule.ToolPatterns); err != nil {
		rule.ToolPatterns = []string{}
	}
	if rule.ToolPatterns == nil {
		rule.ToolPatterns = []string{}
	}
	if agentCUIDsJSON != "" {
		if err := json.Unmarshal([]byte(agentCUIDsJSON), &rule.AgentCUIDs); err != nil {
			rule.AgentCUIDs = []string{}
		}
	}
	if rule.AgentCUIDs == nil {
		rule.AgentCUIDs = []string{}
	}
	return rule, nil
}

// ListApprovalRules satisfies the invocation.rulesStore interface.
// It converts store.Rule to invocation.ApprovalRule for evaluation in the
// invocation service without creating an import cycle.
func (r *RulesRepo) ListApprovalRules(ctx context.Context) ([]invocation.ApprovalRule, error) {
	rules, err := r.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]invocation.ApprovalRule, 0, len(rules))
	for _, rule := range rules {
		out = append(out, invocation.ApprovalRule{
			ID:              rule.ID,
			Action:          rule.Action,
			ServerPatterns:  rule.ServerPatterns,
			ToolPatterns:    rule.ToolPatterns,
			AgentIDPattern:  rule.AgentIDPattern,
			ModelConfigCUID: rule.ModelConfigCUID,
			AgentCUIDs:      rule.AgentCUIDs,
			Enabled:         rule.Enabled,
		})
	}
	return out, nil
}

// InsertBefore inserts rule into the priority list just above the rule with anchorID.
// If anchorID is empty, the rule is inserted at position 0 (highest priority).
// All rules at or after the anchor's position have their order incremented by 1.
func (r *RulesRepo) InsertBefore(ctx context.Context, anchorID string, rule Rule) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	var insertOrder int
	if anchorID == "" {
		insertOrder = 0
	} else {
		q, args, err := r.sb.Select("rule_order").From("approval_rules").Where(sq.Eq{"id": anchorID}).ToSql()
		if err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, q, args...).Scan(&insertOrder); err != nil {
			return err
		}
	}

	// Shift all rules at or after insertOrder up by 1.
	shiftQ, shiftArgs, err := r.sb.Update("approval_rules").
		Set("rule_order", sq.Expr("rule_order + 1")).
		Set("updated_at", time.Now().UTC()).
		Where(sq.GtOrEq{"rule_order": insertOrder}).
		ToSql()
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, shiftQ, shiftArgs...); err != nil {
		return err
	}

	// Insert the new rule at insertOrder.
	rule.Order = insertOrder
	now := time.Now().UTC()
	serverJSON, toolJSON, agentCUIDsJSON, err := encodeRulePatterns(rule)
	if err != nil {
		return err
	}
	insQ, insArgs, err := r.sb.Insert("approval_rules").
		Columns(ruleColumns...).
		Values(
			rule.ID, rule.Action, serverJSON, toolJSON, rule.AgentIDPattern,
			emptyToNil(rule.ModelConfigCUID), agentCUIDsJSON,
			emptyToNil(rule.Description), boolToInt(rule.Enabled), rule.Order, now, now,
		).ToSql()
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, insQ, insArgs...); err != nil {
		return err
	}

	return tx.Commit()
}

func encodeRulePatterns(rule Rule) (serverJSON string, toolJSON string, agentCUIDsJSON string, err error) {
	sp := rule.ServerPatterns
	if sp == nil {
		sp = []string{}
	}
	tp := rule.ToolPatterns
	if tp == nil {
		tp = []string{}
	}
	ac := rule.AgentCUIDs
	if ac == nil {
		ac = []string{}
	}
	spBytes, e := json.Marshal(sp)
	if e != nil {
		return "", "", "", e
	}
	tpBytes, e := json.Marshal(tp)
	if e != nil {
		return "", "", "", e
	}
	acBytes, e := json.Marshal(ac)
	if e != nil {
		return "", "", "", e
	}
	return string(spBytes), string(tpBytes), string(acBytes), nil
}
