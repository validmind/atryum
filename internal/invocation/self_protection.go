package invocation

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/validmind/atryum/internal/mcp"
)

// Self-protection keeps an agent from changing the gate that governs it.
//
// The hooks that route an agent's tool calls through Atryum are registered in
// files the agent's own tools can usually reach: the hook script and agent key
// under ~/.atryum, and the harness's hook settings (Claude Code's
// settings.json, Cursor's and Codex's hooks.json). Approval rules match on
// server, tool and agent only, so without this check nothing deterministic
// stops a call such as "edit ~/.claude/settings.json" from switching the hook
// off; only an LLM judge or a person would.
//
// Every tool call is checked before plans, rules, policy or an external
// decider see it. A call whose arguments name one of those files is denied
// (or held for a person). While the hook is intact, a call that would change
// it passes through the hook and is caught here.
//
// The check reads text, so it can be evaded by a path the agent assembles at
// run time (a variable, a glob, an encoded command). It is a deterministic
// floor, not a sandbox: install the hook where the agent cannot write and run
// the agent in a sandbox that makes those files read-only.

// SelfProtectionAction is what happens to a call that targets the gate.
type SelfProtectionAction string

const (
	// SelfProtectionDeny refuses the call outright (the default).
	SelfProtectionDeny SelfProtectionAction = "deny"
	// SelfProtectionHuman holds the call for a person to approve.
	SelfProtectionHuman SelfProtectionAction = "human"
)

// SelfProtection configures the check. The zero value is disabled; use
// DefaultSelfProtection for the shipped behaviour.
type SelfProtection struct {
	Enabled bool
	Action  SelfProtectionAction
	// ExtraPaths are additional path fragments to protect, for example a
	// custom ATRYUM_HOME that isn't a dot-directory. Matched case-insensitively
	// as substrings of each argument, after the same cleanup as the built-in
	// patterns.
	ExtraPaths []string
}

// DefaultSelfProtection is enabled and denies.
func DefaultSelfProtection() SelfProtection {
	return SelfProtection{Enabled: true, Action: SelfProtectionDeny}
}

// readOnlyTools are harness tools that only read files. Reading the hook's
// settings doesn't weaken the gate, so these are not checked.
var readOnlyTools = map[string]bool{
	"Read": true, "Glob": true, "Grep": true, "LS": true,
}

// fileWriteTools are harness tools that write one file named by a path
// argument. Their content is checked only when the file is a script, so
// editing a document that merely mentions ~/.atryum is not blocked while
// writing a script that changes the hook (to run it later) still is.
var fileWriteTools = map[string][]string{
	"Write":        {"file_path"},
	"Edit":         {"file_path"},
	"MultiEdit":    {"file_path"},
	"NotebookEdit": {"notebook_path"},
}

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

type protectedPattern struct {
	label string
	re    *regexp.Regexp
}

// protectedPatterns match the cleaned-up (lower-cased, forward-slash) text of
// a tool argument. Each names the files a hook install keeps its gate in.
var protectedPatterns = []protectedPattern{
	// ~/.atryum (hook script, agent key, hook state) and per-agent homes such
	// as ~/.atryum-b.
	{"Atryum home (~/.atryum)", regexp.MustCompile(`(^|[^a-z0-9_])\.atryum`)},
	// Claude Code user, project and local settings, where the hook is
	// registered, and the system-wide managed settings file.
	{"Claude Code settings (.claude/settings*.json)", regexp.MustCompile(`\.claude/settings[a-z0-9._-]*\.json`)},
	{"Claude Code managed settings", regexp.MustCompile(`managed-settings\.json`)},
	// The Claude Code setting that turns every hook off.
	{"disableAllHooks", regexp.MustCompile(`disableallhooks`)},
	// Cursor and Codex hook registrations.
	{"Cursor hooks (.cursor/hooks.json)", regexp.MustCompile(`\.cursor/hooks\.json`)},
	{"Codex hooks (.codex/hooks.json)", regexp.MustCompile(`\.codex/hooks\.json`)},
	{"Codex config (.codex/config.toml)", regexp.MustCompile(`\.codex/config\.toml`)},
}

