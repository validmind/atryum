package policy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// The fixtures below port the AA MVP prototype (prototypes/agent-authority-mvp
// data.js: FIRM_RULES, each charter's published rules, and REPLAY_SHAPES with
// their recorded decisions), so the Go engine and the prototype agree. The
// prototype's flat field names become "args.<name>", except the firm rules'
// derived values, which become "facts.<name>".

func c(field, op string, value any) Cond { return Cond{Field: field, Op: op, Value: value} }

var firmRules = []Rule{
	{ID: "F-01", Tool: "*", When: []Cond{c("facts.contains_secret", "eq", true)}, Action: ActionDeny, Note: "Secret literal in arguments (2.3)"},
	{ID: "F-02", Tool: "*audit*|*monitoring*", Action: ActionDeny, Note: "Tampering with audit or monitoring (2.1)"},
	{ID: "F-03", Tool: "*iam*|*rbac*|*grant*", Action: ActionHuman, Note: "Identity and access change (2.2)"},
	{ID: "F-04", Tool: "*", When: []Cond{c("facts.data_egress", "eq", "external")}, Action: ActionDeny, Note: "Customer data leaving the bank (3.2)"},
}

var k8sV4 = []Rule{
	{ID: "K-01", Tool: "pods_list", Action: ActionAllow, Note: "Read pods"},
	{ID: "K-02", Tool: "pod_logs|pod_describe", Action: ActionAllow, Note: "Read logs and pod detail"},
	{ID: "K-03", Tool: "pod_edit", When: []Cond{c("args.patch_scope", "eq", "metadata")}, Action: ActionJudge, Note: "Labels and annotations: judge against the ticket"},
	{ID: "K-04", Tool: "pod_edit", Action: ActionHuman, Note: "Any other pod change"},
	{ID: "K-05", Tool: "pod_exec", When: []Cond{c("args.privileged", "eq", true)}, Action: ActionDeny, Note: "Privileged exec or host namespaces"},
	{ID: "K-06", Tool: "pod_exec", Action: ActionJudge, Note: "Non-privileged exec"},
	{ID: "K-07", Tool: "pod_restart", When: []Cond{c("args.namespace", "prefix", "retail-prod-")}, Action: ActionHuman, Note: "Restart in production"},
	{ID: "K-08", Tool: "pod_restart", Action: ActionNotify, Note: "Restart outside production: run, then notify the owner"},
	{ID: "K-09", Tool: "pod_delete", Action: ActionJudge, Note: "Pod delete"},
	{ID: "K-10", Tool: "deployment_scale", Action: ActionJudge, Note: "Scale a deployment"},
}

var k8sV5 = append(append([]Rule{}, k8sV4[:8]...),
	Rule{ID: "K-09", Tool: "pod_delete", When: []Cond{c("args.namespace", "prefix", "retail-prod-")}, Action: ActionHuman, Note: "Pod delete in production (new)"},
	Rule{ID: "K-10", Tool: "pod_delete", Action: ActionJudge, Note: "Pod delete outside production"},
	Rule{ID: "K-11", Tool: "deployment_scale", When: []Cond{c("args.namespace", "prefix", "retail-prod-"), c("args.replicas_to", "eq", 0)}, Action: ActionDeny, Note: "Scale production to zero (new)"},
	Rule{ID: "K-12", Tool: "deployment_scale", When: []Cond{c("args.namespace", "prefix", "retail-prod-"), c("args.scale_factor", "gt", 2)}, Action: ActionHuman, Note: "Scale production beyond 2x (new)"},
	Rule{ID: "K-13", Tool: "deployment_scale", Action: ActionJudge, Note: "Scale a deployment"},
)

