package atryum

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/validmind/atryum/internal/version"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
)

const validMindSetupBaseURL = "https://app.prod.validmind.ai"

func globalUsage() string {
	return strings.TrimSpace(`usage: atryum <command> [options]

Commands:
  run        Start the Atryum server.
  setup      First-time setup flows (demo, mcp, validmind, claude).
  hooks      Install or uninstall agent hooks.
  login      Sign in to an Atryum server (OAuth device flow).
  logout     Forget a stored login.
  whoami     Show who you are signed in as.
  agent      List/create agents and manage their API keys.
  licenses   Print bundled third-party license notices.
  version    Print the atryum version.
  help       Show this help.

	Examples:
	  atryum run --config ./atryum.toml
	  atryum setup demo
	  atryum setup mcp
	  atryum setup validmind
	  atryum setup claude --url https://atryum.example.com
	  atryum login --url https://atryum.example.com
	  atryum agent list
	  atryum agent key create "Claude Code on my-laptop"
	  atryum hooks install cursor
	  atryum hooks install claude-code
	  atryum hooks install codex
	  atryum hooks install amp
	  atryum hooks install pi
	  atryum licenses`)
}

// versionString is the injected release version, annotated with the VCS
// revision go stamped into the binary when one is present.
func versionString() string {
	v := version.Version
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return v
	}
	var rev, modified string
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			modified = s.Value
		}
	}
	if rev == "" {
		return v
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	if modified == "true" {
		rev += ", modified"
	}
	return fmt.Sprintf("%s (%s)", v, rev)
}

func runUsage() string {
	return strings.TrimSpace(`usage: atryum run [--config PATH] [--init-servers]

Starts the Atryum server.

Options:
  --config PATH    Path to TOML config file.
  --init-servers   Test all enabled MCP servers on startup (optional).

Config resolution when --config is not provided:
  1) ./atryum.toml
  2) <user-config-dir>/atryum/atryum.toml
  3) fallback to ./atryum.toml defaults

Examples:
	  atryum run
	  atryum run --config ./atryum.toml
	  atryum run --init-servers`)
}

func runLicenses(o options) error {
	fmt.Print(o.thirdPartyNotices)
	if !strings.HasSuffix(o.thirdPartyNotices, "\n") {
		fmt.Println()
	}
	return nil
}

func runSetup(args []string) error {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	configPath := fs.String("config", "", "path to TOML config")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Println(setupUsage())
			return nil
		}
		return errors.New(setupUsage())
	}

	remaining := fs.Args()
	if len(remaining) == 0 {
		return errors.New(setupUsage())
	}
	if remaining[0] == "help" {
		fmt.Println(setupUsage())
		return nil
	}
	// Help after the target belongs to that target: "setup claude --help"
	// must reach setupClaudeUsage, which is where the --url/--agent/-y
	// flags are documented. The other targets take no flags of their own,
	// so their help is this command's usage.
	if remaining[0] != "claude" && hasHelpArg(remaining[1:]) {
		fmt.Println(setupUsage())
		return nil
	}

	switch remaining[0] {
	case "demo":
		return runSetupDemo(*configPath)
	case "mcp":
		return runSetupMCP(*configPath)
	case "validmind":
		return runSetupValidMind(*configPath)
	case "claude":
		return runSetupClaude(remaining[1:], stdIO())
	default:
		return fmt.Errorf("unknown setup target %q\n%s", remaining[0], setupUsage())
	}
}

func setupUsage() string {
	return strings.TrimSpace(`usage: atryum setup [--config PATH] [demo|mcp|validmind|claude]

Commands:
	  demo       Create a minimal local config with SQLite and calc MCP upstream.
	  mcp        Add the calc MCP upstream to an existing config.
	  validmind  Prompt for API key/secret and store them in an existing config.
	  claude     Sign in, pick an agent, issue an API key and install Claude Code hooks
	             (see "atryum setup claude --help").

Examples:
	  atryum setup demo
	  atryum setup mcp
	  atryum setup --config ./atryum.toml demo
	  atryum setup validmind
	  atryum setup --help`)
}

