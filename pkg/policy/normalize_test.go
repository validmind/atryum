package policy

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestCanonicalText(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"already clean", "/etc/passwd", "/etc/passwd"},
		{"upper case", "/ETC/Passwd", "/etc/passwd"},
		{"spaces at the ends", "  forbidden ", "forbidden"},
		{"runs of spaces and tabs", "drop    table\tusers", "drop table users"},
		{"double slash", "//etc/passwd", "/etc/passwd"},
		{"dot segment", "/etc/./passwd", "/etc/passwd"},
		{"dot-dot detour", "/tmp/../etc/passwd", "/etc/passwd"},
		{"backslashes", `\etc\passwd`, "/etc/passwd"},
		{"file URL", "file:///etc/passwd", "/etc/passwd"},
		{"percent escapes", "%2Fetc%2Fpasswd", "/etc/passwd"},
		{"double percent escapes", "%252Fetc%252Fpasswd", "/etc/passwd"},
		{"invalid percent escape is left", "100%", "100%"},
		{"zero-width space", "for​bidden", "forbidden"},
		{"NUL", "for\x00bidden", "forbidden"},
		{"escaped NUL", "for%00bidden", "forbidden"},
		{"full-width letters", "ｆｏｒｂｉｄｄｅｎ", "forbidden"},
		{"trailing slash is kept", "/etc/", "/etc/"},
		{"a name that only starts like it", "/etcetera/x", "/etcetera/x"},
		{"empty", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := canonicalText(tc.in); got != tc.want {
				t.Errorf("canonicalText(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestCanonicalArgs(t *testing.T) {
	args := map[string]any{
		"Path":    "//ETC/passwd",
		"count":   float64(3),
		"dry":     true,
		"nothing": nil,
		"nested":  map[string]any{"File": " /Tmp/../etc/x "},
		"list":    []any{"A  B", float64(1)},
	}
	before, _ := json.Marshal(args)
	want := map[string]any{
		"path":    "/etc/passwd",
		"count":   float64(3),
		"dry":     true,
		"nothing": nil,
		"nested":  map[string]any{"file": "/etc/x"},
		"list":    []any{"a b", float64(1)},
	}
	if got := canonicalArgs(args); !reflect.DeepEqual(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
	if after, _ := json.Marshal(args); string(after) != string(before) {
		t.Errorf("the original arguments were changed: %s", after)
	}
	if canonicalArgs(nil) != nil {
		t.Error("nil arguments should stay nil")
	}
}

// Two keys that clean to one name keep the first in sorted order, on every run.
func TestCanonicalArgsKeyCollisionIsDeterministic(t *testing.T) {
	args := map[string]any{"Path": "/b", "path": "/a"}
	for i := 0; i < 50; i++ {
		if got := canonicalArgs(args)["path"]; got != "/b" { // "Path" sorts before "path"
			t.Fatalf("run %d: got %v", i, got)
		}
	}
}

func TestCanonicalField(t *testing.T) {
	for in, want := range map[string]string{
		"args.Path":             "args.path",
		"args.Payment.Amount":   "args.payment.amount",
		"facts.contains_secret": "facts.contains_secret",
	} {
		if got := canonicalField(in); got != want {
			t.Errorf("canonicalField(%q) = %q, want %q", in, got, want)
		}
	}
}

// evaluate is a shorthand for one call against an agent level (and optional firm level).
func decide(firm, agent []Rule, tool string, args map[string]any, judge Judge) Result {
	return Evaluate(context.Background(), firm, agent, Call{Tool: tool, Args: args}, judge)
}

// The case from the bug report: a deny rule on a path, with a broad allow rule behind it.
var (
	readOnly = []Rule{{ID: "F-02", Tool: "read_*|get_*|list_*", Action: ActionAllow, Note: "Read-only tools"}}
	denyEtc  = []Rule{{ID: "R-10", Tool: "read_file", When: []Cond{{Field: "args.path", Op: "prefix", Value: "/etc/"}}, Action: ActionDeny}}
)

func TestDenyRuleOnAPathHoldsAgainstOtherSpellings(t *testing.T) {
	for _, path := range []string{
		"/etc/passwd",        // the plain spelling: always worked
		"//etc/passwd",       // double slash
		"/tmp/../etc/passwd", // a detour
		"/ETC/passwd",        // case
		" /etc/passwd",       // a leading space
		"file:///etc/passwd", // a file URL
		"%2Fetc%2Fpasswd",    // percent escapes
		`\etc\passwd`,        // backslashes
		"/etc/./passwd",      // a dot segment
		"/e​tc/passwd",       // a zero-width space
		"／ｅｔｃ／passwd",        // full-width characters
	} {
		if res := decide(readOnly, denyEtc, "read_file", map[string]any{"path": path}, nil); res.Action != ActionDeny || res.Rule.ID != "R-10" || res.Level != "agent" {
			t.Errorf("path %q: got %s by %v, want deny by R-10", path, res.Action, res.Rule)
		}
	}
}

func TestSafeCallsAreStillAllowed(t *testing.T) {
	for _, path := range []string{
		"/home/me/notes.txt",
		"/etcetera/readme", // starts like "/etc" but isn't under "/etc/"
		"/Users/Me/Documents/Notes.txt",
		"relative/path.txt",
	} {
		if res := decide(readOnly, denyEtc, "read_file", map[string]any{"path": path}, nil); res.Action != ActionAllow || res.Rule.ID != "F-02" {
			t.Errorf("path %q: got %s by %v, want allow by F-02", path, res.Action, res.Rule)
		}
	}
}

// The answer explains itself, so a reader of the record isn't confused.
func TestTheBasisSaysTheArgumentsWereCleaned(t *testing.T) {
	res := decide(readOnly, denyEtc, "read_file", map[string]any{"path": "//etc/passwd"}, nil)
	if !strings.Contains(res.Basis, "R-10 (matched after cleaning up") {
		t.Errorf("basis %q should say the arguments were cleaned", res.Basis)
	}
	if plain := decide(readOnly, denyEtc, "read_file", map[string]any{"path": "/etc/passwd"}, nil); strings.Contains(plain.Basis, "cleaning up") {
		t.Errorf("a plain match should not mention cleaning: %q", plain.Basis)
	}
}

// Rules written in upper case or with stray spaces still line up with arguments.
func TestRuleValuesAreCleanedToo(t *testing.T) {
	rules := []Rule{{ID: "R-1", Tool: "read_file", When: []Cond{{Field: "args.path", Op: "prefix", Value: "/ETC/ "}}, Action: ActionDeny}}
	if res := decide(readOnly, rules, "read_file", map[string]any{"path": "/etc/passwd"}, nil); res.Action != ActionDeny {
		t.Errorf("got %s, want deny", res.Action)
	}
}

func TestEveryTextOperatorIsCleaned(t *testing.T) {
	for _, tc := range []struct {
		name string
		cond Cond
		args map[string]any
	}{
		{"eq", Cond{Field: "args.text", Op: "eq", Value: "Needs-Approval"}, map[string]any{"text": " needs-approval "}},
		{"prefix", Cond{Field: "args.dir", Op: "prefix", Value: "/Var/Log/"}, map[string]any{"dir": "//var/./log/x"}},
		{"contains", Cond{Field: "args.cmd", Op: "contains", Value: "RM -RF"}, map[string]any{"cmd": "sudo rm    -rf /"}},
		{"in", Cond{Field: "args.env", Op: "in", Value: []any{"PROD", "Staging"}}, map[string]any{"env": "Prod "}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rules := []Rule{{ID: "R", Tool: "t", When: []Cond{tc.cond}, Action: ActionHuman}}
			// Without a rule for it, the call is allowed; the rule must hold it.
			allow := []Rule{{ID: "A", Tool: "t", Action: ActionAllow}}
			if res := decide(allow, rules, "t", tc.args, nil); res.Action != ActionHuman || res.Rule.ID != "R" {
				t.Errorf("got %s by %v, want human by R", res.Action, res.Rule)
			}
		})
	}
}

func TestKeyCaseDoesNotHideAnArgument(t *testing.T) {
	if res := decide(readOnly, denyEtc, "read_file", map[string]any{"Path": "/etc/passwd"}, nil); res.Action != ActionDeny {
		t.Errorf("got %s, want deny", res.Action)
	}
	nested := []Rule{{ID: "N", Tool: "read_file", When: []Cond{{Field: "args.File.Name", Op: "eq", Value: "secret"}}, Action: ActionDeny}}
	if res := decide(readOnly, nested, "read_file", map[string]any{"file": map[string]any{"NAME": "SECRET"}}, nil); res.Action != ActionDeny {
		t.Errorf("a nested key: got %s, want deny", res.Action)
	}
}

// Numbers are never cleaned, so numeric rules behave as before.
func TestNumbersAreUnchanged(t *testing.T) {
	rules := []Rule{{ID: "R", Tool: "pay", When: []Cond{{Field: "args.amount", Op: "gt", Value: float64(1000)}}, Action: ActionDeny}}
	allow := []Rule{{ID: "A", Tool: "pay", Action: ActionAllow}}
	for amount, want := range map[any]Action{float64(5000): ActionDeny, "5000": ActionDeny, " 5000": ActionDeny, float64(999): ActionAllow, "abc": ActionAllow} {
		if res := decide(allow, rules, "pay", map[string]any{"amount": amount}, nil); res.Action != want {
			t.Errorf("amount %v: got %s, want %s", amount, res.Action, want)
		}
	}
}

// The safety property: cleaning can make an answer stricter, never looser.
func TestCleaningNeverLoosensADecision(t *testing.T) {
	// As typed, R-1 denies. Cleaned, R-0 would allow. R-1's answer is stricter, so it stands.
	rules := []Rule{
		{ID: "R-1", Tool: "read_file", When: []Cond{{Field: "args.path", Op: "eq", Value: "/Etc/Secret"}}, Action: ActionDeny},
		{ID: "R-0", Tool: "read_file", When: []Cond{{Field: "args.path", Op: "eq", Value: "/etc/secret"}}, Action: ActionAllow},
	}
	if res := decide(nil, rules, "read_file", map[string]any{"path": "/Etc/Secret"}, nil); res.Action != ActionDeny || res.Rule.ID != "R-1" {
		t.Errorf("got %s by %v, want deny by R-1", res.Action, res.Rule)
	}
}

// A cleaned answer that is equally strict or weaker, or from the same rule, changes nothing.
func TestEquallyStrictOrWeakerSecondAnswerIsIgnored(t *testing.T) {
	agent := []Rule{{ID: "A-1", Tool: "list_files", When: []Cond{{Field: "args.dir", Op: "eq", Value: "/tmp"}}, Action: ActionAllow}}
	res := decide(readOnly, agent, "list_files", map[string]any{"dir": "/TMP"}, nil)
	if res.Action != ActionAllow || res.Rule.ID != "F-02" {
		t.Errorf("got %s by %v, want allow by F-02 (the first answer)", res.Action, res.Rule)
	}
}

func TestResultRuleStillPointsIntoTheCallersSlice(t *testing.T) {
	rules := denyEtc
	res := decide(readOnly, rules, "read_file", map[string]any{"path": "//etc/passwd"}, nil)
	if res.Rule != &rules[0] {
		t.Error("Result.Rule should point into the slice passed to Evaluate, as documented")
	}
}

// --- the judge ---

func judgeRule(id string, cond ...Cond) Rule {
	return Rule{ID: id, Tool: "label", When: cond, Action: ActionJudge}
}

var refund = Cond{Field: "args.text", Op: "contains", Value: "refund"}

// A judge rule the call only matches after cleaning goes to a person, and the judge is not asked.
func TestJudgeRuleReachedOnlyAfterCleaningGoesToAPerson(t *testing.T) {
	asked := 0
	judge := func(context.Context, Rule) (Action, error) { asked++; return ActionAllow, nil }
	res := decide(nil, []Rule{judgeRule("J-1", refund)}, "label", map[string]any{"text": "Please REFUND me"}, judge)
	// "REFUND" doesn't match as typed (no rule matched: a person decides) and
	// matches J-1 once cleaned, which is no stricter. Either way a person decides.
	if asked != 0 || res.Action != ActionHuman {
		t.Errorf("judge asked %d times, result %s; want 0 and human", asked, res.Action)
	}

	// With a rule below that would allow the call as typed, the cleaned-only
	// judge match is stricter (human beats allow), so a person decides.
	rules := []Rule{judgeRule("J-1", refund), {ID: "A-1", Tool: "label", Action: ActionAllow}}
	res = decide(nil, rules, "label", map[string]any{"text": "Please REFUND me"}, judge)
	if asked != 0 || res.Action != ActionHuman || res.Rule.ID != "J-1" {
		t.Errorf("judge asked %d times, result %s by %v; want 0 and human by J-1", asked, res.Action, res.Rule)
	}
}

// A judge rule matched as typed is asked once, and its real verdict stands
// even though the cleaned pass matches the same rule.
func TestJudgeIsAskedOnceAndItsVerdictStands(t *testing.T) {
	for _, verdict := range []Action{ActionAllow, ActionDeny, ActionHuman} {
		asked := 0
		judge := func(context.Context, Rule) (Action, error) { asked++; return verdict, nil }
		res := decide(nil, []Rule{judgeRule("J-1", refund)}, "label", map[string]any{"text": "please refund me"}, judge)
		if asked != 1 || res.Action != verdict {
			t.Errorf("verdict %s: judge asked %d times, result %s; want 1 and %s", verdict, asked, res.Action, verdict)
		}
	}
}

// A judge with no opinion lets the level carry on to its next rule; the
// cleaned pass must not turn that into "a person decides".
func TestAJudgeWithNoOpinionStillLetsTheNextRuleDecide(t *testing.T) {
	asked := 0
	judge := func(context.Context, Rule) (Action, error) { asked++; return "", nil }
	rules := []Rule{judgeRule("J-1", refund), {ID: "N-1", Tool: "label", Action: ActionNotify}}
	res := decide(nil, rules, "label", map[string]any{"text": "please refund me"}, judge)
	if asked != 1 || res.Action != ActionNotify || res.Rule.ID != "N-1" {
		t.Errorf("judge asked %d times, result %s by %v; want 1 and notify by N-1", asked, res.Action, res.Rule)
	}
}

// A judge that failed sends the call to a person once, not twice.
func TestAJudgeThatFailedIsNotRetriedOnTheCleanedCall(t *testing.T) {
	asked := 0
	judge := func(context.Context, Rule) (Action, error) { asked++; return "", context.DeadlineExceeded }
	res := decide(nil, []Rule{judgeRule("J-1", refund)}, "label", map[string]any{"text": "please refund me"}, judge)
	if asked != 1 || res.Action != ActionHuman {
		t.Errorf("judge asked %d times, result %s; want 1 and human", asked, res.Action)
	}
}

func TestNilJudgeStillSendsAMatchedJudgeRuleToAPerson(t *testing.T) {
	res := decide(nil, []Rule{judgeRule("J-1", refund)}, "label", map[string]any{"text": "please refund me"}, nil)
	if res.Action != ActionHuman || res.Rule == nil || res.Rule.ID != "J-1" {
		t.Errorf("got %s by %v, want human by J-1", res.Action, res.Rule)
	}
}
