package atryum

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/validmind/atryum/internal/api"
)

// defaultServerURL is used when no --url, ATRYUM_URL, or stored login exists.
const defaultServerURL = "http://localhost:8080"

// agentKeyFile is where `atryum setup claude` stores the agent's API key.
const agentKeyFile = "agent-key"

// cliIO bundles the streams the user-facing commands write to and read from,
// so tests can drive them without a terminal.
type cliIO struct {
	out io.Writer
	in  *bufio.Reader
}

func stdIO() cliIO {
	return cliIO{out: os.Stdout, in: bufio.NewReader(os.Stdin)}
}

// resolveServerURL picks the Atryum server: explicit flag, then ATRYUM_URL,
// then the only stored login (if exactly one), then the default.
func resolveServerURL(flagValue string) string {
	if v := normalizeServerURL(flagValue); v != "" {
		return v
	}
	if v := normalizeServerURL(os.Getenv("ATRYUM_URL")); v != "" {
		return v
	}
	if store, _, err := loadCredentials(); err == nil && len(store.Servers) == 1 {
		for url := range store.Servers {
			return url
		}
	}
	return defaultServerURL
}

// sessionFor returns a client for server whose bearer token comes from the
// stored login, refreshed when expired. In a no-auth deployment (the server
// advertises no login providers) requests go out without a token.
func sessionFor(ctx context.Context, server string, io cliIO, loginIfNeeded bool) (*atryumClient, error) {
	client := newAtryumClient(server, nil)
	cfg, err := client.authConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("cannot reach %s: %w", server, err)
	}
	if len(cfg.Providers) == 0 {
		return client, nil // no-auth deployment
	}
	store, _, err := loadCredentials()
	if err != nil {
		return nil, err
	}
	creds, ok := store.Servers[server]
	if !ok || (creds.expired(time.Now()) && creds.RefreshToken == "") {
		if !loginIfNeeded {
			return nil, fmt.Errorf("not logged in to %s; run `atryum login --url %s`", server, server)
		}
		creds, err = runDeviceLogin(ctx, io, server, cfg, "")
		if err != nil {
			return nil, err
		}
	}
	client.token = func(ctx context.Context) (string, error) {
		store, _, err := loadCredentials()
		if err != nil {
			return "", err
		}
		current, ok := store.Servers[server]
		if !ok {
			return "", fmt.Errorf("not logged in to %s", server)
		}
		if !current.expired(time.Now()) {
			return current.AccessToken, nil
		}
		refreshed, err := refreshCredentials(ctx, client.http, current)
		if err != nil {
			return "", err
		}
		store.Servers[server] = refreshed
		if err := saveCredentials(store); err != nil {
			return "", err
		}
		return refreshed.AccessToken, nil
	}
	return client, nil
}

// runDeviceLogin performs the device flow against one of the server's login
// providers and persists the result.
func runDeviceLogin(ctx context.Context, io cliIO, server string, cfg api.AuthConfigResponse, providerID string) (serverCredentials, error) {
	if len(cfg.Providers) == 0 {
		return serverCredentials{}, fmt.Errorf("%s has no login providers configured (inbound auth is disabled); nothing to log in to", server)
	}
	provider := cfg.Providers[0]
	if providerID != "" {
		found := false
		for _, p := range cfg.Providers {
			if p.ID == providerID || p.Provider == providerID {
				provider, found = p, true
				break
			}
		}
		if !found {
			names := make([]string, 0, len(cfg.Providers))
			for _, p := range cfg.Providers {
				names = append(names, p.ID)
			}
			return serverCredentials{}, fmt.Errorf("unknown provider %q; available: %s", providerID, strings.Join(names, ", "))
		}
	} else if len(cfg.Providers) > 1 {
		fmt.Fprintln(io.out, "Multiple login providers are configured:")
		for i, p := range cfg.Providers {
			fmt.Fprintf(io.out, "  %d) %s\n", i+1, p.Name)
		}
		choice, err := promptWithDefault(io.in, "Choose a provider", "1")
		if err != nil {
			return serverCredentials{}, err
		}
		idx, err := strconv.Atoi(strings.TrimSpace(choice))
		if err != nil || idx < 1 || idx > len(cfg.Providers) {
			return serverCredentials{}, fmt.Errorf("invalid choice %q", choice)
		}
		provider = cfg.Providers[idx-1]
	}

	fmt.Fprintf(io.out, "Logging in to %s via %s\n", server, provider.Name)
	creds, err := deviceFlowLogin(ctx, &http.Client{Timeout: 30 * time.Second}, io.out, provider)
	if err != nil {
		return serverCredentials{}, err
	}
	store, _, err := loadCredentials()
	if err != nil {
		return serverCredentials{}, err
	}
	store.Servers[server] = creds
	if err := saveCredentials(store); err != nil {
		return serverCredentials{}, err
	}
	fmt.Fprintln(io.out, "Logged in.")
	return creds, nil
}