func runSetupDemo(configPath string) error {
	targetPath := configPath
	if targetPath == "" {
		var err error
		targetPath, err = defaultUserConfigPath()
		if err != nil {
			return fmt.Errorf("resolve user config path: %w", err)
		}
	}

	if _, err := os.Stat(targetPath); err == nil {
		return fmt.Errorf("config already exists at %s", targetPath)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("check config path: %w", err)
	}

	dbPath, err := defaultUserDatabasePath()
	if err != nil {
		return fmt.Errorf("resolve user database path: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return fmt.Errorf("create database dir: %w", err)
	}

	content := buildDemoConfig(dbPath)
	if err := os.WriteFile(targetPath, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	fmt.Printf("created demo config: %s\n", targetPath)
	fmt.Printf("sqlite database path: %s\n", dbPath)
	fmt.Println("next: run `atryum run` (it will auto-discover this config)")
	return nil
}

func buildDemoConfig(dbPath string) string {
	quotedDBPath := strconv.Quote(dbPath)
	return strings.TrimSpace(fmt.Sprintf(`
[server]
listen_addr = ":8080"
database_path = %s
database_url = ""
log_level = "info"

[backend]
base_url = ""
machine_key = ""
machine_secret = ""
api_key = ""
api_secret = ""
connection_timeout_seconds = 5

[defaults]
request_timeout_seconds = 30

[policy]
provider = "manual_approval"

[api_key]
key = ""
secret = ""

# OpenTelemetry trace export (off by default). Enable and add one
# [[otel.exporters]] per backend (Langfuse, Datadog, …); see atryum.example.toml.
[otel]
enabled = false

[auth_debug]
skip_verify = false

[[upstreams]]
name = "calc"
mode = "stdio"
command = "npx"
args = ["-y", "@coo-quack/calc-mcp@latest"]
enabled = true
`, quotedDBPath)) + "\n"
}

func runSetupMCP(configPath string) error {
	targetPath := configPath
	if targetPath == "" {
		var err error
		targetPath, err = resolveStartupConfigPath("")
		if err != nil {
			return fmt.Errorf("resolve config path: %w", err)
		}
	}

	if _, err := os.Stat(targetPath); os.IsNotExist(err) {
		return fmt.Errorf("no config file found at %s (run `atryum setup demo` first)", targetPath)
	} else if err != nil {
		return fmt.Errorf("check config path: %w", err)
	}

	raw, err := os.ReadFile(targetPath)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}

	updated, added := addCalcUpstream(string(raw))
	if !added {
		fmt.Printf("calc MCP upstream already configured in %s\n", targetPath)
		return nil
	}

	if err := os.WriteFile(targetPath, []byte(updated), 0o644); err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	fmt.Printf("added calc MCP upstream to %s\n", targetPath)
	return nil
}

func addCalcUpstream(content string) (string, bool) {
	if hasUpstreamNamed(content, "calc") {
		return content, false
	}
	trimmed := strings.TrimRight(content, "\n")
	if trimmed != "" {
		trimmed += "\n\n"
	}
	trimmed += strings.TrimSpace(`[[upstreams]]
name = "calc"
mode = "stdio"
command = "npx"
args = ["-y", "@coo-quack/calc-mcp@latest"]
enabled = true`)
	return trimmed + "\n", true
}

func hasUpstreamNamed(content, wanted string) bool {
	lines := strings.Split(content, "\n")
	inUpstream := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "[[upstreams]]" {
			inUpstream = true
			continue
		}
		if strings.HasPrefix(trimmed, "[[") && trimmed != "[[upstreams]]" {
			inUpstream = false
			continue
		}
		if !inUpstream {
			continue
		}
		if !strings.HasPrefix(trimmed, "name") {
			continue
		}
		parts := strings.SplitN(trimmed, "=", 2)
		if len(parts) != 2 {
			continue
		}
		value := strings.TrimSpace(parts[1])
		value = strings.Trim(value, `"`)
		if value == wanted {
			return true
		}
	}
	return false
}

