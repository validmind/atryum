package atryum

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// A managed install registers the Claude Code hook in Claude Code's
// administrator-managed settings and puts the hook script in a root-owned
// directory, so the agent (which runs as the user) can neither remove the
// hook's registration nor rewrite the script. Claude Code merges hooks from
// managed settings with the user's and project's, and a user or project
// "disableAllHooks" cannot switch managed hooks off.
//
// The drop-in file managed-settings.d/atryum.json is used rather than
// managed-settings.json so an organisation's own managed settings are left
// untouched.
//
// What it does not change: the hook still runs as the user, with the user's
// environment, through the node binary named in the command. An agent that
// can replace that binary or alter the environment Claude Code starts with can
// still interfere. The install pins node's absolute path, clears NODE_OPTIONS
// and warns when node is user-writable; a sandbox covers the rest.

// managedRootEnv prefixes every path a managed install writes, so a package
// build can stage the files under a build root. The hook command it writes
// always names the final (unprefixed) script path. When set, the root and
// ownership checks are skipped.
const managedRootEnv = "ATRYUM_MANAGED_ROOT"

// defaultManagedTokenCommand reads each user's own agent key at hook time.
// The hook runs it through a shell, so $HOME is the invoking user's.
const defaultManagedTokenCommand = `cat "$HOME/.atryum/agent-key"`

type managedLayout struct {
	// DropIn is Claude Code's managed-settings.d drop-in for Atryum, and
	// Script the hook script, as they will be on the target machine.
	DropIn, Script string
	// Root is the staging prefix the files are written under ("" normally).
	Root string
}

func (l managedLayout) dropInFile() string { return filepath.Join(l.Root, l.DropIn) }
func (l managedLayout) scriptFile() string { return filepath.Join(l.Root, l.Script) }

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
		DropIn: filepath.Join(settingsDir, "managed-settings.d", "atryum.json"),
		Script: filepath.Join(scriptDir, "atryum-hook.mjs"),
		Root:   root,
	}, nil
}

type managedInstallOptions struct {
	URL           string
	TokenCommand  string
	AllowInsecure bool
	// Node is the node binary the hook runs with; found on PATH when empty.
	Node string
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

// ensureReadableDir creates dir and any missing parents with mode 0755
// whatever the umask, then checks that every directory from root down to dir
// lets other users in. Claude Code runs as the user: a managed file it cannot
// reach is skipped without an error, and nothing would be gated.
func ensureReadableDir(root, dir string) error {
	var missing []string
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Stat(d); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		missing = append(missing, d)
		if d == filepath.Dir(d) {
			break
		}
	}
	for i := len(missing) - 1; i >= 0; i-- {
		if err := os.Mkdir(missing[i], 0o755); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		if err := os.Chmod(missing[i], 0o755); err != nil {
			return err
		}
	}
	stop := filepath.Clean(root)
	if root == "" {
		stop = string(filepath.Separator)
	}
	for d := dir; ; d = filepath.Dir(d) {
		info, err := os.Stat(d)
		if err != nil {
			return err
		}
		if info.Mode().Perm()&0o005 != 0o005 {
			return fmt.Errorf("%s has mode %o, so users (and Claude Code) can't read what's inside it; run chmod o+rx on it and install again", d, info.Mode().Perm())
		}
		if d == stop || d == filepath.Dir(d) {
			return nil
		}
	}
}

// writeManagedFile writes path with exactly mode, whatever the umask or the
// mode of a file already there.
func writeManagedFile(path string, data []byte, mode os.FileMode) error {
	if err := os.WriteFile(path, data, mode); err != nil {
		return err
	}
	return os.Chmod(path, mode)
}

// userWritable reports whether someone other than root could change path:
// it is not owned by root, or its group or others may write it.
func userWritable(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.Mode().Perm()&0o022 != 0 || !ownedByRoot(info)
}

