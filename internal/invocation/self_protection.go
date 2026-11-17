package invocation

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/validmind/atryum/internal/mcp"
	argrules "github.com/validmind/atryum/pkg/policy"
)

// Self-protection keeps an agent from changing the gate that governs it.
//
// The hooks that route an agent's tool calls through Atryum are registered in
// files the agent's own tools can usually reach: the hook script and agent key
// under ~/.atryum, and the harness's hook settings (Claude Code's
// settings.json, Cursor's and Codex's hooks.json, the Amp plugin, the Pi
// extension). Approval rules match on server, tool and agent only, so without
// this check nothing deterministic stops a call such as "edit
// ~/.claude/settings.json" from switching the hook off; only an LLM judge or
// a person would.
//
// Every tool call is checked before plans, rules, policy or an external
// decider see it. The protected files are a table of pkg/policy rules
// (protectedTargets), and each piece of argument text is decided by
// policy.Evaluate, so the text is compared the way that package compares
// everything: as sent, then cleaned up (case, spaces, %XX escapes, invisible
// characters), then with paths tidied (detours, Windows spellings). A call
// that names a protected file is held for a person, or denied outright.
//
// The check reads text. It does not interpret shell: quoting, escapes,
// variables, globs, encoded commands, or a command that removes a whole
// parent directory can get past it. It is a deterministic floor, not a
// sandbox: install the hook where the agent cannot write and run the agent
// in a sandbox that makes those files read-only.

// SelfProtectionAction is what happens to a call that targets the gate.
type SelfProtectionAction string

const (
	// SelfProtectionHuman holds the call for a person to approve or deny
	// (the default): the agent can't change its gate on its own say-so, but
	// a person can let an intended change through. It is only as strong as
	// the review UI's sign-in; where an agent could reach an open operator
	// API and approve its own call, use SelfProtectionDeny.
	SelfProtectionHuman SelfProtectionAction = "human"
	// SelfProtectionDeny refuses the call outright.
	SelfProtectionDeny SelfProtectionAction = "deny"
)

// SelfProtection configures the check. The zero value is disabled; use
// DefaultSelfProtection for the shipped behaviour.
type SelfProtection struct {
	Enabled bool
	Action  SelfProtectionAction
	// ExtraPaths are additional path fragments to protect, for example a
	// custom ATRYUM_HOME or CLAUDE_CONFIG_DIR. A call is caught when one of
	// its arguments contains the fragment. Blank entries are ignored.
	ExtraPaths []string
}

// DefaultSelfProtection is enabled and holds calls for a person.
func DefaultSelfProtection() SelfProtection {
	return SelfProtection{Enabled: true, Action: SelfProtectionHuman}
}

// readOnlyTools are harness tools that change nothing on disk: they read, or
// they only carry text (a search, a to-do list, a sub-agent's prompt, whose
// own tool calls are gated separately). Calls to them are not checked. Names
// are compared in lower case.
var readOnlyTools = map[string]bool{
	"read": true, "glob": true, "grep": true, "ls": true, "notebookread": true,
	"websearch": true, "webfetch": true, "todowrite": true, "todoread": true,
	"task": true, "agent": true,
}

// fileWriteTools are harness tools that write one file named by a path
// argument. Their content is checked only when the file is a script, so
// editing a document that merely mentions ~/.atryum is not blocked while
// writing a script that changes the hook (to run it later) still is.
var fileWriteTools = map[string]bool{
	"write": true, "edit": true, "multiedit": true, "notebookedit": true,
}

var fileWritePathKeys = []string{"file_path", "path", "notebook_path"}

var scriptExtensions = map[string]bool{
	"": true, ".sh": true, ".bash": true, ".zsh": true, ".fish": true, ".ps1": true,
	".bat": true, ".cmd": true, ".py": true, ".rb": true, ".pl": true, ".php": true,
	".js": true, ".mjs": true, ".cjs": true, ".ts": true, ".mts": true, ".cts": true,
	".command": true, ".lua": true, ".ipynb": true,
}