var repeatedSlashes = regexp.MustCompile(`/{2,}`)

// selfProtectionVariants returns the cleaned-up spellings of s that patterns
// are matched against: lower-cased, with shell quoting removed (so .atr”yum
// and ".atryum" read as .atryum), and with backslashes read both as path
// separators (Windows) and as shell escapes (.at\ryum).
func selfProtectionVariants(s string) []string {
	lower := strings.ToLower(s)
	unquoted := strings.NewReplacer(`'`, "", `"`, "", "`", "").Replace(lower)
	asSeparator := repeatedSlashes.ReplaceAllString(strings.ReplaceAll(unquoted, `\`, "/"), "/")
	asEscape := repeatedSlashes.ReplaceAllString(strings.ReplaceAll(unquoted, `\`, ""), "/")
	asSeparator = strings.ReplaceAll(asSeparator, "/./", "/")
	asEscape = strings.ReplaceAll(asEscape, "/./", "/")
	return []string{asSeparator, asEscape}
}

// Match reports which protected file a call targets, or "" when none. The
// tool's name and every string in its arguments (keys and values, at any
// depth) are checked.
func (p SelfProtection) Match(tool string, input json.RawMessage) string {
	if !p.Enabled || readOnlyTools[tool] {
		return ""
	}
	var texts []string
	var decoded any
	if len(input) > 0 && json.Unmarshal(input, &decoded) == nil {
		texts = selfProtectionTexts(tool, decoded)
	} else if len(input) > 0 {
		texts = append(texts, string(input))
	}
	texts = append(texts, tool)

	extras := make([]string, 0, len(p.ExtraPaths))
	for _, extra := range p.ExtraPaths {
		for _, v := range selfProtectionVariants(strings.TrimSpace(extra)) {
			if v != "" {
				extras = append(extras, v)
				break
			}
		}
	}

	for _, text := range texts {
		for _, v := range selfProtectionVariants(text) {
			for _, pat := range protectedPatterns {
				if pat.re.MatchString(v) {
					return pat.label
				}
			}
			for i, extra := range extras {
				if strings.Contains(v, extra) {
					return strings.TrimSpace(p.ExtraPaths[i])
				}
			}
		}
	}
	return ""
}

// selfProtectionTexts picks the argument text to check: only the path for a
// file-write tool writing a non-script file, everything otherwise.
func selfProtectionTexts(tool string, decoded any) []string {
	var texts []string
	if pathKeys, ok := fileWriteTools[tool]; ok {
		if args, ok := decoded.(map[string]any); ok {
			script := false
			for _, key := range pathKeys {
				if path, ok := args[key].(string); ok {
					texts = append(texts, path)
					if isScriptPath(path) {
						script = true
					}
				}
			}
			if len(texts) > 0 && !script {
				return texts
			}
			texts = texts[:0]
		}
	}
	collectStrings(decoded, &texts)
	return texts
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

// selfProtectionReason is the explanation stored on the invocation and shown
// to the agent.
func selfProtectionReason(target string, action SelfProtectionAction) string {
	verb := "blocked"
	if action == SelfProtectionHuman {
		verb = "held for a person"
	}
	return fmt.Sprintf("This call touches Atryum's own hook or hook settings (%s), so it was %s: an agent may not change the gate that reviews its tool calls. Make this change yourself, outside the agent.", target, verb)
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
	inv.Error = deniedError("Tool call denied: it would change Atryum's own hook.", reason, nil)
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
	inv.Error = deniedError("Tool call denied: it would change Atryum's own hook.", reason, nil)
	s.commitOrLog(context.WithoutCancel(ctx), inv, s.invocations.UpdateResult,
		Event{InvocationID: inv.InvocationID, EventType: "invocation.received", Payload: mustJSON(payload), CreatedAt: now},
		Event{InvocationID: inv.InvocationID, EventType: "invocation.denied", Payload: mustJSON(map[string]any{"self_protection": target, "reason": reason}), CreatedAt: completed})
	return s.toResponse(inv), nil
}
