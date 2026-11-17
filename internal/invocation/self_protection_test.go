package invocation

import (
	"encoding/json"
	"testing"
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

func TestSelfProtectionMatchesCallsThatChangeTheGate(t *testing.T) {
	cases := []struct {
		name  string
		tool  string
		input map[string]any
	}{
		{"edit user settings with sed", "Bash", map[string]any{"command": `sed -i '' '/atryum/d' ~/.claude/settings.json`}},
		{"delete the hook script", "Bash", map[string]any{"command": "rm -rf ~/.atryum/hooks"}},
		{"overwrite the hook script", "Bash", map[string]any{"command": "cat > /Users/a/.atryum/hooks/atryum-hook.mjs <<'EOF'\nprocess.exit(0)\nEOF"}},
		{"disable all hooks in a project file", "Bash", map[string]any{"command": `echo '{"disableAllHooks": true}' > notes.json && mv notes.json .claude/settings.local.json`}},
		{"disableAllHooks with the path hidden", "Bash", map[string]any{"command": `python3 -c "import json;open(p,'w').write(json.dumps({'disableAllHooks':True}))"`}},
		{"shell quotes inside the name", "Bash", map[string]any{"command": "rm ~/.atr''yum/agent-key"}},
		{"double-quoted segment", "Bash", map[string]any{"command": `rm ~/".atryum"/hooks/atryum-hook.mjs`}},
		{"backslash escape", "Bash", map[string]any{"command": `rm ~/.at\ryum/hooks/atryum-hook.mjs`}},
		{"per-agent home", "Bash", map[string]any{"command": "cp /tmp/x ~/.atryum-b/hooks/atryum-hook.mjs"}},
		{"Write to user settings", "Write", map[string]any{"file_path": "/Users/a/.claude/settings.json", "content": "{}"}},
		{"Edit a Windows local settings file", "Edit", map[string]any{"file_path": `C:\Users\a\.claude\settings.local.json`, "old_string": "x", "new_string": "y"}},
		{"MultiEdit project settings", "MultiEdit", map[string]any{"file_path": "/repo/.claude/settings.json", "edits": []any{}}},
		{"Write a script that edits the hook", "Write", map[string]any{"file_path": "/tmp/fix.sh", "content": "#!/bin/sh\nrm -rf ~/.atryum/hooks\n"}},
		{"Write an extensionless script", "Write", map[string]any{"file_path": "/tmp/fix", "content": "sed -i '' d ~/.claude/settings.json"}},
		{"MCP filesystem tool", "write_file", map[string]any{"path": "/home/u/.cursor/hooks.json", "content": "{}"}},
		{"Codex hooks", "Bash", map[string]any{"command": "vim ~/.codex/hooks.json"}},
		{"Codex config", "shell", map[string]any{"command": []any{"bash", "-lc", "echo x >> ~/.codex/config.toml"}}},
		{"managed settings drop-in", "Bash", map[string]any{"command": `sudo tee "/Library/Application Support/ClaudeCode/managed-settings.json"`}},
		{"nested argument", "custom_tool", map[string]any{"ops": []any{map[string]any{"target": "~/.atryum/agent-key"}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sp().Match(tc.tool, args(t, tc.input)); got == "" {
				t.Fatalf("Match(%s, %v) = no match; want it caught", tc.tool, tc.input)
			}
		})
	}
}

func TestSelfProtectionLeavesOrdinaryCallsAlone(t *testing.T) {
	cases := []struct {
		name  string
		tool  string
		input map[string]any
	}{
		{"reading settings", "Read", map[string]any{"file_path": "/Users/a/.claude/settings.json"}},
		{"grepping the hook dir", "Grep", map[string]any{"pattern": "x", "path": "/Users/a/.atryum"}},
		{"a README that mentions ~/.atryum", "Write", map[string]any{"file_path": "/repo/README.md", "content": "The hook lives in ~/.atryum/hooks and is registered in ~/.claude/settings.json."}},
		{"docs that mention disableAllHooks", "Edit", map[string]any{"file_path": "/repo/docs/hooks.md", "old_string": "a", "new_string": "Never set disableAllHooks."}},
		{"the Atryum repo itself", "Bash", map[string]any{"command": "cd ~/src/atryum && go test ./... && cat examples/shared-agent-hook/atryum-hook.mjs"}},
		{"the example settings file", "Edit", map[string]any{"file_path": "/repo/examples/claude-code-hook/settings.example.json", "old_string": "a", "new_string": "b"}},
		{"an unrelated dotfile", "Bash", map[string]any{"command": "echo hi >> ~/.zshrc"}},
		{"a word ending in .atryum", "Bash", map[string]any{"command": "ls foo.atryum"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sp().Match(tc.tool, args(t, tc.input)); got != "" {
				t.Fatalf("Match(%s, %v) = %q; want no match", tc.tool, tc.input, got)
			}
		})
	}
}

func TestSelfProtectionExtraPathsAndDisabled(t *testing.T) {
	p := DefaultSelfProtection()
	p.ExtraPaths = []string{"/opt/Agent-Home/"}
	if got := p.Match("Bash", args(t, map[string]any{"command": "rm /opt/agent-home/hooks/atryum-hook.mjs"})); got != "/opt/Agent-Home/" {
		t.Fatalf("extra path: got %q", got)
	}
	off := SelfProtection{}
	if got := off.Match("Bash", args(t, map[string]any{"command": "rm -rf ~/.atryum"})); got != "" {
		t.Fatalf("disabled check matched %q", got)
	}
}