func isScriptPath(path string) bool {
	path = strings.ReplaceAll(strings.TrimSpace(path), `\`, "/")
	base := path[strings.LastIndex(path, "/")+1:]
	ext := ""
	if i := strings.LastIndex(base, "."); i > 0 {
		ext = strings.ToLower(base[i:])
	}
	return scriptExtensions[ext]
}

// protectedTarget is one gate file (or family of files). A piece of argument
// text names it when every fragment in one of its alternatives appears in
// that text.
type protectedTarget struct {
	label string
	any   [][]string
}

// protectedTargets lists the files a hook install keeps its gate in.
var protectedTargets = []protectedTarget{
	// Hook script, agent key and hook state; per-agent homes such as ~/.atryum-b.
	{"Atryum home (~/.atryum)", [][]string{{".atryum"}}},
	// Claude Code user, project and local settings, where the hook is
	// registered, in any config directory named .claude* (CLAUDE_CONFIG_DIR).
	// The fragments are matched separately so "cd ~/.claude && sed ...
	// settings.json" is caught too.
	{"Claude Code settings (.claude*/settings*.json)", [][]string{{".claude", "settings", ".json"}}},
	// Claude Code's managed settings, its drop-in directory, and the
	// root-owned directories a managed install writes.
	{"Claude Code managed settings", [][]string{{"managed-settings"}, {"/etc/claude-code"}, {"application support/claudecode"}}},
	{"Atryum managed hook script", [][]string{{"application support/atryum"}, {"/usr/local/lib/atryum"}}},
	// The Claude Code setting that turns every hook off.
	{"disableAllHooks", [][]string{{"disableallhooks"}}},
	{"Cursor hooks (.cursor/hooks.json)", [][]string{{".cursor", "hooks.json"}}},
	{"Codex hooks (.codex/hooks.json, config.toml)", [][]string{{".codex", "hooks.json"}, {".codex", "config.toml"}}},
	{"Amp plugins (~/.config/amp/plugins)", [][]string{{"/amp/plugins"}}},
	{"Pi extensions (~/.pi/agent/extensions)", [][]string{{".pi/agent/extensions"}}},
}

// selfProtectionField is the argument name each piece of text is given when
// it is handed to policy.Evaluate.
const selfProtectionField = "args.text"

func targetRules(targets []protectedTarget) []argrules.Rule {
	var rules []argrules.Rule
	for _, target := range targets {
		for _, fragments := range target.any {
			rule := argrules.Rule{ID: "self-protection", Tool: "*", Action: argrules.ActionDeny, Note: target.label}
			for _, fragment := range fragments {
				rule.When = append(rule.When, argrules.Cond{Field: selfProtectionField, Op: "contains", Value: fragment})
			}
			rules = append(rules, rule)
		}
	}
	return rules
}

var builtinSelfProtectionRules = targetRules(protectedTargets)

// rules returns the built-in rules plus one per extra path.
func (p SelfProtection) rules() []argrules.Rule {
	rules := builtinSelfProtectionRules
	for _, extra := range p.ExtraPaths {
		if extra = strings.TrimSpace(extra); extra != "" {
			rules = append(rules[:len(rules):len(rules)], targetRules([]protectedTarget{{extra, [][]string{{extra}}}})...)
		}
	}
	return rules
}

// Match reports which protected file a call names, or "" when none. harness
// says the call came from an agent harness's hook, whose tool names are
// known: its read-only tools are skipped and its file-write tools are checked
// on their path (plus content, for a script). Any other call has every string
// in its arguments checked, keys included.
func (p SelfProtection) Match(tool string, input json.RawMessage, harness bool) string {
	if !p.Enabled || len(input) == 0 {
		return ""
	}
	name := strings.ToLower(tool)
	if harness && readOnlyTools[name] {
		return ""
	}
	var texts []string
	var decoded any
	switch {
	case json.Unmarshal(input, &decoded) != nil:
		texts = []string{string(input)}
	case harness && fileWriteTools[name]:
		texts = fileWriteTexts(decoded)
	}
	if texts == nil {
		collectStrings(decoded, &texts)
	}

	rules := p.rules()
	for _, text := range texts {
		call := argrules.Call{Tool: tool, Args: map[string]any{"text": text}}
		if res := argrules.Evaluate(context.Background(), rules, nil, call, nil); res.Rule != nil {
			return res.Rule.Note
		}
	}
	return ""
}

// fileWriteTexts returns just the path arguments of a file-write call that
// writes a non-script file, or nil when everything should be checked (no
// path found, or the file is a script).
func fileWriteTexts(decoded any) []string {
	args, ok := decoded.(map[string]any)
	if !ok {
		return nil
	}
	var paths []string
	for _, key := range fileWritePathKeys {
		if path, ok := args[key].(string); ok {
			if isScriptPath(path) {
				return nil
			}
			paths = append(paths, path)
		}
	}
	return paths
}

func collectStrings(v any, out *[]string) {
	switch t := v.(type) {
	case string:
		*out = append(*out, t)
	case []any:
		for _, item := range t {
			collectStrings(item, out)
		}
	case map[string]any:
		for k, item := range t {
			*out = append(*out, k)
			collectStrings(item, out)
		}
	}
}

// upstreamIsLocal reports whether an MCP upstream runs on this machine: a
// stdio server, or an HTTP server on a loopback address. Only those can reach
// local hook files, so proxy calls to remote servers are not checked (an
// issue tracker call that mentions ~/.atryum changes nothing here).
func upstreamIsLocal(upstream mcp.Upstream) bool {
	if upstream.Mode != mcp.UpstreamModeHTTP {
		return true
	}
	u, err := url.Parse(strings.TrimSpace(upstream.BaseURL))
	if err != nil || u.Hostname() == "" {
		return true // unknown: check it
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// selfProtectionReason is the explanation stored on the invocation and shown
// to the reviewer and the agent.
func selfProtectionReason(target string, action SelfProtectionAction) string {
	if action == SelfProtectionHuman {
		return fmt.Sprintf("This call names Atryum's own hook files or hook settings (%s). An agent may not change the gate that reviews its tool calls on its own, so a person has to approve it.", target)
	}
	return fmt.Sprintf("This call names Atryum's own hook files or hook settings (%s), so it was blocked: an agent may not change the gate that reviews its tool calls. If the change is intended, make it yourself, outside the agent.", target)
}

// selfProtectProxy settles an MCP-proxy call that targets the gate: denied,
// or held for a person before it can execute.
func (s *Service) selfProtectProxy(ctx context.Context, inv Invocation, upstream mcp.Upstream, req CreateInvocationRequest, target string, now time.Time) (InvocationResponse, error) {
	action := s.selfProtection.Action
	reason := selfProtectionReason(target, action)
	payload := map[string]any{
		"tool": req.Tool, "upstream": upstream.Name, "request_id": req.RequestID,
		"input": json.RawMessage(inv.Input), "arguments": json.RawMessage(inv.Input),
		"disposition_reason": reason, "self_protection": target,
	}
	if inv.AgentID != nil {
		payload["agent_id"] = *inv.AgentID
	}
	if action == SelfProtectionHuman {
		payload["disposition"] = "self_protection_escalated"
		inv.Approval = newApproval("self_protection_escalated", reason, nil)
		if err := s.commit(ctx, inv, s.invocations.UpdateResult, Event{InvocationID: inv.InvocationID, EventType: "invocation.received", Payload: mustJSON(payload), CreatedAt: now}); err != nil {
			return InvocationResponse{}, err
		}
		return s.waitForHumanApproval(ctx, inv, upstream, req)
	}
	payload["disposition"] = "self_protection_denied"
	completed := time.Now().UTC()
	inv.Status = StatusDenied
	inv.CompletedAt = &completed
	inv.Approval = newApproval("self_protection_denied", reason, nil)
	inv.Error = deniedError("Tool call denied: it names Atryum's own hook files or settings.", reason, nil)
	s.commitOrLog(context.WithoutCancel(ctx), inv, s.invocations.UpdateResult,
		Event{InvocationID: inv.InvocationID, EventType: "invocation.received", Payload: mustJSON(payload), CreatedAt: now},
		Event{InvocationID: inv.InvocationID, EventType: "invocation.denied", Payload: mustJSON(map[string]any{"self_protection": target, "reason": reason}), CreatedAt: completed})
	return s.toResponse(inv), nil
}

// selfProtectExternal settles a hook-submitted call that targets the gate:
// denied, or left pending for a person.
func (s *Service) selfProtectExternal(ctx context.Context, inv Invocation, source, agentID string, requestID *string, target string, now time.Time) (InvocationResponse, error) {
	action := s.selfProtection.Action
	reason := selfProtectionReason(target, action)
	payload := map[string]any{
		"tool": inv.Tool, "upstream": source, "request_id": requestID,
		"input": json.RawMessage(inv.Input), "arguments": json.RawMessage(inv.Input),
		"external": true, "disposition_reason": reason, "self_protection": target,
	}
	if agentID != "" {
		payload["agent_id"] = agentID
	}
	if action == SelfProtectionHuman {
		payload["disposition"] = "self_protection_escalated"
		inv.Status = StatusPendingApproval
		inv.Approval = newApproval("self_protection_escalated", reason, nil)
		if err := s.commit(ctx, inv, s.invocations.UpdateResult,
			Event{InvocationID: inv.InvocationID, EventType: "invocation.received", Payload: mustJSON(payload), CreatedAt: now},
			Event{InvocationID: inv.InvocationID, EventType: "invocation.pending_approval", Payload: mustJSON(payload), CreatedAt: time.Now().UTC()}); err != nil {
			return InvocationResponse{}, err
		}
		s.summarizePendingApproval(ctx, inv.InvocationID)
		return s.toResponse(inv), nil
	}
	payload["disposition"] = "self_protection_denied"
	completed := time.Now().UTC()
	inv.Status = StatusDenied
	inv.CompletedAt = &completed
	inv.Approval = newApproval("self_protection_denied", reason, nil)
	inv.Error = deniedError("Tool call denied: it names Atryum's own hook files or settings.", reason, nil)
	s.commitOrLog(context.WithoutCancel(ctx), inv, s.invocations.UpdateResult,
		Event{InvocationID: inv.InvocationID, EventType: "invocation.received", Payload: mustJSON(payload), CreatedAt: now},
		Event{InvocationID: inv.InvocationID, EventType: "invocation.denied", Payload: mustJSON(map[string]any{"self_protection": target, "reason": reason}), CreatedAt: completed})
	return s.toResponse(inv), nil
}
