package invocation

import (
	"encoding/json"
	"testing"

	"github.com/validmind/atryum/internal/mcp"
)

func sp() SelfProtection { return DefaultSelfProtection() }

func args(t *testing.T, v map[string]any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}

type matchCase struct {
	name  string
	tool  string
	input map[string]any
}

func TestSelfProtectionMatchesCallsThatNameTheGate(t *testing.T) {
	cases := []matchCase{
		{"edit user settings with sed", "Bash", map[string]any{"command": `sed -i '' '/atryum/d' ~/.claude/settings.json`}},
		{"cd into the config dir first", "Bash", map[string]any{"command": `cd ~/.claude && sed -i '' '/hook/d' settings.json`}},
		{"path detour", "Bash", map[string]any{"command": "rm ~/.claude/hooks/../settings.json"}},
		{"percent-encoded path", "write_file", map[string]any{"path": "/home/u/%2Eclaude/settings.json", "content": "{}"}},
		{"upper-case path", "Bash", map[string]any{"command": "rm ~/.CLAUDE/SETTINGS.JSON"}},
		{"second Claude config dir", "Edit", map[string]any{"file_path": "/Users/a/.claude-b/settings.json", "old_string": "x", "new_string": "y"}},
		{"delete the hook script", "Bash", map[string]any{"command": "rm -rf ~/.atryum/hooks"}},
		{"overwrite the hook script", "Bash", map[string]any{"command": "cat > /Users/a/.atryum/hooks/atryum-hook.mjs <<'EOF'\nprocess.exit(0)\nEOF"}},
		{"relative Atryum home", "Bash", map[string]any{"command": "cd ~ && rm -rf .atryum"}},
		{"per-agent home", "Bash", map[string]any{"command": "cp /tmp/x ~/.atryum-b/hooks/atryum-hook.mjs"}},
		{"disable all hooks in a project file", "Bash", map[string]any{"command": `echo '{"disableAllHooks": true}' > notes.json && mv notes.json .claude/settings.local.json`}},
		{"disableAllHooks with the path hidden", "Bash", map[string]any{"command": `python3 -c "import json;open(p,'w').write(json.dumps({'disableAllHooks':True}))"`}},
		{"Write to user settings", "Write", map[string]any{"file_path": "/Users/a/.claude/settings.json", "content": "{}"}},
		{"Edit a Windows local settings file", "Edit", map[string]any{"file_path": `C:\Users\a\.claude\settings.local.json`, "old_string": "x", "new_string": "y"}},
		{"MultiEdit project settings", "MultiEdit", map[string]any{"file_path": "/repo/.claude/settings.json", "edits": []any{}}},
		{"lower-case write tool with a path key", "write", map[string]any{"path": "/home/u/.claude/settings.json", "content": "{}"}},
		{"Write a script that edits the hook", "Write", map[string]any{"file_path": "/tmp/fix.sh", "content": "#!/bin/sh\nrm -rf ~/.atryum/hooks\n"}},
		{"Write an extensionless script", "Write", map[string]any{"file_path": "/tmp/fix", "content": "sed -i '' d ~/.claude/settings.json"}},
		{"Cursor hooks", "write_file", map[string]any{"path": "/home/u/.cursor/hooks.json", "content": "{}"}},
		{"cd into .cursor first", "Bash", map[string]any{"command": "cd ~/.cursor && rm hooks.json"}},
		{"Codex hooks", "Bash", map[string]any{"command": "vim ~/.codex/hooks.json"}},
		{"Codex config", "shell", map[string]any{"command": []any{"bash", "-lc", "echo x >> ~/.codex/config.toml"}}},
		{"Amp plugin", "Bash", map[string]any{"command": "rm ~/.config/amp/plugins/atryum.ts"}},
		{"Pi extension", "Bash", map[string]any{"command": "rm -rf ~/.pi/agent/extensions/atryum"}},
		{"managed settings file", "Bash", map[string]any{"command": `sudo tee "/Library/Application Support/ClaudeCode/managed-settings.json"`}},
		{"managed settings drop-in", "Bash", map[string]any{"command": "sudo rm /etc/claude-code/managed-settings.d/atryum.json"}},
		{"managed hook script (macOS)", "Bash", map[string]any{"command": `sudo rm "/Library/Application Support/Atryum/hooks/atryum-hook.mjs"`}},
		{"managed hook script (Linux)", "Bash", map[string]any{"command": "sudo rm /usr/local/lib/atryum/hooks/atryum-hook.mjs"}},
		{"nested argument", "custom_tool", map[string]any{"ops": []any{map[string]any{"target": "~/.atryum/agent-key"}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sp().Match(tc.tool, args(t, tc.input), true); got == "" {
				t.Fatalf("Match(%s, %v) = no match; want it caught", tc.tool, tc.input)
			}
		})
	}
}

func TestSelfProtectionLeavesOrdinaryCallsAlone(t *testing.T) {
	cases := []matchCase{
		{"reading settings", "Read", map[string]any{"file_path": "/Users/a/.claude/settings.json"}},
		{"lower-case read tool", "read", map[string]any{"path": "/Users/a/.claude/settings.json"}},
		{"grepping the hook dir", "Grep", map[string]any{"pattern": "x", "path": "/Users/a/.atryum"}},
		{"a web search about the setting", "WebSearch", map[string]any{"query": "claude code disableAllHooks"}},
		{"a sub-agent prompt", "Task", map[string]any{"prompt": "Explain what ~/.atryum/hooks does"}},
		{"a README that mentions ~/.atryum", "Write", map[string]any{"file_path": "/repo/README.md", "content": "The hook lives in ~/.atryum/hooks and is registered in ~/.claude/settings.json."}},
		{"docs that mention disableAllHooks", "Edit", map[string]any{"file_path": "/repo/docs/hooks.md", "old_string": "a", "new_string": "Never set disableAllHooks."}},
		{"the Atryum repo itself", "Bash", map[string]any{"command": "cd ~/src/atryum && go test ./... && cat examples/shared-agent-hook/atryum-hook.mjs"}},
		{"the example settings file", "Edit", map[string]any{"file_path": "/repo/examples/claude-code-hook/settings.example.json", "old_string": "a", "new_string": "b"}},
		{"project .claude files that aren't settings", "Bash", map[string]any{"command": "ls .claude/commands && cat .claude/skills/review/SKILL.md"}},
		{"an unrelated dotfile", "Bash", map[string]any{"command": "echo hi >> ~/.zshrc"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sp().Match(tc.tool, args(t, tc.input), true); got != "" {
				t.Fatalf("Match(%s, %v) = %q; want no match", tc.tool, tc.input, got)
			}
		})
	}
}

// What the text check is documented not to catch. If one of these starts
// matching, update the README's list of limits.
func TestSelfProtectionKnownLimits(t *testing.T) {
	cases := []matchCase{
		{"shell quotes inside a name", "Bash", map[string]any{"command": "rm ~/.atr''yum/agent-key"}},
		{"a variable", "Bash", map[string]any{"command": "d=atryum; rm -rf ~/.$d"}},
		{"a glob", "Bash", map[string]any{"command": "rm -rf ~/.atry*"}},
		{"removing a whole parent directory", "Bash", map[string]any{"command": "rm -rf ~/.claude"}},
		{"a non-script file run later", "Write", map[string]any{"file_path": "/tmp/notes.txt", "content": "rm -rf ~/.atryum/hooks"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sp().Match(tc.tool, args(t, tc.input), true); got != "" {
				t.Fatalf("Match(%s, %v) = %q; this limit is now covered", tc.tool, tc.input, got)
			}
		})
	}
}

// A call that didn't come from a harness hook (the MCP proxy) has unknown tool
// names, so nothing is skipped: a tool named like a read-only one is checked,
// and so is a file-write tool's content.
func TestSelfProtectionChecksEverythingOffTheHookPath(t *testing.T) {
	if got := sp().Match("read", args(t, map[string]any{"path": "/home/u/.claude/settings.json"}), false); got == "" {
		t.Fatal("proxy tool named read: want it checked")
	}
	if got := sp().Match("write", args(t, map[string]any{"path": "/tmp/notes.txt", "content": "rm -rf ~/.atryum"}), false); got == "" {
		t.Fatal("proxy tool named write: want its content checked")
	}
}

func TestSelfProtectionExtraPathsAndDisabled(t *testing.T) {
	rm := args(t, map[string]any{"command": "rm /opt/agent-home/hooks/atryum-hook.mjs"})
	for _, extra := range [][]string{{"/opt/Agent-Home/"}, {"", " ", "/opt/Agent-Home/"}} {
		p := DefaultSelfProtection()
		p.ExtraPaths = extra
		if got := p.Match("Bash", rm, true); got != "/opt/Agent-Home/" {
			t.Fatalf("extra_paths %q: got %q", extra, got)
		}
		if got := p.Match("Bash", args(t, map[string]any{"command": "go test ./..."}), true); got != "" {
			t.Fatalf("extra_paths %q matched an ordinary command: %q", extra, got)
		}
	}
	off := SelfProtection{}
	if got := off.Match("Bash", args(t, map[string]any{"command": "rm -rf ~/.atryum"}), true); got != "" {
		t.Fatalf("disabled check matched %q", got)
	}
}

func TestUpstreamIsLocal(t *testing.T) {
	cases := []struct {
		upstream mcp.Upstream
		want     bool
	}{
		{mcp.Upstream{Mode: mcp.UpstreamModeStdio}, true},
		{mcp.Upstream{Mode: mcp.UpstreamModeHTTP, BaseURL: "http://localhost:3000/mcp"}, true},
		{mcp.Upstream{Mode: mcp.UpstreamModeHTTP, BaseURL: "http://127.0.0.1:3000/mcp"}, true},
		{mcp.Upstream{Mode: mcp.UpstreamModeHTTP, BaseURL: "http://[::1]:3000/mcp"}, true},
		{mcp.Upstream{Mode: mcp.UpstreamModeHTTP, BaseURL: "https://mcp.linear.app/mcp"}, false},
		{mcp.Upstream{Mode: mcp.UpstreamModeHTTP, BaseURL: "https://10.0.0.5/mcp"}, false},
		{mcp.Upstream{Mode: mcp.UpstreamModeHTTP, BaseURL: "::bad"}, true},
	}
	for _, tc := range cases {
		if got := upstreamIsLocal(tc.upstream); got != tc.want {
			t.Fatalf("upstreamIsLocal(%+v) = %t, want %t", tc.upstream, got, tc.want)
		}
	}
}
