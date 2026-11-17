package atryum

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// A managed install registers the Claude Code hook in Claude Code's
// administrator-managed settings and puts the hook script in a root-owned
// directory, so the agent (which runs as the user) can neither remove the
// hook nor rewrite the script. Claude Code merges hooks from managed settings
// with the user's and project's, and a user or project "disableAllHooks"
// cannot switch managed hooks off.
//
// The drop-in file managed-settings.d/atryum.json is used rather than
// managed-settings.json so an organisation's own managed settings are left
// untouched.

// managedRootEnv prefixes every managed path. It exists for packaging (build
// into a staging root) and tests; when set, the root check is skipped.
const managedRootEnv = "ATRYUM_MANAGED_ROOT"

// defaultManagedTokenCommand reads each user's own agent key at hook time.
// The hook runs it through a shell, so $HOME is the invoking user's.
const defaultManagedTokenCommand = `cat "$HOME/.atryum/agent-key"`

type managedLayout struct {
	// DropInPath is Claude Code's managed-settings.d drop-in for Atryum.
	DropInPath string
	// ScriptPath is the root-owned copy of the hook script.
	ScriptPath string
}

func managedClaudeLayout(goos, root string) (managedLayout, error) {
	var settingsDir, scriptDir string
	switch goos {
	case "darwin":
		settingsDir = "/Library/Application Support/ClaudeCode"
		scriptDir = "/Library/Application Support/Atryum/hooks"
	case "linux":
		settingsDir = "/etc/claude-code"
		scriptDir = "/usr/local/lib/atryum/hooks"
	default:
		return managedLayout{}, fmt.Errorf("managed hook install supports macOS and Linux, not %s", goos)
	}
	return managedLayout{
		DropInPath: filepath.Join(root, settingsDir, "managed-settings.d", "atryum.json"),
		ScriptPath: filepath.Join(root, scriptDir, "atryum-hook.mjs"),
	}, nil
}

type managedInstallOptions struct {
	URL           string
	TokenCommand  string
	AllowInsecure bool
}

// checkManagedPreconditions refuses a managed install that would not deliver
// what it promises: not root (the files would be user-writable again), or WSL
// (Claude Code documents /etc/claude-code as user-writable there).
func checkManagedPreconditions(root string) error {
	if root != "" {
		return nil
	}
	if runtime.GOOS == "linux" && isWSL() {
		return errors.New("managed hook install is not supported on WSL: Claude Code documents /etc/claude-code as writable by users there, so the agent could still change the hook. Use the Windows managed settings or run the agent in a sandbox (see examples/nono-profile)")
	}
	if os.Geteuid() != 0 {
		return errors.New("a managed hook install writes root-owned files the agent can't change: run it with sudo (e.g. sudo atryum hooks install --managed --url https://atryum.example claude-code)")
	}
	return nil
}

func isWSL() bool {
	for _, path := range []string{"/proc/sys/kernel/osrelease", "/proc/version"} {
		if raw, err := os.ReadFile(path); err == nil && strings.Contains(strings.ToLower(string(raw)), "microsoft") {
			return true
		}
	}
	return false
}

func installManagedClaudeHooks(opts managedInstallOptions, out io.Writer) error {
	root := strings.TrimSpace(os.Getenv(managedRootEnv))
	if err := checkManagedPreconditions(root); err != nil {
		return err
	}
	url := normalizeServerURL(opts.URL)
	if url == "" {
		url = normalizeServerURL(os.Getenv("ATRYUM_URL"))
	}
	if url == "" {
		return errors.New("a managed install needs the Atryum server URL: pass --url https://atryum.example (or set ATRYUM_URL)")
	}
	if insecureTransport(url) && !opts.AllowInsecure {
		return fmt.Errorf("%s is plain http to a non-loopback host; the hook would send agent keys over it. Use https, or pass --allow-insecure-http", url)
	}
	layout, err := managedClaudeLayout(runtime.GOOS, root)
	if err != nil {
		return err
	}
	hookScript, err := resolveHookScript()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(layout.ScriptPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(layout.ScriptPath, hookScript, 0o755); err != nil {
		return err
	}
	// WriteFile keeps an existing file's mode; make sure no one else can write it.
	if err := os.Chmod(layout.ScriptPath, 0o755); err != nil {
		return err
	}

	tokenCommand := strings.TrimSpace(opts.TokenCommand)
	if tokenCommand == "" {
		tokenCommand = defaultManagedTokenCommand
	}
	env := map[string]string{
		"ATRYUM_URL":           url,
		"ATRYUM_TOKEN_COMMAND": tokenCommand,
	}
	if opts.AllowInsecure && insecureTransport(url) {
		env["ATRYUM_ALLOW_INSECURE_HTTP"] = "1"
	}
	settings := map[string]any{}
	applyInstallHookConfigWithEnvAndScript(settings, "claude-code", hookEnvPrefix(env), shellQuote(layout.ScriptPath))
	if err := writeJSONMap(layout.DropInPath, settings); err != nil {
		return err
	}
	if err := os.Chmod(layout.DropInPath, 0o644); err != nil {
		return err
	}

	fmt.Fprintf(out, "installed managed Claude Code hooks in %s\n", layout.DropInPath)
	fmt.Fprintf(out, "hook script: %s (only root can change it)\n", layout.ScriptPath)
	fmt.Fprintln(out, "\nNext, for each user who runs Claude Code:")
	fmt.Fprintln(out, "  1. as that user: atryum setup claude   (signs in and saves ~/.atryum/agent-key)")
	fmt.Fprintln(out, "  2. as that user: atryum hooks uninstall --yes claude-code   (removes the user-level copy so calls aren't gated twice)")
	fmt.Fprintln(out, "Restart Claude Code to apply.")
	return nil
}

func uninstallManagedClaudeHooks(out io.Writer) error {
	root := strings.TrimSpace(os.Getenv(managedRootEnv))
	if err := checkManagedPreconditions(root); err != nil {
		return err
	}
	layout, err := managedClaudeLayout(runtime.GOOS, root)
	if err != nil {
		return err
	}
	for _, path := range []string{layout.DropInPath, layout.ScriptPath} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	fmt.Fprintf(out, "removed managed Claude Code hooks (%s)\n", layout.DropInPath)
	return nil
}