var agentRules = map[string][]Rule{
	"k8s-ops": k8sV4,
	"claude-code": {
		{ID: "C-01", Tool: "Read|Grep|Glob", Action: ActionAllow, Note: "Read the repository"},
		{ID: "C-02", Tool: "Edit|Write", Action: ActionAllow, Note: "Edit the repository"},
		{ID: "C-03", Tool: "Bash", When: []Cond{c("args.command", "contains", "--force")}, Action: ActionDeny, Note: "Force push"},
		{ID: "C-04", Tool: "Bash", When: []Cond{c("args.command", "prefix", "git push origin feature/")}, Action: ActionAllow, Note: "Push a feature branch"},
		{ID: "C-05", Tool: "Bash", When: []Cond{c("args.command", "prefix", "git push origin main")}, Action: ActionHuman, Note: "Push to main"},
		{ID: "C-06", Tool: "Bash|WebFetch", Action: ActionJudge, Note: "Other shell and web calls"},
	},
	"codex": {
		{ID: "X-01", Tool: "shell", When: []Cond{c("args.command", "contains", "env/prod")}, Action: ActionHuman, Note: "Terraform against production"},
		{ID: "X-02", Tool: "apply_patch|read_file", Action: ActionAllow, Note: "Edit the repository"},
		{ID: "X-03", Tool: "shell", When: []Cond{c("args.command", "prefix", "cargo test")}, Action: ActionAllow, Note: "Run tests"},
		{ID: "X-04", Tool: "shell", When: []Cond{c("args.command", "contains", "destroy")}, Action: ActionDeny, Note: "Destroy"},
		{ID: "X-05", Tool: "shell", Action: ActionJudge, Note: "Other shell commands"},
	},
	"ansible-cve": {
		{ID: "A-01", Tool: "ansible.inventory_*|ansible.job_status|ansible.host_facts", Action: ActionAllow, Note: "Reads and facts"},
		{ID: "A-02", Tool: "plan.submit", Action: ActionJudge, Note: "Remediation plans"},
		{ID: "A-03", Tool: "ansible.job_template_launch", When: []Cond{c("args.template", "contains", "major")}, Action: ActionHuman, Note: "Major upgrades"},
		{ID: "A-04", Tool: "ansible.job_template_launch", Action: ActionJudge, Note: "Run a job template"},
		{ID: "A-05", Tool: "ansible.*delete*", Action: ActionDeny, Note: "Delete anything"},
	},
	"cma-recon": {
		{ID: "R-01", Tool: "ledger.read_*|ledger.match_transactions", Action: ActionAllow, Note: "Reads and matching"},
		{ID: "R-02", Tool: "ledger.post_journal_adjustment", When: []Cond{c("args.credit_account", "prefix", "4")}, Action: ActionHuman, Note: "Credits to income accounts"},
		{ID: "R-03", Tool: "ledger.post_journal_adjustment", Action: ActionJudge, Note: "Other adjustments"},
	},
	"data-insights": {
		{ID: "D-01", Tool: "run_query", Action: ActionJudge, Note: "Queries"},
		{ID: "D-02", Tool: "read_account", Action: ActionJudge, Note: "Single-account lookups"},
		{ID: "D-03", Tool: "export_accounts", Action: ActionJudge, Note: "Exports, with session context"},
		{ID: "D-04", Tool: "update_account|delete_*", Action: ActionDeny, Note: "Writes to customer records"},
	},
	"ap-payments": {
		{ID: "P-01", Tool: "erp.vendor_lookup|erp.invoice_get|erp.po_get", Action: ActionAllow, Note: "Lookups"},
		{ID: "P-02", Tool: "payments.create", When: []Cond{c("args.amount", "gt", 10000)}, Action: ActionHuman, Note: "Payments above $10,000"},
		{ID: "P-03", Tool: "payments.create", Action: ActionJudge, Note: "Payments up to $10,000"},
		{ID: "P-04", Tool: "erp.vendor_update_bank", Action: ActionDeny, Note: "Vendor bank details"},
	},
	"treasury-fx": {{ID: "T-01", Tool: "fx.*", Action: ActionHuman, Note: "All FX instructions"}},
}

// shape is one REPLAY_SHAPES entry: judge is what the judge says ("" means it
// fails, which the prototype also sends to a person), want the recorded decision.
type shape struct {
	tool  string
	args  string // JSON
	facts map[string]any
	judge Action
	want  Action
}