func runSetupValidMind(configPath string) error {
	targetPath := configPath
	if targetPath == "" {
		var err error
		targetPath, err = resolveStartupConfigPath("")
		if err != nil {
			return fmt.Errorf("resolve config path: %w", err)
		}
	}

	if _, err := os.Stat(targetPath); os.IsNotExist(err) {
		return fmt.Errorf("no config file found at %s (run `atryum setup demo` first)", targetPath)
	} else if err != nil {
		return fmt.Errorf("check config path: %w", err)
	}

	reader := bufio.NewReader(os.Stdin)
	baseURL, err := promptWithDefault(reader, "ValidMind base URL", validMindSetupBaseURL)
	if err != nil {
		return err
	}
	if strings.TrimSpace(baseURL) == "" {
		return errors.New("base URL cannot be empty")
	}

	apiKey, err := prompt(reader, "ValidMind API key")
	if err != nil {
		return err
	}
	if strings.TrimSpace(apiKey) == "" {
		return errors.New("api key cannot be empty")
	}

	apiSecret, err := prompt(reader, "ValidMind API secret")
	if err != nil {
		return err
	}
	if strings.TrimSpace(apiSecret) == "" {
		return errors.New("api secret cannot be empty")
	}

	raw, err := os.ReadFile(targetPath)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	updated := upsertBackendCredentials(string(raw), baseURL, apiKey, apiSecret)

	if err := os.WriteFile(targetPath, []byte(updated), 0o644); err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	fmt.Printf("updated ValidMind credentials in %s\n", targetPath)
	return nil
}

func prompt(reader *bufio.Reader, label string) (string, error) {
	fmt.Printf("%s: ", label)
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func promptWithDefault(reader *bufio.Reader, label, defaultValue string) (string, error) {
	fmt.Printf("%s [%s]: ", label, defaultValue)
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	value := strings.TrimSpace(line)
	if value == "" {
		return defaultValue, nil
	}
	return value, nil
}

func upsertBackendCredentials(content, baseURL, apiKey, apiSecret string) string {
	lines := strings.Split(content, "\n")
	backendStart := -1
	backendEnd := len(lines)

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "[backend]" {
			backendStart = i
			continue
		}
		if backendStart >= 0 && i > backendStart && strings.HasPrefix(trimmed, "[") {
			backendEnd = i
			break
		}
	}

	baseURLLine := fmt.Sprintf("base_url = %s", strconv.Quote(baseURL))
	keyLine := fmt.Sprintf("api_key = %s", strconv.Quote(apiKey))
	secretLine := fmt.Sprintf("api_secret = %s", strconv.Quote(apiSecret))

	if backendStart == -1 {
		trimmed := strings.TrimRight(content, "\n")
		if trimmed != "" {
			trimmed += "\n\n"
		}
		trimmed += "[backend]\n"
		trimmed += baseURLLine + "\n"
		trimmed += keyLine + "\n"
		trimmed += secretLine + "\n"
		return trimmed + "\n"
	}

	hasKey := false
	hasSecret := false
	hasBaseURL := false
	for i := backendStart + 1; i < backendEnd; i++ {
		trimmed := strings.TrimSpace(lines[i])
		switch {
		case strings.HasPrefix(trimmed, "base_url"):
			lines[i] = baseURLLine
			hasBaseURL = true
		case strings.HasPrefix(trimmed, "api_key"):
			lines[i] = keyLine
			hasKey = true
		case strings.HasPrefix(trimmed, "api_secret"):
			lines[i] = secretLine
			hasSecret = true
		}
	}

	insert := make([]string, 0, 3)
	if !hasBaseURL {
		insert = append(insert, baseURLLine)
	}
	if !hasKey {
		insert = append(insert, keyLine)
	}
	if !hasSecret {
		insert = append(insert, secretLine)
	}
	if len(insert) == 0 {
		return strings.Join(lines, "\n")
	}

	updated := make([]string, 0, len(lines)+len(insert))
	updated = append(updated, lines[:backendEnd]...)
	updated = append(updated, insert...)
	updated = append(updated, lines[backendEnd:]...)
	return strings.Join(updated, "\n")
}

func runHooks(args []string) error {
	if hasHelpArg(args) {
		fmt.Println(hooksUsage())
		return nil
	}

	fs := flag.NewFlagSet("hooks", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	autoYes := fs.Bool("y", false, "auto-confirm")
	autoYesLong := fs.Bool("yes", false, "auto-confirm")
	if err := fs.Parse(args); err != nil {
		return errors.New(hooksUsage())
	}

	remaining := fs.Args()
	if len(remaining) == 0 {
		return errors.New("missing action and target. tell me exactly what you want, for example:\n" + hooksUsage())
	}
	if len(remaining) == 1 {
		return errors.New("missing target (cursor|claude-code|codex|amp|pi). tell me exactly what you want:\n" + hooksUsage())
	}
	if len(remaining) > 2 {
		return errors.New("too many arguments\n" + hooksUsage())
	}

	action := remaining[0]
	target := remaining[1]
	if action != "install" && action != "uninstall" {
		return fmt.Errorf("unknown hooks action %q\n%s", action, hooksUsage())
	}
	if target != "cursor" && target != "claude-code" && target != "codex" && target != "amp" && target != "pi" {
		return fmt.Errorf("unknown hooks target %q\n%s", target, hooksUsage())
	}

	confirmed := *autoYes || *autoYesLong
	if !confirmed {
		reader := bufio.NewReader(os.Stdin)
		ok, err := promptConfirm(reader, fmt.Sprintf("%s Atryum hooks for %s", action, target))
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("aborted")
		}
	}

	if action == "install" {
		return installHooks(target)
	}
	return uninstallHooks(target)
}

