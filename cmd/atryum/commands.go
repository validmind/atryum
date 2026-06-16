package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

const validMindSetupBaseURL = "https://app.dev.vm.validmind.ai"

func globalUsage() string {
	return strings.TrimSpace(`usage: atryum <command> [options]

Commands:
  run        Start the Atryum server.
	  setup      First-time setup flows (demo, mcp, validmind).
  hooks      Install or uninstall agent hooks.
  licenses   Print bundled third-party license notices.
  help       Show this help.

	Examples:
	  atryum run --config ./atryum.toml
	  atryum setup demo
	  atryum setup mcp
	  atryum setup validmind
	  atryum hooks install cursor
	  atryum hooks install amp
	  atryum hooks install pi
	  atryum licenses`)
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

func runLicenses() error {
	fmt.Print(thirdPartyNotices)
	if !strings.HasSuffix(thirdPartyNotices, "\n") {
		fmt.Println()
	}
	return nil
}

func runSetup(args []string) error {
	if hasHelpArg(args) {
		fmt.Println(setupUsage())
		return nil
	}

	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	configPath := fs.String("config", "", "path to TOML config")
	if err := fs.Parse(args); err != nil {
		return errors.New(setupUsage())
	}

	remaining := fs.Args()
	if len(remaining) == 0 {
		return errors.New(setupUsage())
	}

	switch remaining[0] {
	case "demo":
		return runSetupDemo(*configPath)
	case "mcp":
		return runSetupMCP(*configPath)
	case "validmind":
		return runSetupValidMind(*configPath)
	default:
		return fmt.Errorf("unknown setup target %q\n%s", remaining[0], setupUsage())
	}
}

func setupUsage() string {
	return strings.TrimSpace(`usage: atryum setup [--config PATH] [demo|mcp|validmind]

Commands:
	  demo       Create a minimal local config with SQLite and calc MCP upstream.
	  mcp        Add the calc MCP upstream to an existing config.
	  validmind  Prompt for API key/secret and store them in an existing config.

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
		return errors.New("missing target (cursor|claude-code|amp|pi). tell me exactly what you want:\n" + hooksUsage())
	}
	if len(remaining) > 2 {
		return errors.New("too many arguments\n" + hooksUsage())
	}

	action := remaining[0]
	target := remaining[1]
	if action != "install" && action != "uninstall" {
		return fmt.Errorf("unknown hooks action %q\n%s", action, hooksUsage())
	}
	if target != "cursor" && target != "claude-code" && target != "amp" && target != "pi" {
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
	return strings.TrimSpace(`usage: atryum hooks [install|uninstall] [-y|--yes] [cursor|claude-code|amp|pi]

Commands:
  install    Install Atryum hook/plugin files and add hook commands when needed.
  uninstall  Remove Atryum hook commands or plugin files from target settings.

Targets:
  cursor       ~/.cursor/hooks.json
  claude-code  ~/.claude/settings.json
  amp          ~/.config/amp/plugins/atryum.ts
  pi           ~/.pi/agent/extensions/atryum/index.ts

Examples:
  atryum hooks install cursor
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
	if target == "amp" || target == "pi" {
		return installAgentPlugin(target)
	}

	hookScript, err := resolveHookScript()
	if err != nil {
		return err
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	dst := filepath.Join(homeDir, ".atryum", "hooks", "atryum-hook.mjs")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(dst, hookScript, 0o755); err != nil {
		return err
	}

	settingsPath, err := hookSettingsPath(target)
	if err != nil {
		return err
	}
	settings, err := readJSONMap(settingsPath)
	if err != nil {
		return err
	}
	applyInstallHookConfig(settings, target)
	if err := writeJSONMap(settingsPath, settings); err != nil {
		return err
	}

	fmt.Printf("installed hooks for %s in %s\n", target, settingsPath)
	fmt.Println("restart your editor/agent tool to apply hook changes")
	return nil
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
		return filepath.Join(home, ".claude", "settings.json"), nil
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
	hooks := ensureMap(settings, "hooks")
	if target == "cursor" {
		pre := ensureSlice(hooks, "preToolUse")
		post := ensureSlice(hooks, "postToolUse")
		pre = appendUniqueCommand(pre, map[string]any{
			"type":    "command",
			"command": "ATRYUM_HOOK_HOST=cursor ATRYUM_HOOK_EVENT=preToolUse ATRYUM_SOURCE=cursor node ~/.atryum/hooks/atryum-hook.mjs",
		})
		post = appendUniqueCommand(post, map[string]any{
			"type":    "command",
			"command": "ATRYUM_HOOK_HOST=cursor ATRYUM_HOOK_EVENT=postToolUse ATRYUM_SOURCE=cursor node ~/.atryum/hooks/atryum-hook.mjs",
		})
		hooks["preToolUse"] = pre
		hooks["postToolUse"] = post
		return
	}

	pre := ensureSlice(hooks, "PreToolUse")
	post := ensureSlice(hooks, "PostToolUse")
	pre = appendUniqueClaudeEntry(pre, "ATRYUM_HOOK_HOST=claude ATRYUM_HOOK_EVENT=PreToolUse ATRYUM_SOURCE=claude-code node ~/.atryum/hooks/atryum-hook.mjs")
	post = appendUniqueClaudeEntry(post, "ATRYUM_HOOK_HOST=claude ATRYUM_HOOK_EVENT=PostToolUse ATRYUM_SOURCE=claude-code node ~/.atryum/hooks/atryum-hook.mjs")
	hooks["PreToolUse"] = pre
	hooks["PostToolUse"] = post
}

func applyUninstallHookConfig(settings map[string]any, target string) {
	hooks := ensureMap(settings, "hooks")
	if target == "cursor" {
		hooks["preToolUse"] = removeAtryumCommands(ensureSlice(hooks, "preToolUse"))
		hooks["postToolUse"] = removeAtryumCommands(ensureSlice(hooks, "postToolUse"))
		return
	}
	hooks["PreToolUse"] = removeAtryumClaudeCommands(ensureSlice(hooks, "PreToolUse"))
	hooks["PostToolUse"] = removeAtryumClaudeCommands(ensureSlice(hooks, "PostToolUse"))
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

func appendUniqueClaudeEntry(slice []any, command string) []any {
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

func removeAtryumClaudeCommands(slice []any) []any {
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