var replayShapes = map[string][]shape{
	"k8s-ops": {
		{tool: "pods_list", args: `{"namespace":"retail-prod-*"}`, want: "allow"},
		{tool: "pod_logs", args: `{"namespace":"retail-prod-*"}`, want: "allow"},
		{tool: "pod_describe", args: `{"namespace":"retail-prod-*"}`, want: "allow"},
		{tool: "pod_edit", args: `{"namespace":"retail-prod-*","patch_scope":"metadata"}`, judge: "allow", want: "allow"},
		{tool: "pod_restart", args: `{"namespace":"retail-prod-payments"}`, want: "human"},
		{tool: "pod_restart", args: `{"namespace":"retail-staging-web"}`, want: "notify"},
		{tool: "pod_delete", args: `{"namespace":"retail-staging-web"}`, judge: "allow", want: "allow"},
		{tool: "deployment_scale", args: `{"namespace":"retail-prod-cards","replicas_to":6,"scale_factor":1.5}`, judge: "allow", want: "allow"},
		{tool: "pod_exec", args: `{"namespace":"retail-prod-payments","privileged":true}`, want: "deny"},
		{tool: "pod_exec", args: `{"namespace":"retail-prod-*","privileged":false}`, judge: "allow", want: "allow"},
		{tool: "pod_delete", args: `{"namespace":"retail-prod-payments"}`, judge: "allow", want: "allow"},
		{tool: "deployment_scale", args: `{"namespace":"retail-prod-cards","replicas_to":8,"scale_factor":2.67}`, judge: "allow", want: "allow"},
		{tool: "deployment_scale", args: `{"namespace":"retail-prod-cards","replicas_to":0,"scale_factor":0}`, judge: "allow", want: "allow"},
	},
	"claude-code": {
		{tool: "Read", args: `{}`, want: "allow"},
		{tool: "Edit", args: `{}`, want: "allow"},
		{tool: "Bash", args: `{"command":"npm test"}`, judge: "allow", want: "allow"},
		{tool: "Bash", args: `{"command":"git push origin feature/PAY-2291"}`, want: "allow"},
		{tool: "Bash", args: `{"command":"git push --force origin main"}`, want: "deny"},
		{tool: "WebFetch", args: `{"url":"pastebin.com"}`, judge: "deny", want: "deny"},
	},
	"codex": {
		{tool: "apply_patch", args: `{}`, want: "allow"},
		{tool: "shell", args: `{"command":"cargo test -p core-ledger"}`, want: "allow"},
		{tool: "shell", args: `{"command":"terraform plan -var-file=env/staging.tfvars"}`, judge: "allow", want: "allow"},
		{tool: "shell", args: `{"command":"terraform apply -var-file=env/prod.tfvars"}`, want: "human"},
	},
	"ansible-cve": {
		{tool: "ansible.inventory_hosts", args: `{}`, want: "allow"},
		{tool: "plan.submit", args: `{}`, judge: "allow", want: "allow"},
		{tool: "ansible.job_template_launch", args: `{"template":"Patch python (minor)"}`, judge: "allow", want: "allow"},
		{tool: "ansible.job_template_launch", args: `{"template":"Patch python (major)"}`, want: "human"},
	},
	"cma-recon": {
		{tool: "ledger.read_statement", args: `{}`, want: "allow"},
		{tool: "ledger.match_transactions", args: `{}`, want: "allow"},
		{tool: "ledger.post_journal_adjustment", args: `{"credit_account":"4410"}`, want: "human"},
		{tool: "ledger.post_journal_adjustment", args: `{"credit_account":"1190"}`, judge: "allow", want: "allow"},
	},
	"data-insights": {
		{tool: "run_query", args: `{}`, judge: "allow", want: "allow"},
		{tool: "read_account", args: `{}`, judge: "allow", want: "allow"},
		{tool: "export_accounts", args: `{}`, judge: "deny", want: "deny"},
		{tool: "export_accounts", args: `{}`, facts: map[string]any{"data_egress": "internal"}, judge: "allow", want: "allow"},
	},
	"ap-payments": {
		{tool: "erp.invoice_get", args: `{}`, want: "allow"},
		{tool: "payments.create", args: `{"amount":4200}`, judge: "allow", want: "allow"},
		{tool: "payments.create", args: `{"amount":18500}`, want: "human"},
	},
}

func (s shape) call() Call {
	var args map[string]any
	if err := json.Unmarshal([]byte(s.args), &args); err != nil {
		panic(err)
	}
	return Call{Tool: s.tool, Args: args, Facts: s.facts}
}

func (s shape) judgeFn(context.Context, Rule) (Verdict, error) {
	if s.judge == "" {
		return Verdict{}, errors.New("judge unavailable")
	}
	return Verdict{Action: s.judge}, nil
}

func TestReplayShapesMatchPrototype(t *testing.T) {
	for agent, shapes := range replayShapes {
		for _, s := range shapes {
			got := Evaluate(context.Background(), firmRules, agentRules[agent], s.call(), s.judgeFn)
			if got.Action != s.want {
				t.Errorf("%s %s %s: got %s (%s), want %s", agent, s.tool, s.args, got.Action, got.Basis, s.want)
			}
		}
	}
}