func hooksUsage() string {
	return strings.TrimSpace(`usage: atryum hooks [install|uninstall] [-y|--yes] [cursor|claude-code|codex|amp|pi]

Commands:
  install    Install Atryum hook/plugin files and add hook commands when needed.
  uninstall  Remove Atryum hook commands or plugin files from target settings.

Targets:
  cursor       ~/.cursor/hooks.json
  claude-code  ~/.claude/settings.json (or $CLAUDE_CONFIG_DIR/settings.json)
  codex        ~/.codex/hooks.json
  amp          ~/.config/amp/plugins/atryum.ts
  pi           ~/.pi/agent/extensions/atryum/index.ts

The hook script and its state live under ~/.atryum, or under $ATRYUM_HOME when
set; hook commands are written with that location baked in.

Examples:
  atryum hooks install cursor
  atryum hooks install claude-code
  atryum hooks install codex
  atryum hooks install amp
  atryum hooks install pi
  atryum hooks uninstall --yes claude-code
  atryum hooks --help`)
}

func hasHelpArg(args []string) bool {
	for _, arg := range args {
		if arg == "-h" || arg == "--help" || arg == "help" {
			return true
		}
	}
	return false
}

func promptConfirm(reader *bufio.Reader, label string) (bool, error) {
	fmt.Printf("%s? [y/N]: ", label)
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	value := strings.ToLower(strings.TrimSpace(line))
	return value == "y" || value == "yes", nil
}

func installHooks(target string) error {
	return installHooksWithEnv(target, nil, os.Stdout)
}