// resolveManagedNode returns the absolute path of the node binary the hook
// will run with.
func resolveManagedNode(configured string) (string, error) {
	node := strings.TrimSpace(configured)
	if node == "" {
		found, err := exec.LookPath("node")
		if err != nil {
			return "", errors.New("node was not found on PATH (sudo often resets it): pass --node /path/to/node")
		}
		node = found
	}
	abs, err := filepath.Abs(node)
	if err != nil {
		return "", err
	}
	if info, err := os.Stat(abs); err != nil || info.IsDir() {
		return "", fmt.Errorf("--node %s is not a file", abs)
	}
	return abs, nil
}

// managedClaudeSettings is the drop-in's content. The commands have the same
// shape as a user-level install, with node's absolute path. PreToolUse sets
// onFailure so a hook that can't start blocks the call instead of letting it
// through (Claude Code 2.1.295 and later).
func managedClaudeSettings(envPrefix, node, script string) map[string]any {
	entry := func(event string) map[string]any {
		hook := map[string]any{
			"type":    "command",
			"command": fmt.Sprintf("%sATRYUM_HOOK_HOST=claude ATRYUM_HOOK_EVENT=%s ATRYUM_SOURCE=claude-code %s %s", envPrefix, event, shellQuote(node), shellQuote(script)),
		}
		if event == "PreToolUse" {
			hook["onFailure"] = "block"
		}
		return map[string]any{"matcher": "*", "hooks": []any{hook}}
	}
	return map[string]any{"hooks": map[string]any{
		"SessionStart": []any{entry("SessionStart")},
		"PreToolUse":   []any{entry("PreToolUse")},
		"PostToolUse":  []any{entry("PostToolUse")},
	}}
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
	node, err := resolveManagedNode(opts.Node)
	if err != nil {
		return err
	}

	for _, file := range []string{layout.scriptFile(), layout.dropInFile()} {
		if err := ensureReadableDir(root, filepath.Dir(file)); err != nil {
			return err
		}
	}
	// Only the copy built into this binary: a file in the working directory
	// could have been put there by the agent.
	if err := writeManagedFile(layout.scriptFile(), embeddedHookScript(), 0o755); err != nil {
		return err
	}

	tokenCommand := strings.TrimSpace(opts.TokenCommand)
	if tokenCommand == "" {
		tokenCommand = defaultManagedTokenCommand
	}
	env := map[string]string{
		"ATRYUM_URL":           url,
		"ATRYUM_TOKEN_COMMAND": tokenCommand,
		// A preload set in the user's environment would run before the hook.
		"NODE_OPTIONS": "",
	}
	if opts.AllowInsecure && insecureTransport(url) {
		env["ATRYUM_ALLOW_INSECURE_HTTP"] = "1"
	}
	settings := managedClaudeSettings(hookEnvPrefix(env), node, layout.Script)
	raw, err := marshalJSONMap(settings)
	if err != nil {
		return err
	}
	if err := writeManagedFile(layout.dropInFile(), raw, 0o644); err != nil {
		return err
	}

	fmt.Fprintf(out, "installed managed Claude Code hooks in %s\n", layout.dropInFile())
	fmt.Fprintf(out, "hook script: %s (only root can change it)\n", layout.scriptFile())
	fmt.Fprintf(out, "node: %s\n", node)
	if root == "" {
		for _, path := range []string{node, filepath.Dir(node)} {
			if userWritable(path) {
				fmt.Fprintf(out, "WARNING: %s can be changed without root, so the agent could replace the node binary the hook runs with. Point --node at a root-owned node, or run the agent in a sandbox.\n", path)
				break
			}
		}
	}
	fmt.Fprintln(out, "blocking when the hook can't start needs Claude Code 2.1.295 or later")
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
	for _, path := range []string{layout.dropInFile(), layout.scriptFile()} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	fmt.Fprintf(out, "removed managed Claude Code hooks (%s)\n", layout.dropInFile())
	return nil
}