// Charter v5 (draft) is what the shadow replay compares against v4: the
// production deletes and scales the judge allowed flip to a person or deny.
func TestK8sDraftFlips(t *testing.T) {
	allow := func(context.Context, Rule) (Verdict, error) { return Verdict{Action: ActionAllow}, nil }
	for _, tc := range []struct {
		tool, args string
		want       Action
		rule       string
	}{
		{"pod_delete", `{"namespace":"retail-prod-payments"}`, ActionHuman, "K-09"},
		{"pod_delete", `{"namespace":"retail-staging-web"}`, ActionAllow, "K-10"},
		{"deployment_scale", `{"namespace":"retail-prod-cards","replicas_to":0,"scale_factor":0}`, ActionDeny, "K-11"},
		{"deployment_scale", `{"namespace":"retail-prod-cards","replicas_to":8,"scale_factor":2.67}`, ActionHuman, "K-12"},
		{"deployment_scale", `{"namespace":"retail-prod-cards","replicas_to":6,"scale_factor":1.5}`, ActionAllow, "K-13"},
	} {
		got := Evaluate(context.Background(), firmRules, k8sV5, shape{tool: tc.tool, args: tc.args}.call(), allow)
		if got.Action != tc.want || got.Rule == nil || got.Rule.ID != tc.rule || got.Level != "agent" {
			t.Errorf("%s %s: got %s by %+v at %q, want %s by %s", tc.tool, tc.args, got.Action, got.Rule, got.Level, tc.want, tc.rule)
		}
	}
}