// installHooksWithEnv installs hooks for target with extra environment
// variables (e.g. ATRYUM_URL, ATRYUM_TOKEN_COMMAND) prefixed onto each hook
// command. Any previously installed Atryum hook commands for the target are
// replaced so re-running setup never leaves two copies behind.
func installHooksWithEnv(target string, env map[string]string, out io.Writer) error {
	if target == "amp" || target == "pi" {
		return installAgentPlugin(target)
	}

	hookScript, err := resolveHookScript()
	if err != nil {
		return err
	}
	layout, err := resolveHookLayout()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(layout.ScriptPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(layout.ScriptPath, hookScript, 0o755); err != nil {
		return err
	}
	if layout.StateDir != "" {
		merged := make(map[string]string, len(env)+1)
		for k, v := range env {
			merged[k] = v
		}
		if _, set := merged["ATRYUM_STATE_DIR"]; !set {
			merged["ATRYUM_STATE_DIR"] = layout.StateDir
		}
		env = merged
	}

	settingsPath, err := hookSettingsPath(target)
	if err != nil {
		return err
	}
	settings, err := readJSONMap(settingsPath)
	if err != nil {
		return err
	}
	// Always strip earlier Atryum entries first. Install dedupes by exact
	// command string, and the command changes whenever the env prefix, the
	// script path or the state dir changes (setup claude vs. a bare hooks
	// install, or a different ATRYUM_HOME), so without this a re-install
	// leaves two entries firing on every event, one of them unauthenticated.
	applyUninstallHookConfig(settings, target)
	applyInstallHookConfigWithEnvAndScript(settings, target, hookEnvPrefix(env), layout.CommandScript)
	if err := writeJSONMap(settingsPath, settings); err != nil {
		return err
	}

	fmt.Fprintf(out, "installed hooks for %s in %s\n", target, settingsPath)
	fmt.Fprintln(out, "restart your editor/agent tool to apply hook changes")
	return nil
}

// defaultHookScriptRef is how installed hook commands refer to the script when
// Atryum's home is the stock ~/.atryum: a tilde path, so settings files stay
// portable and match the hand-written examples.
const defaultHookScriptRef = "~/.atryum/hooks/atryum-hook.mjs"

// hookLayout is where the hook's files live for one Atryum home. It is the
// single place ATRYUM_HOME is turned into paths, so a custom home repoints the
// script, the agent key (see setup claude) and the hook state together.
type hookLayout struct {
	// ScriptPath is the absolute location the hook script is written to.
	ScriptPath string
	// CommandScript is how hook commands in settings files refer to the
	// script: the tilde form for the default home, absolute otherwise.
	CommandScript string
	// StateDir is the ATRYUM_STATE_DIR to bake into hook commands. Empty for
	// the default home, where the hook's own default already points there.
	StateDir string
}

// resolveHookLayout derives the hook file layout from ATRYUM_HOME (falling
// back to ~/.atryum). Power users run several Claude Codes as distinct agents
// by giving each its own ATRYUM_HOME (and CLAUDE_CONFIG_DIR); everything the
// hook touches then lives under that one directory.
func resolveHookLayout() (hookLayout, error) {
	home, err := atryumHomeDir()
	if err != nil {
		return hookLayout{}, err
	}
	layout := hookLayout{
		ScriptPath:    filepath.Join(home, "hooks", "atryum-hook.mjs"),
		CommandScript: defaultHookScriptRef,
	}
	if !isDefaultAtryumHome(home) {
		layout.CommandScript = shellQuote(layout.ScriptPath)
		layout.StateDir = filepath.Join(home, "agent-hook-state")
	}
	return layout, nil
}

// isDefaultAtryumHome reports whether dir is the stock ~/.atryum.
func isDefaultAtryumHome(dir string) bool {
	userHome, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	return filepath.Clean(dir) == filepath.Join(userHome, ".atryum")
}

// hookEnvPrefix renders env as "K=V K2=V2 " for prefixing onto a shell
// command, in sorted key order so output is deterministic. Values are
// single-quoted when they contain shell metacharacters.
func hookEnvPrefix(env map[string]string) string {
	if len(env) == 0 {
		return ""
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteString("=")
		b.WriteString(shellQuote(env[k]))
		b.WriteString(" ")
	}
	return b.String()
}

func uninstallHooks(target string) error {
	if target == "amp" || target == "pi" {
		return uninstallAgentPlugin(target)
	}

	settingsPath, err := hookSettingsPath(target)
	if err != nil {
		return err
	}
	settings, err := readJSONMap(settingsPath)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Printf("no settings file found at %s\n", settingsPath)
			return nil
		}
		return err
	}
	applyUninstallHookConfig(settings, target)
	if err := writeJSONMap(settingsPath, settings); err != nil {
		return err
	}
	fmt.Printf("uninstalled hooks for %s in %s\n", target, settingsPath)
	return nil
}

func resolveHookScript() ([]byte, error) {
	wdPath := filepath.Join("examples", "shared-agent-hook", "atryum-hook.mjs")
	if raw, err := os.ReadFile(wdPath); err == nil {
		return raw, nil
	}

	_, thisFile, _, ok := runtime.Caller(0)
	if ok {
		root := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))
		path := filepath.Join(root, "examples", "shared-agent-hook", "atryum-hook.mjs")
		if raw, err := os.ReadFile(path); err == nil {
			return raw, nil
		}
	}

	return nil, errors.New("could not locate examples/shared-agent-hook/atryum-hook.mjs")
}

func installAgentPlugin(target string) error {
	plugin, err := resolveAgentPlugin(target)
	if err != nil {
		return err
	}
	path, err := agentPluginPath(target)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, plugin, 0o644); err != nil {
		return err
	}
	fmt.Printf("installed %s plugin in %s\n", target, path)
	if target == "amp" {
		fmt.Println("start Amp with PLUGINS=all so it loads plugins")
	} else if target == "pi" {
		fmt.Println("restart Pi or run /reload in an active Pi session")
	}
	return nil
}

func uninstallAgentPlugin(target string) error {
	path, err := agentPluginPath(target)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			fmt.Printf("no %s plugin found at %s\n", target, path)
			return nil
		}
		return err
	}
	fmt.Printf("uninstalled %s plugin from %s\n", target, path)
	return nil
}

