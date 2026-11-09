package policy

import "testing"

// Holds is for an allowance on what a call says: the condition must hold
// for the arguments as sent and however cleaning respells them, so a path
// that climbs out of the allowed folder once resolved is not allowed, while
// one that stays inside is.
func TestHoldsResolvesPaths(t *testing.T) {
	underTmp := []Cond{c("args.path", "prefix", "/tmp/")}
	for p, want := range map[string]bool{
		"/tmp/build/out.log":                true,
		"/tmp/a/../b.log":                   true, // the detour stays inside
		"/tmp/./x":                          true,
		"/tmp//x":                           true,
		"/tmp/100%2Efile":                   true, // /tmp/100.file
		"/tmp/x/":                           true,
		"/tmp/../etc/passwd":                false, // /etc/passwd
		"/tmp/x/../../home/kam/.ssh/id_rsa": false,
		"/tmp/%2E%2E/etc/passwd":            false, // the dots escaped
		"/tmp/%252E%252E/etc/passwd":        false, // and escaped twice
		"/tmp/..":                           false, // / itself
		"/tmp/../":                          false,
		"/tmp/\u200b../etc/passwd":          false, // an invisible character in the dots
		"/tmp/\uff0e\uff0e/etc/passwd":      false, // full-width dots
		"/TMP/x":                            false, // never: as sent it isn't under /tmp/
		"//tmp/x":                           false,
		"/var/tmp/x":                        false,
		"/tmp/my dir/../../etc/passwd":      false,
		"/tmp/./../tmp/x":                   true, // leaves and comes back
	} {
		if got := Holds(underTmp, Call{Tool: "write_file", Args: map[string]any{"path": p}}); got != want {
			t.Errorf("prefix /tmp/ holds for %q: %v, want %v", p, got, want)
		}
	}
}

// Every operator reads the cleaned arguments, and so does a condition on a
// nested argument or on text that isn't a path; facts are read as they
// are; several conditions must all hold.
func TestHoldsOperators(t *testing.T) {
	for _, tc := range []struct {
		name string
		when []Cond
		args map[string]any
		want bool
	}{
		{"eq, plain", []Cond{c("args.path", "eq", "/tmp/out.log")}, map[string]any{"path": "/tmp/out.log"}, true},
		{"eq, a detour to the same file", []Cond{c("args.path", "eq", "/tmp/out.log")}, map[string]any{"path": "/tmp/x/../out.log"}, false},
		{"contains, a detour out", []Cond{c("args.path", "contains", "/tmp/")}, map[string]any{"path": "/a/tmp/../etc/passwd"}, false},
		{"contains, a detour in", []Cond{c("args.path", "contains", "/tmp/")}, map[string]any{"path": "/etc/../tmp/x"}, true},
		{"in", []Cond{c("args.path", "in", []any{"/tmp/a", "/tmp/b"})}, map[string]any{"path": "/tmp/a"}, true},
		{"in, a detour", []Cond{c("args.path", "in", []any{"/tmp/a", "/tmp/b"})}, map[string]any{"path": "/tmp/../tmp/a"}, false},
		{"lt", []Cond{c("args.amount", "lt", 1080)}, map[string]any{"amount": 900}, true},
		{"lt, over", []Cond{c("args.amount", "lt", 1080)}, map[string]any{"amount": 1200}, false},
		{"a command", []Cond{c("args.command", "prefix", "git ")}, map[string]any{"command": "git status"}, true},
		{"a command with a path detour", []Cond{c("args.command", "contains", "/tmp/")}, map[string]any{"command": "cat /tmp/../etc/passwd"}, false},
		{"a command that fails as sent", []Cond{c("args.command", "prefix", "git ")}, map[string]any{"command": "GIT status"}, false},
		{"nested", []Cond{c("args.file.path", "prefix", "/tmp/")}, map[string]any{"file": map[string]any{"path": "/tmp/x"}}, true},
		{"nested detour", []Cond{c("args.file.path", "prefix", "/tmp/")}, map[string]any{"file": map[string]any{"path": "/tmp/../etc/passwd"}}, false},
		{"a missing argument", []Cond{c("args.path", "prefix", "/tmp/")}, map[string]any{"other": "/tmp/x"}, false},
		{"two conditions, both hold", []Cond{c("args.path", "prefix", "/tmp/"), c("args.mode", "eq", "w")}, map[string]any{"path": "/tmp/x", "mode": "w"}, true},
		{"two conditions, one fails", []Cond{c("args.path", "prefix", "/tmp/"), c("args.mode", "eq", "w")}, map[string]any{"path": "/tmp/x", "mode": "a"}, false},
		{"no conditions", nil, map[string]any{"path": "/etc/passwd"}, true},
	} {
		if got := Holds(tc.when, Call{Tool: "t", Args: tc.args}); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
	secret := []Cond{c("facts.contains_secret", "eq", false)}
	if !Holds(secret, Call{Tool: "t", Facts: map[string]any{"contains_secret": false}}) || Holds(secret, Call{Tool: "t", Facts: map[string]any{"contains_secret": true}}) {
		t.Error("a fact is read as it is")
	}
}

// Holds never allows what Evaluate would deny for the same condition: a
// deny on the condition that fires in any pass means Holds is true, and
// Holds true means the deny fires.
func TestHoldsAgreesWithEvaluate(t *testing.T) {
	when := []Cond{c("args.path", "prefix", "/tmp/")}
	deny := []Rule{{ID: "D", Tool: "*", When: when, Action: ActionDeny}, {ID: "A", Tool: "*", Action: ActionAllow}}
	for _, p := range []string{"/tmp/x", "/tmp/../etc/passwd", "/TMP/x", "/tmp/%2E%2E/etc/passwd", "//tmp/x", "/tmp/a/../b", "/etc/passwd"} {
		call := Call{Tool: "write_file", Args: map[string]any{"path": p}}
		holds := Holds(when, call)
		denied := decide(deny, nil, "write_file", call.Args, nil).Action == ActionDeny
		if holds && !denied {
			t.Errorf("%q: Holds allows what Evaluate would not deny", p)
		}
	}
}