// ─── atryum login / logout / whoami ──────────────────────────────────────────

func loginUsage() string {
	return strings.TrimSpace(`usage: atryum login [--url URL] [--provider ID]

Signs in to an Atryum server with your identity provider using the OAuth
device flow, and stores the session in ~/.atryum/credentials.json (0600).

Options:
  --url URL        Atryum server (default: $ATRYUM_URL or http://localhost:8080).
  --provider ID    Login provider id when the server offers several.`)
}

func runLogin(args []string, io cliIO) error {
	if hasHelpArg(args) {
		fmt.Fprintln(io.out, loginUsage())
		return nil
	}
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	fs.SetOutput(ioDiscard())
	urlFlag := fs.String("url", "", "atryum server url")
	providerFlag := fs.String("provider", "", "provider id")
	if err := fs.Parse(args); err != nil {
		return errors.New(loginUsage())
	}
	server := resolveServerURL(*urlFlag)
	ctx := context.Background()
	client := newAtryumClient(server, nil)
	cfg, err := client.authConfig(ctx)
	if err != nil {
		return fmt.Errorf("cannot reach %s: %w", server, err)
	}
	if _, err := runDeviceLogin(ctx, io, server, cfg, *providerFlag); err != nil {
		return err
	}
	session, err := sessionFor(ctx, server, io, false)
	if err != nil {
		return err
	}
	me, err := session.me(ctx)
	if err != nil {
		return fmt.Errorf("login succeeded but /api/v1/me failed: %w", err)
	}
	fmt.Fprintf(io.out, "Signed in as %s (%s)\n", firstNonEmpty(me.Email, me.Name, me.Subject), me.Role)
	return nil
}

func runLogout(args []string, io cliIO) error {
	fs := flag.NewFlagSet("logout", flag.ContinueOnError)
	fs.SetOutput(ioDiscard())
	urlFlag := fs.String("url", "", "atryum server url")
	if err := fs.Parse(args); err != nil {
		return errors.New("usage: atryum logout [--url URL]")
	}
	server := resolveServerURL(*urlFlag)
	store, _, err := loadCredentials()
	if err != nil {
		return err
	}
	if _, ok := store.Servers[server]; !ok {
		fmt.Fprintf(io.out, "No stored login for %s\n", server)
		return nil
	}
	delete(store.Servers, server)
	if err := saveCredentials(store); err != nil {
		return err
	}
	fmt.Fprintf(io.out, "Logged out of %s\n", server)
	return nil
}

