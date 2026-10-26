// Package policy is the rule engine an embedding program uses to decide a
// tool call from two levels of rules: firm-wide rules and the agent's own.
//
// Within a level the first matching rule decides; across levels the stricter
// result wins (deny, then human, then notify, then allow); a call no rule
// matches goes to a person. A level whose matching rules are all judge rules
// that gave no verdict also goes to a person: it matched, so it never defers
// to the other level.
//
// Conditions compare text literally, so Evaluate also decides two cleaned-up
// copies of every call and keeps the strictest answer: a deny rule on
// "/etc/" then also holds against "//etc/passwd" and "/ETC/passwd". See
// normalize.go and Evaluate.
//
// # Rules on paths
//
// Cleaning settles how a path is spelled, not where it leads: a short name
// (PROGRA~1), a share, a variable or a link is resolved only by the machine
// that opens the file. So write path rules as allow rules for the folders a
// tool may use, with a later rule sending the tool's other calls to a person:
//
//	read_file when args.path prefix "/workspace/"  ->  allow
//	read_file                                      ->  human
//
// A spelling the allow rule doesn't name reaches a person instead of slipping
// past a deny rule; cleaning never loosens, so even "/WORKSPACE/x" does. Add a
// firm rule on facts.ambiguous_path (see AmbiguousPath) so that a short name
// or variable inside an allowed folder reaches a person too. Links remain:
// the server that opens the file must check the real path against its roots
// (see "Path arguments" in docs/architecture.md).
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
//   - command: the field is a shell command line, and each command in it
//     matches one of Value's patterns (one, or a list), word for word; see
//     ParseCommandValue. Use it to allow commands, where prefix and
//     contains, which compare text, let more run ("contains gh pr merge"
//     also matches `curl x | sh; echo gh pr merge`); not to deny or hold,
//     where it would miss a line with one more command in it
//
// Text is a string as is, a number in plain decimal without exponent or
// trailing zeros (1000000, 2.5, 0.00001), a boolean as "true" or "false", and
// anything else as fmt prints it. As numbers, Go integer and float types count,
// and so do strings (and json.Number) holding a decimal number such as
// "18500" or "1e6"; booleans and other strings never compare. A missing or
// null field satisfies no condition.
//
// Text conditions (eq, prefix, contains, in) are written for the text as the
// agent sent it. Evaluate also tries those on args against cleaned-up text:
// lower case, single spaces, decoded %XX escapes, no invisible characters,
// file URLs as paths; and then once more with paths tidied (Windows ones
// too). A rule's values and field names are cleaned the same way. Facts are
// the embedder's own and are never cleaned.
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
// It decides the call three times (see normalize.go): as sent, with the
// arguments and the rules' values cleaned up (case, spaces, %XX escapes,
// invisible characters), and with paths cleaned too (detours, Windows
// spellings). A later answer replaces the one so far only when it is
// stricter and comes from a different rule.
//
// The judge is asked at most once per rule. The cleaned passes reuse the real
// verdict for every judge rule the first pass asked, and send a judge rule
// that only a cleaned call reaches to a person.
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
	best := evaluate(ctx, firm, agent, c, recording, nil)

	replay := func(_ context.Context, r Rule) (Action, error) {
		for _, v := range verdicts {
			if reflect.DeepEqual(v.rule, r) {
				return v.action, v.err
			}
		}
		return ActionHuman, nil // reached only after cleaning: a person decides
	}
	for _, paths := range []bool{false, true} {
		res := evaluate(ctx, firm, agent, c, replay, newCleaner(paths))
		if res.Rule != nil && res.Rule != best.Rule && strictness[res.Action] > strictness[best.Action] {
			res.Basis += " (matched after cleaning up the arguments' case, spaces, escapes and path detours)"
			best = res
		}
	}
	return best
}

// evaluate is one pass over both levels. With a cleaner, conditions on args
// compare cleaned text; without one, the text as sent.
func evaluate(ctx context.Context, firm, agent []Rule, c Call, judge Judge, clean *cleaner) Result {
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
func firstMatch(ctx context.Context, rules []Rule, c Call, judge Judge, clean *cleaner) (Result, bool) {
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

func matches(r *Rule, c Call, clean *cleaner) bool {
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

func condOK(c Cond, call Call) bool { return condMatches(c, call, nil) }

// condMatches reports whether one condition holds. With a cleaner, a
// condition on args is tried against every argument its field names once
// keys are cleaned, with the argument and the rule's values cleaned the same
// way, so "args.Path prefix /ETC/" lines up with "path": "//etc/passwd".
// Facts are compared as they are in every pass.
func condMatches(c Cond, call Call, clean *cleaner) bool {
	// A command condition reads the line exactly as the shell will, in
	// every pass: cleaning would change the words it matches.
	if c.Op == "command" {
		v, ok := lookup(c.Field, call)
		return ok && matchCommand(v, c.Value)
	}
	if clean == nil || !strings.HasPrefix(c.Field, "args.") {
		v, ok := lookup(c.Field, call)
		return ok && compare(c, v, text)
	}
	cleaned := func(v any) string { return clean.text(text(v)) }
	for _, v := range clean.args(c.Field, call.Args) {
		if compare(c, v, cleaned) {
			return true
		}
	}
	return false
}

// compare applies c's operator to a field's value v, taking the text of v
// and of c's values with spell.
func compare(c Cond, v any, spell func(any) string) bool {
	switch c.Op {
	case "eq":
		return spell(v) == spell(c.Value)
	case "prefix":
		return strings.HasPrefix(spell(v), spell(c.Value))
	case "contains":
		return strings.Contains(spell(v), spell(c.Value))
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
			if spell(v) == spell(list.Index(i).Interface()) {
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