func TestEvaluate(t *testing.T) {
	ctx := context.Background()
	verdict := func(v Action) Judge {
		return func(context.Context, Rule) (Verdict, error) { return Verdict{Action: v}, nil }
	}
	for _, tc := range []struct {
		name         string
		firm, agent  []Rule
		call         Call
		judge        Judge
		want         Action
		rule, level  string
		basisContain string
	}{
		{name: "no rule matches: a person decides", agent: k8sV4, call: Call{Tool: "unknown"}, want: ActionHuman, basisContain: "No rule matched"},
		{name: "firm deny beats agent allow", firm: firmRules, agent: k8sV4, call: Call{Tool: "pods_list", Facts: map[string]any{"contains_secret": true}}, want: ActionDeny, rule: "F-01", level: "firm", basisContain: "F-01 Secret literal"},
		{name: "agent human beats firm allow",
			firm:  []Rule{{ID: "F", Tool: "*", Action: ActionAllow}},
			agent: []Rule{{ID: "A", Tool: "pay", Action: ActionHuman}},
			call:  Call{Tool: "pay"}, want: ActionHuman, rule: "A", level: "agent"},
		{name: "tie keeps the firm rule",
			firm:  []Rule{{ID: "F", Tool: "*", Action: ActionNotify}},
			agent: []Rule{{ID: "A", Tool: "*", Action: ActionNotify}},
			call:  Call{Tool: "x"}, want: ActionNotify, rule: "F", level: "firm"},
		{name: "notify beats allow",
			firm:  []Rule{{ID: "F", Tool: "*", Action: ActionAllow}},
			agent: []Rule{{ID: "A", Tool: "*", Action: ActionNotify}},
			call:  Call{Tool: "x"}, want: ActionNotify, rule: "A", level: "agent"},
		{name: "first match within a level",
			agent: []Rule{{ID: "1", Tool: "x", Action: ActionAllow}, {ID: "2", Tool: "x", Action: ActionDeny}},
			call:  Call{Tool: "x"}, want: ActionAllow, rule: "1"},
		{name: "judge verdict and basis",
			agent: []Rule{{ID: "J", Tool: "x", Action: ActionJudge, Note: "judged"}},
			call:  Call{Tool: "x"}, judge: verdict(ActionDeny), want: ActionDeny, rule: "J", basisContain: "J judged → judge said deny"},
		{name: "judge error: a person decides",
			agent: []Rule{{ID: "J", Tool: "x", Action: ActionJudge}, {ID: "next", Tool: "x", Action: ActionAllow}},
			call:  Call{Tool: "x"}, judge: func(context.Context, Rule) (Verdict, error) { return Verdict{}, errors.New("down") }, want: ActionHuman, rule: "J"},
		{name: "nil judge: a person decides",
			agent: []Rule{{ID: "J", Tool: "x", Action: ActionJudge}, {ID: "next", Tool: "x", Action: ActionAllow}},
			call:  Call{Tool: "x"}, want: ActionHuman, rule: "J"},
		{name: "unknown verdict keeps scanning the level",
			agent: []Rule{{ID: "J", Tool: "x", Action: ActionJudge}, {ID: "next", Tool: "x", Action: ActionNotify}},
			call:  Call{Tool: "x"}, judge: verdict(""), want: ActionNotify, rule: "next"},
		{name: "judge saying notify is unknown: a level that only deferred is human",
			agent: []Rule{{ID: "J", Tool: "x", Action: ActionJudge}},
			call:  Call{Tool: "x"}, judge: verdict(ActionNotify), want: ActionHuman, rule: "J", level: "agent", basisContain: "no verdict"},
		{name: "a deferring firm judge does not hand the call to the agent level",
			firm:  []Rule{{ID: "FJ", Tool: "x", Action: ActionJudge}, {ID: "F-other", Tool: "y", Action: ActionAllow}},
			agent: []Rule{{ID: "A", Tool: "x", Action: ActionAllow}},
			call:  Call{Tool: "x"}, judge: verdict(""), want: ActionHuman, rule: "FJ", level: "firm"},
		{name: "a firm deny still beats a deferring agent judge",
			firm:  []Rule{{ID: "F", Tool: "x", Action: ActionDeny}},
			agent: []Rule{{ID: "AJ", Tool: "x", Action: ActionJudge}},
			call:  Call{Tool: "x"}, judge: verdict(""), want: ActionDeny, rule: "F", level: "firm"},
		{name: "unknown rule action fails safe",
			agent: []Rule{{ID: "B", Tool: "x", Action: "maybe"}},
			call:  Call{Tool: "x"}, want: ActionHuman, rule: "B"},
		{name: "server glob",
			agent: []Rule{{ID: "S", Tool: "*", Server: "bank|ledger-*", Action: ActionDeny}, {ID: "else", Tool: "*", Action: ActionAllow}},
			call:  Call{Tool: "x", Server: "ledger-eu"}, want: ActionDeny, rule: "S"},
		{name: "server glob misses",
			agent: []Rule{{ID: "S", Tool: "*", Server: "bank", Action: ActionDeny}, {ID: "else", Tool: "*", Action: ActionAllow}},
			call:  Call{Tool: "x", Server: "bank-2"}, want: ActionAllow, rule: "else"},
		{name: "glob is literal apart from *",
			agent: []Rule{{ID: "G", Tool: "a.b", Action: ActionDeny}, {ID: "else", Tool: "*", Action: ActionAllow}},
			call:  Call{Tool: "axb"}, want: ActionAllow, rule: "else"},
		{name: "all conditions must hold",
			agent: []Rule{{ID: "C", Tool: "*", When: []Cond{c("args.a", "eq", "1"), c("args.b", "eq", "2")}, Action: ActionDeny}},
			call:  Call{Tool: "x", Args: map[string]any{"a": "1", "b": "3"}}, want: ActionHuman, basisContain: "No rule matched"},
		{name: "dotted path and in",
			agent: []Rule{{ID: "P", Tool: "*", When: []Cond{c("args.target.env", "in", []any{"prod", "dr"})}, Action: ActionDeny}},
			call:  Call{Tool: "x", Args: map[string]any{"target": map[string]any{"env": "dr"}}}, want: ActionDeny, rule: "P"},
		{name: "lt compares numbers given as text",
			agent: []Rule{{ID: "L", Tool: "*", When: []Cond{c("args.amount", "lt", 100)}, Action: ActionAllow}},
			call:  Call{Tool: "x", Args: map[string]any{"amount": "99.5"}}, want: ActionAllow, rule: "L"},
		{name: "missing field matches nothing",
			agent: []Rule{{ID: "M", Tool: "*", When: []Cond{c("args.nope", "eq", "<nil>")}, Action: ActionAllow}},
			call:  Call{Tool: "x"}, want: ActionHuman, basisContain: "No rule matched"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Evaluate(ctx, tc.firm, tc.agent, tc.call, tc.judge)
			ruleID := ""
			if got.Rule != nil {
				ruleID = got.Rule.ID
			}
			if got.Action != tc.want || ruleID != tc.rule || (tc.level != "" && got.Level != tc.level) || !strings.Contains(got.Basis, tc.basisContain) {
				t.Fatalf("got %s rule=%q level=%q basis=%q; want %s rule=%q level=%q basis~%q", got.Action, ruleID, got.Level, got.Basis, tc.want, tc.rule, tc.level, tc.basisContain)
			}
		})
	}
}

