// Package policy is the rule engine an embedding program uses to decide a
// tool call from two levels of rules: firm-wide rules and the agent's own.
//
// Within a level the first matching rule decides; across levels the stricter
// result wins (deny, then human, then notify, then allow); a call no rule
// matches goes to a person.
package policy

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Action is what a rule does with a matching call: "allow", "notify" (allow,
// then tell someone), "deny", "human" (hold for a person) or "judge" (ask the
// Judge, which answers allow, deny or human).
type Action string

const (
	ActionAllow  Action = "allow"
	ActionNotify Action = "notify"
	ActionDeny   Action = "deny"
	ActionHuman  Action = "human"
	ActionJudge  Action = "judge"
)

// Cond is one condition on a call. Field is "args.<dotted.path>" into the
// call's arguments or "facts.<name>" for a fact the embedder computed (see
// ContainsSecret). Op is one of:
//   - eq: the field's text equals Value's text
//   - prefix, contains: the field's text starts with or contains Value's text
//   - gt, lt: the field and Value compared as numbers
//   - in: Value is a list and the field's text equals one item's text
//
// A missing field satisfies no condition.
type Cond struct {
	Field string `json:"field"`
	Op    string `json:"op"`
	Value any    `json:"value"`
}

// Rule matches calls by tool and server, then conditions. Tool and Server are
// "|"-separated globs in which "*" matches anything; an empty Server matches
// every server. Never marks a rule written from a charter's "never" clause;
// the engine treats it like any other rule.
type Rule struct {
	ID     string `json:"id"`
	Tool   string `json:"tool"`
	Server string `json:"server,omitempty"`
	When   []Cond `json:"when,omitempty"`
	Action Action `json:"action"`
	Never  bool   `json:"never,omitempty"`
	Note   string `json:"note,omitempty"`
}

// Call is the tool call being decided.
type Call struct {
	Tool, Server string
	Args         map[string]any
	Facts        map[string]any
}

// Judge is called for a matching "judge" rule. It returns allow, deny or
// human, or "" for an unknown verdict, which moves on to the level's next
// matching rule. An error sends the call to a person.
type Judge func(ctx context.Context, r Rule) (Action, error)

// Result is the decision. Rule and Level ("firm" or "agent") name the rule
// that decided, and are empty when no rule matched. Basis is a short reason
// naming that rule.
type Result struct {
	Action Action
	Rule   *Rule
	Level  string
	Basis  string
}

var strictness = map[Action]int{ActionAllow: 1, ActionNotify: 2, ActionHuman: 3, ActionDeny: 4}

// Evaluate decides c against the firm and agent rules. A nil judge behaves as
// one that always errors.
func Evaluate(ctx context.Context, firm, agent []Rule, c Call, judge Judge) Result {
	var best Result
	for _, lv := range []struct {
		name  string
		rules []Rule
	}{{"firm", firm}, {"agent", agent}} {
		r, ok := firstMatch(ctx, lv.rules, c, judge)
		// On a tie the firm result stands.
		if ok && strictness[r.Action] > strictness[best.Action] {
			r.Level = lv.name
			best = r
		}
	}
	if best.Rule == nil {
		return Result{Action: ActionHuman, Basis: "No rule matched: a person decides"}
	}
	return best
}

// firstMatch returns the first rule in rules that matches c and decides it.
func firstMatch(ctx context.Context, rules []Rule, c Call, judge Judge) (Result, bool) {
	for i := range rules {
		r := &rules[i]
		if !matches(r, c) {
			continue
		}
		basis := strings.TrimSpace(r.ID + " " + r.Note)
		if r.Action != ActionJudge {
			if _, known := strictness[r.Action]; !known {
				// A malformed rule fails safe rather than being skipped.
				return Result{Action: ActionHuman, Rule: r, Basis: fmt.Sprintf("%s → unknown action %q, a person decides", basis, r.Action)}, true
			}
			return Result{Action: r.Action, Rule: r, Basis: basis}, true
		}
		var (
			v   Action
			err error
		)
		if judge == nil {
			err = fmt.Errorf("no judge configured")
		} else {
			v, err = judge(ctx, *r)
		}
		if err != nil {
			return Result{Action: ActionHuman, Rule: r, Basis: fmt.Sprintf("%s → judge failed (%v), a person decides", basis, err)}, true
		}
		switch v {
		case ActionAllow, ActionDeny, ActionHuman:
			return Result{Action: v, Rule: r, Basis: fmt.Sprintf("%s → judge said %s", basis, v)}, true
		}
		// Unknown verdict: keep scanning this level.
	}
	return Result{}, false
}

func matches(r *Rule, c Call) bool {
	if !glob(r.Tool, c.Tool) || (r.Server != "" && !glob(r.Server, c.Server)) {
		return false
	}
	for _, cond := range r.When {
		if !condOK(cond, c) {
			return false
		}
	}
	return true
}

// glob reports whether s matches one of the "|"-separated patterns in g.
// ponytail: compiles per call; cache compiled rules if evaluation shows up in profiles.
func glob(g, s string) bool {
	alts := strings.Split(g, "|")
	for i, p := range alts {
		alts[i] = strings.ReplaceAll(regexp.QuoteMeta(p), `\*`, `.*`)
	}
	return regexp.MustCompile(`^(?s:` + strings.Join(alts, "|") + `)$`).MatchString(s)
}

func condOK(c Cond, call Call) bool {
	v, ok := lookup(c.Field, call)
	if !ok {
		return false
	}
	switch c.Op {
	case "eq":
		return text(v) == text(c.Value)
	case "prefix":
		return strings.HasPrefix(text(v), text(c.Value))
	case "contains":
		return strings.Contains(text(v), text(c.Value))
	case "gt", "lt":
		a, okA := number(v)
		b, okB := number(c.Value)
		if !okA || !okB {
			return false
		}
		if c.Op == "gt" {
			return a > b
		}
		return a < b
	case "in":
		items, _ := c.Value.([]any)
		if ss, ok := c.Value.([]string); ok {
			for _, s := range ss {
				items = append(items, s)
			}
		}
		for _, it := range items {
			if text(v) == text(it) {
				return true
			}
		}
	}
	return false
}

// lookup resolves "args.a.b" or "facts.name" against the call.
func lookup(field string, c Call) (any, bool) {
	root, path, _ := strings.Cut(field, ".")
	var cur any
	switch root {
	case "args":
		cur = c.Args
	case "facts":
		cur = c.Facts
	default:
		return nil, false
	}
	if path == "" {
		return nil, false
	}
	for _, k := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = m[k]; !ok {
			return nil, false
		}
	}
	return cur, cur != nil
}

func text(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

func number(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case interface{ Float64() (float64, error) }: // json.Number
		f, err := n.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
		return f, err == nil
	}
	return 0, false
}
