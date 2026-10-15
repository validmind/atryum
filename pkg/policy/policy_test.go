package policy

import (
	"context"
	"encoding/json"
	"errors"
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

func (s shape) judgeFn(context.Context, Rule) (Action, error) {
	if s.judge == "" {
		return "", errors.New("judge unavailable")
	}
	return s.judge, nil
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
	allow := func(context.Context, Rule) (Action, error) { return ActionAllow, nil }
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
	verdict := func(v Action) Judge { return func(context.Context, Rule) (Action, error) { return v, nil } }
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
			call:  Call{Tool: "x"}, judge: func(context.Context, Rule) (Action, error) { return "", errors.New("down") }, want: ActionHuman, rule: "J"},
		{name: "nil judge: a person decides",
			agent: []Rule{{ID: "J", Tool: "x", Action: ActionJudge}, {ID: "next", Tool: "x", Action: ActionAllow}},
			call:  Call{Tool: "x"}, want: ActionHuman, rule: "J"},
		{name: "unknown verdict keeps scanning the level",
			agent: []Rule{{ID: "J", Tool: "x", Action: ActionJudge}, {ID: "next", Tool: "x", Action: ActionNotify}},
			call:  Call{Tool: "x"}, judge: verdict(""), want: ActionNotify, rule: "next"},
		{name: "judge saying notify is unknown",
			agent: []Rule{{ID: "J", Tool: "x", Action: ActionJudge}},
			call:  Call{Tool: "x"}, judge: verdict(ActionNotify), want: ActionHuman, basisContain: "No rule matched"},
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

func TestContainsSecret(t *testing.T) {
	for _, tc := range []struct {
		args any
		want bool
	}{
		{map[string]any{"command": "aws s3 ls --access-key AKIAIOSFODNN7EXAMPLE"}, true},
		{map[string]any{"body": "-----BEGIN RSA PRIVATE KEY-----\nMIIEow..."}, true},
		{map[string]any{"body": "-----BEGIN OPENSSH PRIVATE KEY-----"}, true},
		{map[string]any{"headers": map[string]any{"Authorization": "Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.abc"}}, true},
		{map[string]any{"command": "mysql -u root --password=hunter22"}, true},
		{map[string]any{"password": "hunter22"}, true},
		{map[string]any{"config": "api_key: sk_live_abcdef"}, true},
		{map[string]any{"token": "ghp_" + strings.Repeat("a", 36)}, true},
		{map[string]any{"command": "git push origin feature/PAY-2291"}, false},
		{map[string]any{"query": "select * from users where password_reset = true"}, false},
		{map[string]any{"text": "password reset requested for the account"}, false},
		{map[string]any{"pwd": "/home/agent"}, false},
		{nil, false},
	} {
		if got := ContainsSecret(tc.args); got != tc.want {
			t.Errorf("ContainsSecret(%v) = %v, want %v", tc.args, got, tc.want)
		}
	}
}