// Numbers compare by value whatever their Go type, and their text has no
// exponent, so a rule on 1000000 matches 1e6 decoded from JSON.
func TestNumberCoercion(t *testing.T) {
	type myInt int16
	for _, tc := range []struct {
		name string
		cond Cond
		arg  any
		want bool
	}{
		{"eq million from JSON", c("args.v", "eq", 1000000), jsonNumber(`1000000`), true},
		{"eq million float", c("args.v", "eq", float64(1000000)), float64(1e6), true},
		{"eq 2.5e7", c("args.v", "eq", "25000000"), 2.5e7, true},
		{"eq small", c("args.v", "eq", "0.00001"), 0.00001, true},
		{"prefix on amount", c("args.v", "prefix", "1000"), float64(1000000), true},
		{"in amounts", c("args.v", "in", []any{float64(5e6), float64(1e6)}), float64(1e6), true},
		{"in typed slice", c("args.v", "in", []int{1, 1000000}), 1000000, true},
		{"in typed strings", c("args.v", "in", []string{"a", "b"}), "b", true},
		{"in not a list", c("args.v", "in", "b"), "b", false},
		{"eq amount given as string", c("args.v", "eq", 1000000), "1000000", true},
		{"gt amount given as string", c("args.v", "gt", 10000), "18500", true},
		{"gt exponent string", c("args.v", "gt", 999999), "1e6", true},
		{"gt json.Number", c("args.v", "gt", 10000), json.Number("10000.5"), true},
		{"lt uint8", c("args.v", "lt", 300), uint8(255), true},
		{"gt named int", c("args.v", "gt", myInt(1)), myInt(2), true},
		{"gt int64 vs float", c("args.v", "gt", 2.5), int64(3), true},
		{"bool is not a number", c("args.v", "gt", 0), true, false},
		{"bool eq text", c("args.v", "eq", "true"), true, true},
		{"word is not a number", c("args.v", "lt", 5), "five", false},
	} {
		got := condOK(tc.cond, Call{Args: map[string]any{"v": tc.arg}})
		if got != tc.want {
			t.Errorf("%s: condOK(%+v, %v) = %v, want %v", tc.name, tc.cond, tc.arg, got, tc.want)
		}
	}
}

func jsonNumber(s string) any {
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		panic(err)
	}
	return v
}

// The glob cache stays bounded however many distinct patterns it sees.
func TestGlobCacheBounded(t *testing.T) {
	for i := 0; i < 3*maxGlobs; i++ {
		if !glob(fmt.Sprintf("tool_%d|other", i), "other") {
			t.Fatal("glob stopped matching")
		}
	}
	n := 0
	globs.Range(func(any, any) bool { n++; return true })
	if n > maxGlobs {
		t.Fatalf("cache holds %d patterns, want at most %d", n, maxGlobs)
	}
}

func TestRuleJSON(t *testing.T) {
	var r Rule
	if err := json.Unmarshal([]byte(`{"id":"K-11","tool":"deployment_scale","when":[{"field":"args.replicas_to","op":"eq","value":0}],"action":"deny","never":true}`), &r); err != nil {
		t.Fatal(err)
	}
	got := Evaluate(context.Background(), nil, []Rule{r}, shape{tool: "deployment_scale", args: `{"replicas_to":0}`}.call(), nil)
	if got.Action != ActionDeny || !got.Rule.Never {
		t.Fatalf("got %+v", got)
	}
}