func runWhoami(args []string, io cliIO) error {
	fs := flag.NewFlagSet("whoami", flag.ContinueOnError)
	fs.SetOutput(ioDiscard())
	urlFlag := fs.String("url", "", "atryum server url")
	if err := fs.Parse(args); err != nil {
		return errors.New("usage: atryum whoami [--url URL]")
	}
	server := resolveServerURL(*urlFlag)
	ctx := context.Background()
	session, err := sessionFor(ctx, server, io, false)
	if err != nil {
		return err
	}
	me, err := session.me(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(io.out, "server:  %s\n", server)
	fmt.Fprintf(io.out, "user:    %s\n", firstNonEmpty(me.Email, me.Name, me.Subject, "(anonymous)"))
	fmt.Fprintf(io.out, "role:    %s\n", me.Role)
	fmt.Fprintf(io.out, "method:  %s\n", me.Method)
	if me.UserID != "" {
		fmt.Fprintf(io.out, "user_id: %s\n", me.UserID)
	}
	return nil
}

// ─── atryum agent ... ────────────────────────────────────────────────────────

func agentUsage() string {
	return strings.TrimSpace(`usage: atryum agent <command> [options]

Commands:
  list                                  List agents you can see.
  create NAME [--description TEXT]      Create an agent (you become its owner).
  key list AGENT                        List API keys for an agent.
  key create AGENT [--name N] [--expires 30d] [--save PATH]
                                        Issue a new API key; the token prints once.
  key revoke AGENT KEY_ID               Revoke a key.

AGENT is an agent name or id. All commands accept --url URL.

Examples:
  atryum agent list
  atryum agent create "Claude on my laptop"
  atryum agent key create "Claude on my laptop" --name laptop --expires 90d
  atryum agent key revoke "Claude on my laptop" 7f3c...`)
}

func runAgent(args []string, io cliIO) error {
	if len(args) == 0 || hasHelpArg(args) {
		fmt.Fprintln(io.out, agentUsage())
		if len(args) == 0 {
			return errors.New("missing agent command")
		}
		return nil
	}
	ctx := context.Background()
	switch args[0] {
	case "list":
		fs := flag.NewFlagSet("agent list", flag.ContinueOnError)
		fs.SetOutput(ioDiscard())
		urlFlag := fs.String("url", "", "atryum server url")
		if err := fs.Parse(args[1:]); err != nil {
			return errors.New(agentUsage())
		}
		session, err := sessionFor(ctx, resolveServerURL(*urlFlag), io, false)
		if err != nil {
			return err
		}
		agents, err := session.listAgents(ctx)
		if err != nil {
			return err
		}
		if len(agents) == 0 {
			fmt.Fprintln(io.out, "No agents visible. Create one with `atryum agent create NAME`.")
			return nil
		}
		tw := tabwriter.NewWriter(io.out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tNAME\tENABLED\tSOURCE")
		for _, a := range agents {
			source := "local"
			if a.Synced {
				source = "validmind"
			}
			fmt.Fprintf(tw, "%s\t%s\t%t\t%s\n", a.CUID, a.Name, a.Enabled, source)
		}
		return tw.Flush()

	case "create":
		fs := flag.NewFlagSet("agent create", flag.ContinueOnError)
		fs.SetOutput(ioDiscard())
		urlFlag := fs.String("url", "", "atryum server url")
		desc := fs.String("description", "", "description")
		pos, err := parseInterspersed(fs, args[1:])
		if err != nil || len(pos) != 1 {
			return errors.New(agentUsage())
		}
		session, err := sessionFor(ctx, resolveServerURL(*urlFlag), io, false)
		if err != nil {
			return err
		}
		agent, err := session.createAgent(ctx, pos[0], *desc)
		if err != nil {
			return err
		}
		fmt.Fprintf(io.out, "created agent %q (%s)\n", agent.Name, agent.CUID)
		return nil

	case "key":
		return runAgentKey(ctx, args[1:], io)

	default:
		return fmt.Errorf("unknown agent command %q\n%s", args[0], agentUsage())
	}
}

func runAgentKey(ctx context.Context, args []string, io cliIO) error {
	if len(args) == 0 {
		return errors.New(agentUsage())
	}
	switch args[0] {
	case "list":
		fs := flag.NewFlagSet("agent key list", flag.ContinueOnError)
		fs.SetOutput(ioDiscard())
		urlFlag := fs.String("url", "", "atryum server url")
		pos, err := parseInterspersed(fs, args[1:])
		if err != nil || len(pos) != 1 {
			return errors.New(agentUsage())
		}
		session, err := sessionFor(ctx, resolveServerURL(*urlFlag), io, false)
		if err != nil {
			return err
		}
		agent, err := lookupAgent(ctx, session, pos[0])
		if err != nil {
			return err
		}
		keys, err := session.listKeys(ctx, agent.CUID)
		if err != nil {
			return err
		}
		if len(keys) == 0 {
			fmt.Fprintf(io.out, "No keys for %q.\n", agent.Name)
			return nil
		}
		tw := tabwriter.NewWriter(io.out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "KEY_ID\tNAME\tPREFIX\tSTATUS\tCREATED\tLAST_USED\tEXPIRES")
		for _, k := range keys {
			fmt.Fprintf(tw, "%s\t%s\t%s…\t%s\t%s\t%s\t%s\n", k.ID, k.Name, k.KeyPrefix, keyStatusLabel(k), k.CreatedAt.Format(time.RFC3339), fmtTimePtr(k.LastUsedAt), fmtTimePtr(k.ExpiresAt))
		}
		return tw.Flush()

	case "create":
		fs := flag.NewFlagSet("agent key create", flag.ContinueOnError)
		fs.SetOutput(ioDiscard())
		urlFlag := fs.String("url", "", "atryum server url")
		name := fs.String("name", "", "key name")
		expires := fs.String("expires", "", "lifetime such as 30d or 720h")
		save := fs.String("save", "", "write the token to this file (0600) instead of printing it")
		pos, err := parseInterspersed(fs, args[1:])
		if err != nil || len(pos) != 1 {
			return errors.New(agentUsage())
		}
		session, err := sessionFor(ctx, resolveServerURL(*urlFlag), io, false)
		if err != nil {
			return err
		}
		agent, err := lookupAgent(ctx, session, pos[0])
		if err != nil {
			return err
		}
		keyName := *name
		if keyName == "" {
			keyName = defaultKeyName()
		}
		key, err := session.createKey(ctx, agent.CUID, keyName, *expires)
		if err != nil {
			return err
		}
		if *save != "" {
			if err := writeSecretFile(*save, []byte(key.Token+"\n")); err != nil {
				return err
			}
			fmt.Fprintf(io.out, "created key %q for agent %q; token saved to %s\n", key.Name, agent.Name, *save)
			return nil
		}
		fmt.Fprintf(io.out, "created key %q for agent %q (id %s)\n\n", key.Name, agent.Name, key.ID)
		fmt.Fprintf(io.out, "%s\n\n", key.Token)
		fmt.Fprintln(io.out, "Copy it now; it will not be shown again. Use it as `Authorization: Bearer <token>`.")
		return nil

	case "revoke":
		fs := flag.NewFlagSet("agent key revoke", flag.ContinueOnError)
		fs.SetOutput(ioDiscard())
		urlFlag := fs.String("url", "", "atryum server url")
		pos, err := parseInterspersed(fs, args[1:])
		if err != nil || len(pos) != 2 {
			return errors.New(agentUsage())
		}
		session, err := sessionFor(ctx, resolveServerURL(*urlFlag), io, false)
		if err != nil {
			return err
		}
		agent, err := lookupAgent(ctx, session, pos[0])
		if err != nil {
			return err
		}
		if err := session.revokeKey(ctx, agent.CUID, pos[1]); err != nil {
			return err
		}
		fmt.Fprintf(io.out, "revoked key %s on agent %q\n", pos[1], agent.Name)
		return nil

	default:
		return fmt.Errorf("unknown agent key command %q\n%s", args[0], agentUsage())
	}
}

func lookupAgent(ctx context.Context, session *atryumClient, ref string) (api.OperatorAgent, error) {
	agents, err := session.listAgents(ctx)
	if err != nil {
		return api.OperatorAgent{}, err
	}
	return resolveAgent(agents, ref)
}

func keyStatusLabel(k api.OperatorAPIKey) string {
	switch {
	case k.RevokedAt != nil:
		return "revoked"
	case k.ExpiresAt != nil && !time.Now().Before(*k.ExpiresAt):
		return "expired"
	default:
		return "active"
	}
}

func fmtTimePtr(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return t.Format(time.RFC3339)
}

func defaultKeyName() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "cli"
	}
	return host
}

