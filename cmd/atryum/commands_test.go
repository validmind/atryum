package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGlobalUsageIncludesCommands(t *testing.T) {
	usage := globalUsage()
	for _, expected := range []string{"run", "setup", "hooks", "licenses"} {
		if !strings.Contains(usage, expected) {
			t.Fatalf("usage missing %q: %s", expected, usage)
		}
	}
}

func TestRunLicensesReturnsNoError(t *testing.T) {
	if err := runLicenses(); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestRunUsageMentionsConfigFlag(t *testing.T) {
	usage := runUsage()
	if !strings.Contains(usage, "--config") {
		t.Fatalf("run usage missing --config: %s", usage)
	}
	if !strings.Contains(usage, "--init-servers") {
		t.Fatalf("run usage missing --init-servers: %s", usage)
	}
}

func TestBuildDemoConfigIncludesCalcUpstream(t *testing.T) {
	cfg := buildDemoConfig("/tmp/atryum.db")
	if !strings.Contains(cfg, "[[upstreams]]") {
		t.Fatalf("expected upstream block in demo config:\n%s", cfg)
	}
	if !strings.Contains(cfg, `name = "calc"`) {
		t.Fatalf("expected calc upstream in demo config:\n%s", cfg)
	}
	if !strings.Contains(cfg, `@coo-quack/calc-mcp@latest`) {
		t.Fatalf("expected calc package in demo config:\n%s", cfg)
	}
}

func TestRunSetupHelpReturnsNoError(t *testing.T) {
	if err := runSetup([]string{"--help"}); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestRunHooksHelpReturnsNoError(t *testing.T) {
	if err := runHooks([]string{"--help"}); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestRunHooksRequiresActionAndTarget(t *testing.T) {
	err := runHooks(nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "missing action and target") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestUpsertBackendCredentialsCreatesSection(t *testing.T) {
	updated := upsertBackendCredentials("[server]\nlisten_addr = \":8080\"\n", "https://vm.example", "k1", "s1")
	if !strings.Contains(updated, "[backend]") {
		t.Fatalf("expected backend section, got:\n%s", updated)
	}
	if !strings.Contains(updated, `base_url = "https://vm.example"`) {
		t.Fatalf("expected base_url, got:\n%s", updated)
	}
	if !strings.Contains(updated, `api_key = "k1"`) {
		t.Fatalf("expected api_key, got:\n%s", updated)
	}
	if !strings.Contains(updated, `api_secret = "s1"`) {
		t.Fatalf("expected api_secret, got:\n%s", updated)
	}
}

func TestUpsertBackendCredentialsUpdatesExistingKeys(t *testing.T) {
	input := "[backend]\nbase_url = \"\"\napi_key = \"old\"\napi_secret = \"old\"\n"
	updated := upsertBackendCredentials(input, "https://new.example", "new-k", "new-s")
	if !strings.Contains(updated, `base_url = "https://new.example"`) {
		t.Fatalf("expected updated base_url, got:\n%s", updated)
	}
	if !strings.Contains(updated, `api_key = "new-k"`) {
		t.Fatalf("expected updated api_key, got:\n%s", updated)
	}
	if !strings.Contains(updated, `api_secret = "new-s"`) {
		t.Fatalf("expected updated api_secret, got:\n%s", updated)
	}
	if strings.Contains(updated, `api_key = "old"`) {
		t.Fatalf("old api_key still present:\n%s", updated)
	}
}

func TestResolveStartupConfigPathPrefersCWD(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "atryum.toml"), []byte("[server]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(originalWD) }()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	path, err := resolveStartupConfigPath("")
	if err != nil {
		t.Fatal(err)
	}
	if path != "./atryum.toml" {
		t.Fatalf("expected ./atryum.toml, got %s", path)
	}
}

func TestAddCalcUpstreamAppendsWhenMissing(t *testing.T) {
	input := "[server]\nlisten_addr = \":8080\"\n"
	updated, added := addCalcUpstream(input)
	if !added {
		t.Fatal("expected calc upstream to be added")
	}
	if !strings.Contains(updated, "[[upstreams]]") || !strings.Contains(updated, `name = "calc"`) {
		t.Fatalf("expected calc upstream block, got:\n%s", updated)
	}
}

func TestAddCalcUpstreamNoDuplicate(t *testing.T) {
	input := strings.TrimSpace(`
[server]
listen_addr = ":8080"

[[upstreams]]
name = "calc"
mode = "stdio"
command = "npx"
args = ["-y", "@coo-quack/calc-mcp@latest"]
enabled = true
`) + "\n"
	updated, added := addCalcUpstream(input)
	if added {
		t.Fatal("expected no add when calc already exists")
	}
	if updated != input {
		t.Fatalf("expected unchanged content when calc exists")
	}
}

func TestApplyInstallUninstallHookConfigCursor(t *testing.T) {
	settings := map[string]any{
		"hooks": map[string]any{
			"preToolUse": []any{map[string]any{
				"type":    "command",
				"command": "echo keep-me",
			}},
			"postToolUse": []any{},
		},
	}

	applyInstallHookConfig(settings, "cursor")
	hooks := settings["hooks"].(map[string]any)
	pre := hooks["preToolUse"].([]any)
	post := hooks["postToolUse"].([]any)
	if len(pre) != 2 {
		t.Fatalf("expected 2 pre hooks after install, got %d", len(pre))
	}
	if len(post) != 1 {
		t.Fatalf("expected 1 post hook after install, got %d", len(post))
	}

	applyUninstallHookConfig(settings, "cursor")
	pre = hooks["preToolUse"].([]any)
	post = hooks["postToolUse"].([]any)
	if len(pre) != 1 {
		t.Fatalf("expected original pre hook to remain, got %d", len(pre))
	}
	if len(post) != 0 {
		t.Fatalf("expected post Atryum hook removed, got %d", len(post))
	}
	cmd := pre[0].(map[string]any)["command"].(string)
	if cmd != "echo keep-me" {
		t.Fatalf("unexpected remaining command: %s", cmd)
	}
}

func TestApplyInstallUninstallHookConfigClaudeCode(t *testing.T) {
	settings := map[string]any{
		"hooks": map[string]any{
			"PreToolUse": []any{map[string]any{
				"matcher": "*",
				"hooks": []any{map[string]any{
					"type":    "command",
					"command": "echo keep-claude",
				}},
			}},
			"PostToolUse": []any{},
		},
	}

	applyInstallHookConfig(settings, "claude-code")
	hooks := settings["hooks"].(map[string]any)
	pre := hooks["PreToolUse"].([]any)
	post := hooks["PostToolUse"].([]any)
	if len(pre) != 2 {
		t.Fatalf("expected 2 pre entries after install, got %d", len(pre))
	}
	if len(post) != 1 {
		t.Fatalf("expected 1 post entry after install, got %d", len(post))
	}

	applyUninstallHookConfig(settings, "claude-code")
	pre = hooks["PreToolUse"].([]any)
	post = hooks["PostToolUse"].([]any)
	if len(pre) != 1 {
		t.Fatalf("expected non-Atryum pre entry to remain, got %d", len(pre))
	}
	if len(post) != 0 {
		t.Fatalf("expected Atryum post entry removed, got %d", len(post))
	}

	innerHooks := pre[0].(map[string]any)["hooks"].([]any)
	if len(innerHooks) != 1 {
		t.Fatalf("expected one inner hook, got %d", len(innerHooks))
	}
	cmd := innerHooks[0].(map[string]any)["command"].(string)
	if cmd != "echo keep-claude" {
		t.Fatalf("unexpected remaining command: %s", cmd)
	}
}