func resolveAgentPlugin(target string) ([]byte, error) {
	srcPath, err := agentPluginSourcePath(target)
	if err != nil {
		return nil, err
	}
	wdPath := filepath.Join(srcPath...)
	if raw, err := os.ReadFile(wdPath); err == nil {
		return raw, nil
	}

	_, thisFile, _, ok := runtime.Caller(0)
	if ok {
		root := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))
		path := filepath.Join(append([]string{root}, srcPath...)...)
		if raw, err := os.ReadFile(path); err == nil {
			return raw, nil
		}
	}

	return nil, fmt.Errorf("could not locate %s", filepath.Join(srcPath...))
}

func agentPluginSourcePath(target string) ([]string, error) {
	switch target {
	case "amp":
		return []string{"examples", "amp-plugin", "atryum.ts"}, nil
	case "pi":
		return []string{"examples", "pi-extension", "index.ts"}, nil
	default:
		return nil, fmt.Errorf("unsupported plugin target %q", target)
	}
}

func agentPluginPath(target string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	switch target {
	case "amp":
		return filepath.Join(home, ".config", "amp", "plugins", "atryum.ts"), nil
	case "pi":
		return filepath.Join(home, ".pi", "agent", "extensions", "atryum", "index.ts"), nil
	default:
		return "", fmt.Errorf("unsupported plugin target %q", target)
	}
}

func hookSettingsPath(target string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if target == "cursor" {
		return filepath.Join(home, ".cursor", "hooks.json"), nil
	}
	if target == "claude-code" {
		// Claude Code's own override for its config directory; honouring it
		// is what lets two Claude Codes carry two different hook configs.
		if dir := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")); dir != "" {
			return filepath.Join(dir, "settings.json"), nil
		}
		return filepath.Join(home, ".claude", "settings.json"), nil
	}
	if target == "codex" {
		return filepath.Join(home, ".codex", "hooks.json"), nil
	}
	return "", fmt.Errorf("unsupported hook target %q", target)
}

func readJSONMap(path string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{}, nil
		}
		return nil, err
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return map[string]any{}, nil
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, fmt.Errorf("parse JSON %s: %w", path, err)
	}
	return value, nil
}

func writeJSONMap(path string, value map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return os.WriteFile(path, raw, 0o644)
}

func applyInstallHookConfig(settings map[string]any, target string) {
	applyInstallHookConfigWithEnv(settings, target, "")
}

// applyInstallHookConfigWithEnv is applyInstallHookConfig with an extra
// "K=V " environment prefix on every hook command (see hookEnvPrefix).
func applyInstallHookConfigWithEnv(settings map[string]any, target string, envPrefix string) {
	applyInstallHookConfigWithEnvAndScript(settings, target, envPrefix, defaultHookScriptRef)
}

// applyInstallHookConfigWithEnvAndScript is the general form: script is how
// the commands refer to the hook script (see hookLayout.CommandScript).
func applyInstallHookConfigWithEnvAndScript(settings map[string]any, target string, envPrefix string, script string) {
	hooks := ensureMap(settings, "hooks")
	if target == "cursor" {
		start := ensureSlice(hooks, "sessionStart")
		pre := ensureSlice(hooks, "preToolUse")
		post := ensureSlice(hooks, "postToolUse")
		start = appendUniqueCommand(start, map[string]any{
			"type":    "command",
			"command": envPrefix + "ATRYUM_HOOK_HOST=cursor ATRYUM_HOOK_EVENT=sessionStart ATRYUM_SOURCE=cursor node " + script,
		})
		pre = appendUniqueCommand(pre, map[string]any{
			"type":    "command",
			"command": envPrefix + "ATRYUM_HOOK_HOST=cursor ATRYUM_HOOK_EVENT=preToolUse ATRYUM_SOURCE=cursor node " + script,
		})
		post = appendUniqueCommand(post, map[string]any{
			"type":    "command",
			"command": envPrefix + "ATRYUM_HOOK_HOST=cursor ATRYUM_HOOK_EVENT=postToolUse ATRYUM_SOURCE=cursor node " + script,
		})
		hooks["sessionStart"] = start
		hooks["preToolUse"] = pre
		hooks["postToolUse"] = post
		return
	}

	host := "claude"
	source := "claude-code"
	if target == "codex" {
		host = "codex"
		source = "codex"
	}
	start := ensureSlice(hooks, "SessionStart")
	pre := ensureSlice(hooks, "PreToolUse")
	post := ensureSlice(hooks, "PostToolUse")
	start = appendUniqueNestedHookEntry(start, fmt.Sprintf("%sATRYUM_HOOK_HOST=%s ATRYUM_HOOK_EVENT=SessionStart ATRYUM_SOURCE=%s node %s", envPrefix, host, source, script))
	pre = appendUniqueNestedHookEntry(pre, fmt.Sprintf("%sATRYUM_HOOK_HOST=%s ATRYUM_HOOK_EVENT=PreToolUse ATRYUM_SOURCE=%s node %s", envPrefix, host, source, script))
	post = appendUniqueNestedHookEntry(post, fmt.Sprintf("%sATRYUM_HOOK_HOST=%s ATRYUM_HOOK_EVENT=PostToolUse ATRYUM_SOURCE=%s node %s", envPrefix, host, source, script))
	hooks["SessionStart"] = start
	hooks["PreToolUse"] = pre
	hooks["PostToolUse"] = post
}