// ─── atryum setup claude ─────────────────────────────────────────────────────

func setupClaudeUsage() string {
	return strings.TrimSpace(`usage: atryum setup claude [--url URL] [--agent NAME|ID] [-y|--yes] [--allow-insecure-http]

One-shot Claude Code onboarding:
  1. signs in to Atryum (OAuth device flow) if the server requires it,
  2. picks or creates the agent this machine will act as,
  3. issues an API key for it and stores it in ~/.atryum/agent-key (0600),
  4. installs the Claude Code hooks pointed at the server and that key.

Options:
  --url URL          Atryum server (default: $ATRYUM_URL or http://localhost:8080).
  --agent NAME|ID    Agent to act as; created if the name does not exist.
                     Defaults to "Claude Code on <hostname>".
  -y, --yes          Do not prompt for confirmation.
  --allow-insecure-http
                     Permit a plain http:// server that is not on this machine.
                     The key travels as a bearer token, so this is refused by default.

Running several Claude Codes as different agents:
  Point each at its own Atryum home and Claude config directory. The key, the
  hook script and the hook state all move together, and the hooks are written
  into that Claude config's settings.json:

    ATRYUM_HOME=~/.atryum-b CLAUDE_CONFIG_DIR=~/.claude-b atryum setup claude
    ATRYUM_HOME=~/.atryum-b CLAUDE_CONFIG_DIR=~/.claude-b claude

  Without --agent the default agent name gains the home's basename so the two
  instances do not share an agent record.`)
}