// F-01 denies every call with facts.contains_secret, so both misses and
// false alarms matter.
func TestContainsSecret(t *testing.T) {
	if !ContainsSecret(k8sSecretWithData()) {
		t.Error("a Secret manifest with a literal password was not flagged")
	}
	for _, tc := range []struct {
		name string
		args any
		want bool
	}{
		// Secrets.
		{"aws key id", map[string]any{"command": "aws s3 ls --access-key AKIAIOSFODNN7EXAMPLE"}, true},
		{"rsa private key", map[string]any{"body": "-----BEGIN RSA PRIVATE KEY-----\nMIIEow..."}, true},
		{"openssh private key", map[string]any{"body": "-----BEGIN OPENSSH PRIVATE KEY-----"}, true},
		{"pgp private key", map[string]any{"body": "-----BEGIN PGP PRIVATE KEY BLOCK-----"}, true},
		{"bearer header", map[string]any{"headers": map[string]any{"Authorization": "Bearer abcdefghijklmnopqrstuvwxyz012345"}}, true},
		{"bare jwt", map[string]any{"cookie": "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U"}, true},
		{"flag assignment", map[string]any{"command": "mysql -u root --password=hunter22"}, true},
		{"json key", map[string]any{"password": "hunter22"}, true},
		{"yaml in text", map[string]any{"config": "api_key: sk_live_abcdef12"}, true},
		{"env var with prefix", map[string]any{"command": "DB_PASSWORD=s3cr3tP4ss ./run.sh"}, true},
		{"openai env", map[string]any{"command": "export OPENAI_API_KEY=abcd1234efgh5678"}, true},
		{"aws secret key name", map[string]any{"aws_secret_access_key": "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"}, true},
		{"github token", map[string]any{"token": "ghp_" + strings.Repeat("a", 36)}, true},
		{"github fine-grained token", map[string]any{"t": "github_pat_" + strings.Repeat("A", 30)}, true},
		{"anthropic key", map[string]any{"text": "use sk-ant-api03-" + strings.Repeat("x", 30)}, true},
		{"url credentials", map[string]any{"dsn": "postgresql://app:Tr0ub4dor@db.internal:5432/ledger"}, true},
		{"nested list", map[string]any{"steps": []any{map[string]any{"env": map[string]any{"REFRESH_TOKEN": "8f14e45fceea167a5a36dedd4bea2543"}}}}, true},
		{"camelCase secret key", map[string]any{"clientSecret": "9b2f0c7e4a1d"}, true},
		{"bare token key", map[string]any{"token": "8f14e45fceea167a5a36dedd4bea2543"}, true},
		{"space-separated aws key", map[string]any{"command": "aws configure set aws_secret_access_key wJalrXUtnFEMI/K7MDENG"}, true},
		{"space-separated flag", map[string]any{"command": "vault login --password hunter2222"}, true},
		{"mysql -p", map[string]any{"command": "mysql -u root -ps3cr3t ledger"}, true},
		{"curl -u", map[string]any{"command": "curl -u admin:Tr0ub4dor https://ci.internal/api"}, true},
		// Not secrets.
		{"feature branch push", map[string]any{"command": "git push origin feature/PAY-2291"}, false},
		{"sql on a password column", map[string]any{"query": "select * from users where password_reset = true"}, false},
		{"prose", map[string]any{"text": "password reset requested for the account"}, false},
		{"pwd", map[string]any{"pwd": "/home/agent"}, false},
		{"boolean value", map[string]any{"require_password": true, "password": "true"}, false},
		{"null value", map[string]any{"api_key": nil}, false},
		{"mask", map[string]any{"password": "********"}, false},
		{"partial mask", map[string]any{"command": "login --password=ab****yz"}, false},
		{"placeholder", map[string]any{"command": "DB_PASSWORD=${DB_PASSWORD} ./run.sh"}, false},
		{"env reference", map[string]any{"api_key": "$OPENAI_API_KEY"}, false},
		{"template", map[string]any{"secret": "{{ vault.ledger }}"}, false},
		{"short value", map[string]any{"password": "abc"}, false},
		{"secret name", map[string]any{"secret_name": "ledger-db-credentials"}, false},
		{"k8s secretName", map[string]any{"secretName": "tls-cert-retail-prod"}, false},
		{"k8s secretKeyRef", map[string]any{"secretKeyRef": map[string]any{"name": "ledger-db", "key": "password"}}, false},
		{"k8s secretRef", map[string]any{"secretRef": map[string]any{"name": "ledger-db-env"}}, false},
		{"helm existingSecret", map[string]any{"existingSecret": "ledger-db-credentials"}, false},
		{"prefixed secret name", map[string]any{"tlsSecretName": "retail-prod-wildcard"}, false},
		{"secretary", map[string]any{"secretary": "Jane Wolfenden"}, false},
		{"plural secrets list", map[string]any{"secrets": []any{"ledger-db-credentials"}}, false},
		{"page token", map[string]any{"nextPageToken": "CiAKGjBpNDd2Nmp2Zml2cXRwYjBpOXA3"}, false},
		{"prose with secret", map[string]any{"text": "the secret ingredient is patience"}, false},
		{"mysql password from env", map[string]any{"command": "mysql -u root -p$MYSQL_PWD ledger"}, false},
		{"k8s deployment", k8sDeployment(), false},
		{"lower-case akia is not an aws key", map[string]any{"slug": "akiaiosfodnn7example"}, false},
		{"url without password", map[string]any{"url": "https://user@example.com/path"}, false},
		{"url placeholder password", map[string]any{"dsn": "postgres://app:${PGPASSWORD}@db/ledger"}, false},
		{"nil", nil, false},
		// Uninspectable input fails closed.
		{"unmarshalable", map[string]any{"f": func() {}}, true},
	} {
		if got := ContainsSecret(tc.args); got != tc.want {
			t.Errorf("%s: ContainsSecret(%v) = %v, want %v", tc.name, tc.args, got, tc.want)
		}
	}
}