func applyUninstallHookConfig(settings map[string]any, target string) {
	hooks := ensureMap(settings, "hooks")
	if target == "cursor" {
		hooks["sessionStart"] = removeAtryumCommands(ensureSlice(hooks, "sessionStart"))
		hooks["preToolUse"] = removeAtryumCommands(ensureSlice(hooks, "preToolUse"))
		hooks["postToolUse"] = removeAtryumCommands(ensureSlice(hooks, "postToolUse"))
		return
	}
	hooks["SessionStart"] = removeAtryumNestedHookCommands(ensureSlice(hooks, "SessionStart"))
	hooks["PreToolUse"] = removeAtryumNestedHookCommands(ensureSlice(hooks, "PreToolUse"))
	hooks["PostToolUse"] = removeAtryumNestedHookCommands(ensureSlice(hooks, "PostToolUse"))
}

func ensureMap(parent map[string]any, key string) map[string]any {
	if raw, ok := parent[key]; ok {
		if typed, ok := raw.(map[string]any); ok {
			return typed
		}
	}
	created := map[string]any{}
	parent[key] = created
	return created
}

func ensureSlice(parent map[string]any, key string) []any {
	if raw, ok := parent[key]; ok {
		if typed, ok := raw.([]any); ok {
			return typed
		}
	}
	return []any{}
}

func appendUniqueCommand(slice []any, entry map[string]any) []any {
	wanted := fmt.Sprintf("%v", entry["command"])
	for _, item := range slice {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if fmt.Sprintf("%v", m["command"]) == wanted {
			return slice
		}
	}
	return append(slice, entry)
}

func appendUniqueNestedHookEntry(slice []any, command string) []any {
	for _, item := range slice {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		hooks, ok := entry["hooks"].([]any)
		if !ok {
			continue
		}
		for _, h := range hooks {
			hookObj, ok := h.(map[string]any)
			if !ok {
				continue
			}
			if fmt.Sprintf("%v", hookObj["command"]) == command {
				return slice
			}
		}
	}
	return append(slice, map[string]any{
		"matcher": "*",
		"hooks": []any{map[string]any{
			"type":    "command",
			"command": command,
		}},
	})
}

func removeAtryumCommands(slice []any) []any {
	kept := make([]any, 0, len(slice))
	for _, item := range slice {
		entry, ok := item.(map[string]any)
		if !ok {
			kept = append(kept, item)
			continue
		}
		command := fmt.Sprintf("%v", entry["command"])
		if strings.Contains(command, "atryum-hook.mjs") {
			continue
		}
		kept = append(kept, item)
	}
	return kept
}

func removeAtryumNestedHookCommands(slice []any) []any {
	kept := make([]any, 0, len(slice))
	for _, item := range slice {
		entry, ok := item.(map[string]any)
		if !ok {
			kept = append(kept, item)
			continue
		}

		hooks, ok := entry["hooks"].([]any)
		if !ok {
			kept = append(kept, item)
			continue
		}

		newHooks := make([]any, 0, len(hooks))
		for _, h := range hooks {
			hookObj, ok := h.(map[string]any)
			if !ok {
				newHooks = append(newHooks, h)
				continue
			}
			command := fmt.Sprintf("%v", hookObj["command"])
			if strings.Contains(command, "atryum-hook.mjs") {
				continue
			}
			newHooks = append(newHooks, h)
		}
		if len(newHooks) == 0 {
			continue
		}
		entry["hooks"] = newHooks
		kept = append(kept, entry)
	}
	return kept
}