func runSetupClaude(args []string, io cliIO) error {
	if hasHelpArg(args) {
		fmt.Fprintln(io.out, setupClaudeUsage())
		return nil
	}
	fs := flag.NewFlagSet("setup claude", flag.ContinueOnError)
	fs.SetOutput(ioDiscard())
	urlFlag := fs.String("url", "", "atryum server url")
	agentFlag := fs.String("agent", "", "agent name or id")
	autoYes := fs.Bool("y", false, "auto-confirm")
	autoYesLong := fs.Bool("yes", false, "auto-confirm")
	allowInsecure := fs.Bool("allow-insecure-http", false, "permit a non-loopback http:// server")
	if err := fs.Parse(args); err != nil {
		return errors.New(setupClaudeUsage())
	}
	confirmed := *autoYes || *autoYesLong
	server := resolveServerURL(*urlFlag)
	ctx := context.Background()

	if insecureTransport(server) && !*allowInsecure {
		return fmt.Errorf("refusing to send an API key over plain http to %s; use an https:// URL, or pass --allow-insecure-http if this network is trusted", server)
	}

	fmt.Fprintf(io.out, "Atryum server: %s\n", server)
	session, err := sessionFor(ctx, server, io, true)
	if err != nil {
		return err
	}
	me, err := session.me(ctx)
	if err != nil {
		return err
	}
	if me.Method != "none" {
		fmt.Fprintf(io.out, "Signed in as %s (%s)\n", firstNonEmpty(me.Email, me.Name, me.Subject), me.Role)
	}

	// Pick or create the agent.
	agents, err := session.listAgents(ctx)
	if err != nil {
		return err
	}
	agentRef := strings.TrimSpace(*agentFlag)
	if agentRef == "" {
		agentRef = "Claude Code on " + instanceLabel()
	}
	agent, err := resolveAgent(agents, agentRef)
	if err != nil {
		var created bool
		if !confirmed {
			ok, perr := promptConfirm(io.in, fmt.Sprintf("Create agent %q", agentRef))
			if perr != nil {
				return perr
			}
			if !ok {
				return errors.New("aborted")
			}
		}
		agent, err = session.createAgent(ctx, agentRef, "Created by `atryum setup claude`")
		if err != nil {
			return fmt.Errorf("create agent: %w", err)
		}
		created = true
		fmt.Fprintf(io.out, "Created agent %q (%s)\n", agent.Name, agent.CUID)
		_ = created
	} else {
		fmt.Fprintf(io.out, "Using agent %q (%s)\n", agent.Name, agent.CUID)
	}

	// Issue the key and store it.
	home, err := atryumHomeDir()
	if err != nil {
		return err
	}
	keyPath := filepath.Join(home, agentKeyFile)
	if !confirmed {
		ok, err := promptConfirm(io.in, fmt.Sprintf("Issue an API key for %q, save it to %s, and install Claude Code hooks", agent.Name, keyPath))
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("aborted")
		}
	}
	key, err := session.createKey(ctx, agent.CUID, "claude-code on "+instanceLabel(), "")
	if err != nil {
		return fmt.Errorf("create key: %w", err)
	}
	if err := writeSecretFile(keyPath, []byte(key.Token+"\n")); err != nil {
		return err
	}
	fmt.Fprintf(io.out, "Saved API key %s… to %s\n", key.KeyPrefix, keyPath)

	// Install hooks pointed at this server and key. installHooksWithEnv adds
	// ATRYUM_STATE_DIR itself when ATRYUM_HOME is not the default.
	env := map[string]string{
		"ATRYUM_URL":           server,
		"ATRYUM_TOKEN_COMMAND": "cat " + shellQuote(keyPath),
	}
	if *allowInsecure && insecureTransport(server) {
		env["ATRYUM_ALLOW_INSECURE_HTTP"] = "1"
	}
	if err := installHooksWithEnv("claude-code", env, io.out); err != nil {
		return err
	}
	fmt.Fprintln(io.out, "\nDone. Claude Code will authenticate to Atryum as this agent.")
	fmt.Fprintf(io.out, "Revoke access any time with: atryum agent key revoke %q %s\n", agent.Name, key.ID)
	return nil
}