// k8sDeployment is a realistic manifest that references secrets by name, as
// the k8s-ops agent patches them, and must never trip F-01.
func k8sDeployment() any {
	return jsonNumber(`{
	  "apiVersion": "apps/v1", "kind": "Deployment",
	  "metadata": {"name": "ledger-sync", "namespace": "retail-prod-payments",
	    "annotations": {"secret.reloader.stakater.com/reload": "ledger-db-credentials"}},
	  "spec": {"replicas": 3, "template": {"spec": {
	    "serviceAccountName": "ledger-sync",
	    "imagePullSecrets": [{"name": "registry-pull-secret"}],
	    "containers": [{
	      "name": "ledger-sync", "image": "registry.internal/ledger-sync:4.2.1",
	      "env": [
	        {"name": "DB_PASSWORD", "valueFrom": {"secretKeyRef": {"name": "ledger-db-credentials", "key": "password"}}},
	        {"name": "API_TOKEN", "valueFrom": {"secretKeyRef": {"name": "ledger-api", "key": "token", "optional": false}}},
	        {"name": "LOG_LEVEL", "value": "info"}
	      ],
	      "envFrom": [{"secretRef": {"name": "ledger-sync-env"}}],
	      "volumeMounts": [{"name": "tls", "mountPath": "/etc/tls", "readOnly": true}]
	    }],
	    "volumes": [{"name": "tls", "secret": {"secretName": "retail-prod-wildcard-tls", "defaultMode": 420}}]
	  }}}
	}`)
}

// k8sSecretWithData holds a password literal in stringData, which is a secret.
func k8sSecretWithData() any {
	return jsonNumber(`{"apiVersion": "v1", "kind": "Secret", "metadata": {"name": "ledger-db-credentials"},
	  "type": "Opaque", "stringData": {"username": "ledger", "password": "Tr0ub4dor&3xyz"}}`)
}

// A judge's account of its decision reaches the caller, which renders it
// for the agent whose call was judged (sc-19113). Before this, Evaluate
// kept the verdict's action and dropped the rest, so a denied agent was
// told only "→ judge said deny".
func TestEvaluateCarriesTheJudgesAccount(t *testing.T) {
	rules := []Rule{{ID: "J-01", Tool: "*", Action: ActionJudge, Note: "Evaluate charter"}}
	full := Verdict{
		Action:    ActionDeny,
		Reasoning: "the charter lets this agent read the ledger, never write it",
		Clauses:   []string{"3.1", "3.4"},
		Evidence:  []Evidence{{Quote: "UPDATE ledger SET", Why: "writes a system of record"}},
	}
	got := Evaluate(context.Background(), rules, nil, Call{Tool: "sql"},
		func(context.Context, Rule) (Verdict, error) { return full, nil })

	if got.Action != ActionDeny {
		t.Fatalf("action: got %s, want %s", got.Action, ActionDeny)
	}
	if got.Judge == nil {
		t.Fatal("Judge is nil: the account was dropped")
	}
	if !reflect.DeepEqual(*got.Judge, full) {
		t.Errorf("account: got %+v, want %+v", *got.Judge, full)
	}
	if got.JudgeErr != nil {
		t.Errorf("JudgeErr: got %v, want nil", got.JudgeErr)
	}
	// The basis text is unchanged, so records and the console keep reading
	// what they read before.
	if want := "J-01 Evaluate charter → judge said deny"; got.Basis != want {
		t.Errorf("basis: got %q, want %q", got.Basis, want)
	}
}

// A judge that cannot answer leaves the error on the result, so callers
// stop parsing it back out of the basis text.
func TestEvaluateCarriesTheJudgesError(t *testing.T) {
	rules := []Rule{{ID: "J-01", Tool: "*", Action: ActionJudge}}
	down := errors.New("evaluator timed out")
	got := Evaluate(context.Background(), rules, nil, Call{Tool: "sql"},
		func(context.Context, Rule) (Verdict, error) { return Verdict{}, down })

	if got.Action != ActionHuman {
		t.Errorf("action: got %s, want %s", got.Action, ActionHuman)
	}
	if !errors.Is(got.JudgeErr, down) {
		t.Errorf("JudgeErr: got %v, want %v", got.JudgeErr, down)
	}
	if got.Judge != nil {
		t.Errorf("Judge: got %+v, want nil", got.Judge)
	}
}

// A result no judge rule decided carries no account at all.
func TestEvaluateLeavesTheAccountEmptyWithoutAJudgeRule(t *testing.T) {
	rules := []Rule{{ID: "D-01", Tool: "*", Action: ActionDeny, Note: "never"}}
	got := Evaluate(context.Background(), rules, nil, Call{Tool: "sql"}, nil)
	if got.Judge != nil || got.JudgeErr != nil {
		t.Errorf("got Judge=%+v JudgeErr=%v, want both nil", got.Judge, got.JudgeErr)
	}
}
