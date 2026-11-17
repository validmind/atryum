package atryum

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestManagedClaudeLayoutPaths(t *testing.T) {
	mac, err := managedClaudeLayout("darwin", "")
	if err != nil {
		t.Fatal(err)
	}
	if mac.DropInPath != "/Library/Application Support/ClaudeCode/managed-settings.d/atryum.json" ||
		mac.ScriptPath != "/Library/Application Support/Atryum/hooks/atryum-hook.mjs" {
		t.Fatalf("darwin layout = %+v", mac)
	}
	linux, err := managedClaudeLayout("linux", "/stage")
	if err != nil {
		t.Fatal(err)
	}
	if linux.DropInPath != "/stage/etc/claude-code/managed-settings.d/atryum.json" ||
		linux.ScriptPath != "/stage/usr/local/lib/atryum/hooks/atryum-hook.mjs" {
		t.Fatalf("linux layout = %+v", linux)
	}
	if _, err := managedClaudeLayout("windows", ""); err == nil {
		t.Fatal("windows: want an error")
	}
}

func managedTestRoot(t *testing.T) (string, managedLayout) {
	t.Helper()
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("managed install supports macOS and Linux")
	}
	root := t.TempDir()
	t.Setenv(managedRootEnv, root)
	t.Setenv("ATRYUM_URL", "")
	layout, err := managedClaudeLayout(runtime.GOOS, root)
	if err != nil {
		t.Fatal(err)
	}
	return root, layout
}

func preToolUseCommand(t *testing.T, dropIn string) string {
	t.Helper()
	raw, err := os.ReadFile(dropIn)
	if err != nil {
		t.Fatalf("read drop-in: %v", err)
	}
	var settings struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(raw, &settings); err != nil {
		t.Fatalf("drop-in is not valid JSON: %v", err)
	}
	for _, event := range []string{"SessionStart", "PreToolUse", "PostToolUse"} {
		if len(settings.Hooks[event]) == 0 || len(settings.Hooks[event][0].Hooks) == 0 {
			t.Fatalf("drop-in has no %s hook: %s", event, raw)
		}
	}
	return settings.Hooks["PreToolUse"][0].Hooks[0].Command
}

func TestManagedInstallWritesRootOwnedHookAndDropIn(t *testing.T) {
	_, layout := managedTestRoot(t)
	var out bytes.Buffer
	if err := installManagedClaudeHooks(managedInstallOptions{URL: "https://atryum.example"}, &out); err != nil {
		t.Fatalf("install: %v", err)
	}

	info, err := os.Stat(layout.ScriptPath)
	if err != nil {
		t.Fatalf("script not written: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o755 {
		t.Fatalf("script mode = %o, want 755", perm)
	}
	if info, err := os.Stat(layout.DropInPath); err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("drop-in: err=%v mode=%v, want 644", err, info)
	}

	cmd := preToolUseCommand(t, layout.DropInPath)
	for _, want := range []string{
		"ATRYUM_URL=https://atryum.example",
		`ATRYUM_TOKEN_COMMAND='cat "$HOME/.atryum/agent-key"'`,
		"ATRYUM_HOOK_EVENT=PreToolUse",
		shellQuote(layout.ScriptPath),
	} {
		if !strings.Contains(cmd, want) {
			t.Fatalf("PreToolUse command %q does not contain %q", cmd, want)
		}
	}
	if strings.Contains(cmd, "ATRYUM_ALLOW_INSECURE_HTTP") {
		t.Fatalf("https install must not allow insecure http: %q", cmd)
	}
	if !strings.Contains(out.String(), "atryum hooks uninstall --yes claude-code") {
		t.Fatalf("install output doesn't tell users to remove the user-level copy:\n%s", out.String())
	}

	if err := uninstallManagedClaudeHooks(&out); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	for _, path := range []string{layout.DropInPath, layout.ScriptPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s still exists after uninstall", path)
		}
	}
}

func TestManagedInstallValidatesURL(t *testing.T) {
	_, layout := managedTestRoot(t)
	var out bytes.Buffer
	if err := installManagedClaudeHooks(managedInstallOptions{}, &out); err == nil || !strings.Contains(err.Error(), "--url") {
		t.Fatalf("no URL: err = %v, want a hint to pass --url", err)
	}
	if err := installManagedClaudeHooks(managedInstallOptions{URL: "http://atryum.internal:8080"}, &out); err == nil || !strings.Contains(err.Error(), "--allow-insecure-http") {
		t.Fatalf("plain http: err = %v, want a refusal", err)
	}
	if err := installManagedClaudeHooks(managedInstallOptions{URL: "http://atryum.internal:8080", AllowInsecure: true, TokenCommand: "my-token-helper"}, &out); err != nil {
		t.Fatalf("plain http with opt-in: %v", err)
	}
	cmd := preToolUseCommand(t, layout.DropInPath)
	if !strings.Contains(cmd, "ATRYUM_ALLOW_INSECURE_HTTP=1") || !strings.Contains(cmd, "ATRYUM_TOKEN_COMMAND=my-token-helper") {
		t.Fatalf("command %q missing the opt-in or the custom token command", cmd)
	}
}

func TestManagedInstallNeedsRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root")
	}
	t.Setenv(managedRootEnv, "")
	if err := checkManagedPreconditions(""); err == nil {
		t.Fatal("non-root managed install: want an error")
	} else if !strings.Contains(err.Error(), "sudo") && !strings.Contains(err.Error(), "WSL") {
		t.Fatalf("error %q should say to use sudo", err)
	}
}

func TestHooksCommandAcceptsFlagsAnywhere(t *testing.T) {
	_, layout := managedTestRoot(t)
	if err := runHooks([]string{"install", "--managed", "--url", "https://atryum.example", "-y", "claude-code"}); err != nil {
		t.Fatalf("runHooks: %v", err)
	}
	if _, err := os.Stat(layout.DropInPath); err != nil {
		t.Fatalf("drop-in not written: %v", err)
	}
	if err := runHooks([]string{"uninstall", "--managed", "--yes", "claude-code"}); err != nil {
		t.Fatalf("runHooks uninstall: %v", err)
	}
	if err := runHooks([]string{"install", "--managed", "-y", "cursor"}); err == nil || !strings.Contains(err.Error(), "claude-code only") {
		t.Fatalf("--managed for cursor: err = %v", err)
	}
}

func TestFlagsFirst(t *testing.T) {
	got := flagsFirst([]string{"install", "--url", "https://a", "-y", "claude-code", "--token-command=x"}, map[string]bool{"url": true, "token-command": true})
	want := []string{"--url", "https://a", "-y", "--token-command=x", "install", "claude-code"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("flagsFirst = %q, want %q", got, want)
	}
	if got := flagsFirst([]string{"a", "--", "-x"}, nil); !reflect.DeepEqual(got, []string{"a", "-x"}) {
		t.Fatalf("flagsFirst with -- = %q", got)
	}
}
