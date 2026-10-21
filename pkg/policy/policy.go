// Package policy is the rule engine an embedding program uses to decide a
// tool call from two levels of rules: firm-wide rules and the agent's own.
//
// Within a level the first matching rule decides; across levels the stricter
// result wins (deny, then human, then notify, then allow); a call no rule
// matches goes to a person. A level whose matching rules are all judge rules
// that gave no verdict also goes to a person: it matched, so it never defers
// to the other level.
//
// Conditions compare text literally, so Evaluate also decides a cleaned-up
// copy of every call and keeps the stricter answer: a deny rule on
// "/etc/" then also holds against "//etc/passwd" and "/ETC/passwd". See
// normalize.go and Evaluate.
package policy

import (
	"context"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
//   - in: Value is a list (any slice or array) and the field's text equals one
//     item's text
//
// Text is a string as is, a number in plain decimal without exponent or
// trailing zeros (1000000, 2.5, 0.00001), a boolean as "true" or "false", and
// anything else as fmt prints it. As numbers, Go integer and float types count,
// and so do strings (and json.Number) holding a decimal number such as
// "18500" or "1e6"; booleans and other strings never compare. A missing or
// null field satisfies no condition.
//
// Text conditions (eq, prefix, contains, in) are written for the text as the
// agent sent it. Evaluate also tries them against cleaned-up text: lower
// case, single spaces, decoded %XX escapes, no invisible characters, tidied
// paths. A rule's values and field names are cleaned the same way.
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
// matching rule; if no later rule in that level matches, the level's result
// is human. An error sends the call to a person.
type Judge func(ctx context.Context, r Rule) (Action, error)

// Result is the decision. Rule and Level ("firm" or "agent") name the rule
// that decided, and are empty when no rule matched; Rule points into the
// slice passed to Evaluate. Basis is a short reason naming that rule.
type Result struct {
	Action Action
	Rule   *Rule
	Level  string
	Basis  string
}

var strictness = map[Action]int{ActionAllow: 1, ActionNotify: 2, ActionHuman: 3, ActionDeny: 4}

// Evaluate decides c against the firm and agent rules. A nil judge behaves as
// one that always errors.
//
// It decides the call twice (see normalize.go): as sent, and with the
// arguments and the rules' values cleaned up (case, spaces, %XX escapes,
// invisible characters, path detours). The cleaned answer replaces the first
// only when it is stricter and comes from a different rule.
//
// The judge is asked at most once per rule. The cleaned pass reuses the real
// verdict for every judge rule the first pass asked, and sends a judge rule
// that only the cleaned call reaches to a person.
func Evaluate(ctx context.Context, firm, agent []Rule, c Call, judge Judge) Result {
	type asked struct {
		rule   Rule
		action Action
		err    error
	}
	var verdicts []asked
	recording := judge
	if judge != nil {
		recording = func(ctx context.Context, r Rule) (Action, error) {
			a, err := judge(ctx, r)
			verdicts = append(verdicts, asked{r, a, err})
			return a, err
		}
	}
	first := evaluate(ctx, firm, agent, c, recording, false)

	replay := func(_ context.Context, r Rule) (Action, error) {
		for _, v := range verdicts {
			if reflect.DeepEqual(v.rule, r) {
				return v.action, v.err
			}
		}
		return ActionHuman, nil // reached only after cleaning: a person decides
	}
	cleaned := c
	cleaned.Args = canonicalArgs(c.Args)
	second := evaluate(ctx, firm, agent, cleaned, replay, true)
	if second.Rule != nil && !sameRule(first, second) && strictness[second.Action] > strictness[first.Action] {
		second.Basis += " (matched after cleaning up the arguments' case, spaces, escapes and path detours)"
		return second
	}
	return first
}

// sameRule reports whether two results come from the same rule of the same level.
func sameRule(a, b Result) bool {
	return a.Rule != nil && b.Rule != nil && a.Level == b.Level && a.Rule.ID == b.Rule.ID
}

// evaluate is one pass over both levels. With clean set, conditions compare
// cleaned text: the caller has already cleaned c's arguments.
func evaluate(ctx context.Context, firm, agent []Rule, c Call, judge Judge, clean bool) Result {
	var best Result
	for _, lv := range []struct {
		name  string
		rules []Rule
	}{{"firm", firm}, {"agent", agent}} {
		r, ok := firstMatch(ctx, lv.rules, c, judge, clean)
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

// firstMatch returns the first rule in rules that matches c and decides it,
// or human on the last judge rule that matched if every one deferred.
func firstMatch(ctx context.Context, rules []Rule, c Call, judge Judge, clean bool) (Result, bool) {
	var deferred *Rule
	for i := range rules {
		r := &rules[i]
		if !matches(r, c, clean) {
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
		deferred = r // unknown verdict: keep scanning this level
	}
	if deferred != nil {
		return Result{Action: ActionHuman, Rule: deferred, Basis: strings.TrimSpace(deferred.ID+" "+deferred.Note) + " → judge gave no verdict and no later rule matched, a person decides"}, true
	}
	return Result{}, false
}

func matches(r *Rule, c Call, clean bool) bool {
	if !glob(r.Tool, c.Tool) || (r.Server != "" && !glob(r.Server, c.Server)) {
		return false
	}
	for _, cond := range r.When {
		if !condMatches(cond, c, clean) {
			return false
		}
	}
	return true
}

// globs caches compiled patterns by their text, up to maxGlobs of them.
// Published rule sets hold a few hundred patterns; the bound only guards
// against patterns from unbounded input (drafts, replays of edited rules), by
// starting over when it is reached.
var (
	globs     sync.Map
	globCount atomic.Int64
)

const maxGlobs = 4096

// glob reports whether s matches one of the "|"-separated patterns in g.
func glob(g, s string) bool {
	re, ok := globs.Load(g)
	if !ok {
		alts := strings.Split(g, "|")
		for i, p := range alts {
			alts[i] = strings.ReplaceAll(regexp.QuoteMeta(p), `\*`, `.*`)
		}
		re = regexp.MustCompile(`^(?s:` + strings.Join(alts, "|") + `)$`)
		if globCount.Add(1) > maxGlobs {
			globs.Clear()
			globCount.Store(1)
		}
		globs.Store(g, re)
	}
	return re.(*regexp.Regexp).MatchString(s)
}

func condOK(c Cond, call Call) bool { return condMatches(c, call, false) }

// condMatches reports whether one condition holds. With clean set, the rule's
// field name and values are cleaned the same way as the call's arguments
// (which the caller cleaned), so "args.Path prefix /ETC/" lines up with a
// cleaned "/etc/passwd".
func condMatches(c Cond, call Call, clean bool) bool {
	field, want := c.Field, text
	if clean {
		field = canonicalField(field)
		want = func(v any) string { return canonicalText(text(v)) }
	}
	v, ok := lookup(field, call)
	if !ok {
		return false
	}
	switch c.Op {
	case "eq":
		return text(v) == want(c.Value)
	case "prefix":
		return strings.HasPrefix(text(v), want(c.Value))
	case "contains":
		return strings.Contains(text(v), want(c.Value))
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
		list := reflect.ValueOf(c.Value)
		if list.Kind() != reflect.Slice && list.Kind() != reflect.Array {
			return false
		}
		for i := 0; i < list.Len(); i++ {
			if text(v) == want(list.Index(i).Interface()) {
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
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.String:
		return rv.String()
	case reflect.Float32:
		return strconv.FormatFloat(rv.Float(), 'f', -1, 32)
	case reflect.Float64:
		return strconv.FormatFloat(rv.Float(), 'f', -1, 64)
	}
	return fmt.Sprint(v)
}

func number(v any) (float64, bool) {
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return float64(rv.Int()), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return float64(rv.Uint()), true
	case reflect.Float32, reflect.Float64:
		return rv.Float(), true
	case reflect.String: // includes json.Number
		f, err := strconv.ParseFloat(strings.TrimSpace(rv.String()), 64)
		return f, err == nil
	}
	return 0, false
}
