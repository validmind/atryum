package invocation

import "context"

// Rule actions.
const (
	RuleActionAutoApprove   = "auto_approve"
	RuleActionAutoDeny      = "auto_deny"
	RuleActionHumanApproval = "human_approval"
	// RuleActionAIEvaluation routes the invocation to an LLM for automated
	// accept/reject evaluation using the referenced model configuration.
	// Until LLM wiring is implemented, matched invocations fall back to
	// human_approval so no invocation is silently dropped.
	RuleActionAIEvaluation = "ai_evaluation"
)

// ApprovalRule is the invocation-layer view of a configured rule.
// It is intentionally decoupled from the store layer to avoid import cycles.
type ApprovalRule struct {
	ID              string   // unique rule identifier
	Action          string   // one of the RuleAction* constants
	ServerPatterns  []string // empty slice = match any server
	ToolPatterns    []string // empty slice = match any tool
	AgentIDPattern  string   // "*" or "" = match any agent
	ModelConfigCUID string   // VM agent model config to use for ai_evaluation rules
	AgentCUIDs      []string // Atryum agent CUIDs this rule targets; empty = all
	Enabled         bool
}

// rulesStore is the minimal interface the invocation service needs.
// store.RulesRepo satisfies this via its ListApprovalRules method.
type rulesStore interface {
	ListApprovalRules(ctx context.Context) ([]ApprovalRule, error)
}

// matchRule returns the first enabled rule that matches the given invocation
// parameters, or nil if no rule matches (fall back to human approval).
// The agentID is the authenticated agent identity (empty when auth is
// disabled — callers fall back to request_id for parity with pre-auth behavior).
// The agentCUID is the Atryum-local agent record CUID used by ai_evaluation rules.
func matchRule(rules []ApprovalRule, server, tool, agentID, agentCUID string) *ApprovalRule {
	for i := range rules {
		r := &rules[i]
		if !r.Enabled {
			continue
		}
		if !matchPatterns(r.ServerPatterns, server) {
			continue
		}
		if !matchPatterns(r.ToolPatterns, tool) {
			continue
		}
		if !matchAgentIDPattern(r.AgentIDPattern, agentID) {
			continue
		}
		if !matchAgentCUIDs(r.AgentCUIDs, agentCUID) {
			continue
		}
		return r
	}
	return nil
}

// matchAgentCUIDs returns true when cuids is empty (match all) or contains agentCUID.
func matchAgentCUIDs(cuids []string, agentCUID string) bool {
	if len(cuids) == 0 {
		return true
	}
	for _, c := range cuids {
		if c == agentCUID {
			return true
		}
	}
	return false
}

// matchPatterns returns true when value matches any entry in patterns,
// or when patterns is empty (meaning "match all").
func matchPatterns(patterns []string, value string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, p := range patterns {
		if p == "*" || p == value {
			return true
		}
	}
	return false
}

// matchAgentIDPattern returns true when the rule's agent_id pattern matches the
// authenticated agent identity. An empty or "*" pattern matches any agent.
func matchAgentIDPattern(pattern, agentID string) bool {
	return pattern == "" || pattern == "*" || pattern == agentID
}
