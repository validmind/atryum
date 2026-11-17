//go:build !windows

package atryum

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"

	sharedagenthook "github.com/validmind/atryum/examples/shared-agent-hook"
)

func TestManagedClaudeLayoutPaths(t *testing.T) {
	mac, err := managedClaudeLayout("darwin", "")
	if err != nil {
		t.Fatal(err)
	}
	if mac.dropInFile() != "/Library/Application Support/ClaudeCode/managed-settings.d/atryum.json" ||
		mac.scriptFile() != "/Library/Application Support/Atryum/hooks/atryum-hook.mjs" {
		t.Fatalf("darwin layout = %+v", mac)
	}
	linux, err := managedClaudeLayout("linux", "/stage")
	if err != nil {
		t.Fatal(err)
	}
	if linux.dropInFile() != "/stage/etc/claude-code/managed-settings.d/atryum.json" ||
		linux.scriptFile() != "/stage/usr/local/lib/atryum/hooks/atryum-hook.mjs" ||
		linux.Script != "/usr/local/lib/atryum/hooks/atryum-hook.mjs" {
		t.Fatalf("linux layout = %+v", linux)
	}
	if _, err := managedClaudeLayout("windows", ""); err == nil {
		t.Fatal("windows: want an error")
	}
}

// managedTestRoot stages a managed install under a temporary root and returns
// install options that name a stand-in node binary.
func managedTestRoot(t *testing.T) (managedLayout, managedInstallOptions) {
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
	node := filepath.Join(t.TempDir(), "node")
	if err := os.WriteFile(node, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return layout, managedInstallOptions{URL: "https://atryum.example", Node: node}
}

type hookEntry struct {
	Command   string `json:"command"`
	OnFailure string `json:"onFailure"`
}

func dropInHook(t *testing.T, dropIn, event string) hookEntry {
	t.Helper()
	raw, err := os.ReadFile(dropIn)
	if err != nil {
		t.Fatalf("read drop-in: %v", err)
	}
	var settings struct {
		Hooks map[string][]struct {
			Hooks []hookEntry `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(raw, &settings); err != nil {
		t.Fatalf("drop-in is not valid JSON: %v", err)
	}
	if len(settings.Hooks[event]) != 1 || len(settings.Hooks[event][0].Hooks) != 1 {
		t.Fatalf("drop-in should have one %s hook: %s", event, raw)
	}
	return settings.Hooks[event][0].Hooks[0]
}

func TestManagedInstallWritesHookAndDropIn(t *testing.T) {
	layout, opts := managedTestRoot(t)
	var out bytes.Buffer
	if err := installManagedClaudeHooks(opts, &out); err != nil {
		t.Fatalf("install: %v", err)
	}

	if info, err := os.Stat(layout.scriptFile()); err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("script: err=%v info=%v, want mode 755", err, info)
	}
	if info, err := os.Stat(layout.dropInFile()); err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("drop-in: err=%v info=%v, want mode 644", err, info)
	}

	pre := dropInHook(t, layout.dropInFile(), "PreToolUse")
	for _, want := range []string{
		"ATRYUM_URL=https://atryum.example",
		`ATRYUM_TOKEN_COMMAND='cat "$HOME/.atryum/agent-key"'`,
		"NODE_OPTIONS='' ",
		"ATRYUM_HOOK_EVENT=PreToolUse",
		" " + shellQuote(opts.Node) + " ",
		// The command names the script where it will live on the target
		// machine, not under the staging root it was written to.
		" " + shellQuote(layout.Script),
	} {
		if !strings.Contains(pre.Command, want) {
			t.Fatalf("PreToolUse command %q does not contain %q", pre.Command, want)
		}
	}
	if strings.Contains(pre.Command, layout.Root) {
		t.Fatalf("PreToolUse command names the staging root: %q", pre.Command)
	}
	if strings.Contains(pre.Command, "ATRYUM_ALLOW_INSECURE_HTTP") {
		t.Fatalf("https install must not allow insecure http: %q", pre.Command)
	}
	if pre.OnFailure != "block" {
		t.Fatalf("PreToolUse onFailure = %q, want block", pre.OnFailure)
	}
	if post := dropInHook(t, layout.dropInFile(), "PostToolUse"); post.OnFailure != "" {
		t.Fatalf("PostToolUse onFailure = %q, want unset", post.OnFailure)
	}
	dropInHook(t, layout.dropInFile(), "SessionStart")
	if !strings.Contains(out.String(), "atryum hooks uninstall --yes claude-code") {
		t.Fatalf("install output doesn't tell users to remove the user-level copy:\n%s", out.String())
	}

	if err := uninstallManagedClaudeHooks(&out); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	for _, path := range []string{layout.dropInFile(), layout.scriptFile()} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s still exists after uninstall", path)
		}
	}
}

// A root install must use the script built into the binary, never a file in
// the working directory, which the agent may have written.
func TestManagedInstallIgnoresHookScriptInWorkingDirectory(t *testing.T) {
	layout, opts := managedTestRoot(t)
	cwd := t.TempDir()
	planted := filepath.Join(cwd, "examples", "shared-agent-hook", "atryum-hook.mjs")
	if err := os.MkdirAll(filepath.Dir(planted), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(planted, []byte("process.exit(0) // planted\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(cwd)

	if err := installManagedClaudeHooks(opts, &bytes.Buffer{}); err != nil {
		t.Fatalf("install: %v", err)
	}
	got, err := os.ReadFile(layout.scriptFile())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, sharedagenthook.Script) {
		t.Fatalf("installed script is not the embedded one (starts %q)", string(got[:min(len(got), 40)]))
	}
}

// Under a strict umask the directories must still be readable by users;
// otherwise Claude Code skips the drop-in and nothing is gated.
func TestManagedInstallDirectoriesIgnoreUmask(t *testing.T) {
	layout, opts := managedTestRoot(t)
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)

	if err := installManagedClaudeHooks(opts, &bytes.Buffer{}); err != nil {
		t.Fatalf("install: %v", err)
	}
	for _, file := range []string{layout.dropInFile(), layout.scriptFile()} {
		for dir := filepath.Dir(file); dir != layout.Root; dir = filepath.Dir(dir) {
			info, err := os.Stat(dir)
			if err != nil {
				t.Fatal(err)
			}
			if perm := info.Mode().Perm(); perm != 0o755 {
				t.Fatalf("%s has mode %o under umask 077, want 755", dir, perm)
			}
		}
	}
	if info, _ := os.Stat(layout.dropInFile()); info.Mode().Perm() != 0o644 {
		t.Fatalf("drop-in mode = %o, want 644", info.Mode().Perm())
	}
}

func TestManagedInstallRefusesUnreadableExistingDirectory(t *testing.T) {
	layout, opts := managedTestRoot(t)
	dir := filepath.Dir(layout.dropInFile())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	err := installManagedClaudeHooks(opts, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "chmod o+rx") {
		t.Fatalf("err = %v, want a refusal that says how to fix the directory", err)
	}
}

func TestManagedInstallValidatesURLAndNode(t *testing.T) {
	layout, opts := managedTestRoot(t)
	var out bytes.Buffer
	noURL := opts
	noURL.URL = ""
	if err := installManagedClaudeHooks(noURL, &out); err == nil || !strings.Contains(err.Error(), "--url") {
		t.Fatalf("no URL: err = %v, want a hint to pass --url", err)
	}
	plain := opts
	plain.URL = "http://atryum.internal:8080"
	if err := installManagedClaudeHooks(plain, &out); err == nil || !strings.Contains(err.Error(), "--allow-insecure-http") {
		t.Fatalf("plain http: err = %v, want a refusal", err)
	}
	plain.AllowInsecure = true
	plain.TokenCommand = "my-token-helper"
	if err := installManagedClaudeHooks(plain, &out); err != nil {
		t.Fatalf("plain http with opt-in: %v", err)
	}
	cmd := dropInHook(t, layout.dropInFile(), "PreToolUse").Command
	if !strings.Contains(cmd, "ATRYUM_ALLOW_INSECURE_HTTP=1") || !strings.Contains(cmd, "ATRYUM_TOKEN_COMMAND=my-token-helper") {
		t.Fatalf("command %q missing the opt-in or the custom token command", cmd)
	}
	badNode := opts
	badNode.Node = filepath.Join(t.TempDir(), "missing-node")
	if err := installManagedClaudeHooks(badNode, &out); err == nil || !strings.Contains(err.Error(), "--node") {
		t.Fatalf("missing node: err = %v, want an error naming --node", err)
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
	layout, opts := managedTestRoot(t)
	if err := runHooks([]string{"install", "--managed", "--url", "https://atryum.example", "--node", opts.Node, "-y", "claude-code"}); err != nil {
		t.Fatalf("runHooks: %v", err)
	}
	if _, err := os.Stat(layout.dropInFile()); err != nil {
		t.Fatalf("drop-in not written: %v", err)
	}
	if err := runHooks([]string{"uninstall", "--managed", "--yes", "claude-code"}); err != nil {
		t.Fatalf("runHooks uninstall: %v", err)
	}
	if err := runHooks([]string{"install", "--managed", "-y", "cursor"}); err == nil || !strings.Contains(err.Error(), "claude-code only") {
		t.Fatalf("--managed for cursor: err = %v", err)
	}
	if err := runHooks([]string{"install", "--url", "https://atryum.example", "-y", "claude-code"}); err == nil || !strings.Contains(err.Error(), "only applies with --managed") {
		t.Fatalf("--url without --managed: err = %v, want it rejected", err)
	}
}

// The user-level install works from any directory because the script is built
// into the binary (a released binary has no source tree beside it).
func TestResolveHookScriptFallsBackToEmbeddedCopy(t *testing.T) {
	if len(embeddedHookScript()) == 0 || !bytes.Equal(embeddedHookScript(), sharedagenthook.Script) {
		t.Fatal("embedded hook script is empty or differs from the package copy")
	}
}

func TestUserWritable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root")
	}
	mine := filepath.Join(t.TempDir(), "node")
	if err := os.WriteFile(mine, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	if !userWritable(mine) {
		t.Fatal("a file owned by a non-root user should count as user-writable")
	}
	if userWritable(filepath.Join(t.TempDir(), "missing")) {
		t.Fatal("a missing file should not be reported as user-writable")
	}
}