// instanceLabel names this machine for default agent and key names. When
// ATRYUM_HOME points somewhere other than ~/.atryum the home's basename is
// appended, so two Claude Codes set up on one host with different homes get
// different agent records instead of silently sharing one.
func instanceLabel() string {
	label := defaultKeyName()
	home, err := atryumHomeDir()
	if err != nil || isDefaultAtryumHome(home) {
		return label
	}
	base := strings.TrimPrefix(filepath.Base(filepath.Clean(home)), ".")
	if base == "" || base == "atryum" {
		return label
	}
	return label + " (" + base + ")"
}

// insecureTransport reports whether serverURL would carry a bearer token in
// the clear to another machine: plain http to anything but a loopback host.
// Loopback http is allowed because that is the local development default.
func insecureTransport(serverURL string) bool {
	u, err := url.Parse(strings.TrimSpace(serverURL))
	if err != nil || !strings.EqualFold(u.Scheme, "http") {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return false
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return false
	}
	return true
}

// shellQuote single-quotes a path for the POSIX shell the hook runs under.
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	if !strings.ContainsAny(s, " \t\n'\"\\$`!*?[](){}<>|;&") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func ioDiscard() io.Writer { return io.Discard }

// parseInterspersed parses flags that may appear before or after positional
// arguments (Go's flag package stops at the first positional). It returns the
// positionals in order.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return positional, nil
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
}
