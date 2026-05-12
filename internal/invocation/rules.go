package invocation

import "context"

// Rule actions.
const (
	RuleActionAutoApprove   = "auto_approve"
	RuleActionAutoDeny      = "auto_deny"
	RuleActionHumanApproval = "human_approval"
)

// ApprovalRule is the invocation-layer view of a configured rule.
// It is intentionally decoupled from the store layer to avoid import cycles.
type ApprovalRule struct {
	Action         string   // one of the RuleAction* constants
	ServerPatterns []string // empty slice = match any server
	ToolPatterns   []string // empty slice = match any tool
	UserPattern    string   // "*" or "" = match any user
	Enabled        bool
}

// rulesStore is the minimal interface the invocation service needs.
// store.RulesRepo satisfies this via its ListApprovalRules method.
type rulesStore interface {
	ListApprovalRules(ctx context.Context) ([]ApprovalRule, error)
}

// matchRule returns the first enabled rule that matches the given invocation
// parameters, or nil if no rule matches (fall back to human approval).
func matchRule(rules []ApprovalRule, server, tool, user string) *ApprovalRule {
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
		if !matchUserPattern(r.UserPattern, user) {
			continue
		}
		return r
	}
	return nil
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

// matchUserPattern returns true when the rule's user pattern matches user.
func matchUserPattern(pattern, user string) bool {
	return pattern == "" || pattern == "*" || pattern == user
}
