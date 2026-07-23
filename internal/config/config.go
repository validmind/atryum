package config

import (
	"os"
	"strconv"

	"github.com/BurntSushi/toml"

	"github.com/validmind/atryum/internal/auth"
)

type Config struct {
	Server    ServerConfig     `toml:"server"`
	Backend   BackendConfig    `toml:"backend"`
	Defaults  DefaultsConfig   `toml:"defaults"`
	Policy    PolicyConfig     `toml:"policy"`
	Upstreams []UpstreamConfig `toml:"upstreams"`
	// Auth holds zero or more inbound OAuth bearer-token validators
	// (e.g. one entry for Keycloak, one for Auth0). When empty, the agent-
	// facing /mcp/ routes remain anonymous.
	Auth      []auth.Config   `toml:"auth"`
	AuthDebug AuthDebugConfig `toml:"auth_debug"`
	// APIKey protects the read-only reporting endpoints
	// (GET /invocations/{agent_id}, GET /agent_ids). When key or secret is
	// empty, those endpoints refuse every request.
	APIKey auth.APIKeyConfig `toml:"api_key"`
	// ManagedAgents configures zero or more Claude Managed Agents events
	// bridges, one per Anthropic account/workspace. Declare each as a
	// repeated `[[managed_agents]]` table. An entry with an empty api_key is
	// skipped.
	ManagedAgents []ManagedAgentsConfig `toml:"managed_agents"`
}

// ManagedAgentsConfig configures one outbound connection to Anthropic's Claude
// Managed Agents "events and streaming" API. When APIKey is empty the entry is
// skipped. Name distinguishes entries when more than one is configured and is
// used by the session-registration API to target a specific account. Workspace
// is required whenever APIKey is set; it labels the Anthropic workspace this
// account belongs to for UI display and Atryum ownership metadata. Anthropic API
// keys are already scoped by Anthropic, so this value does not retarget a key to
// a different workspace.
type ManagedAgentsConfig struct {
	Name                    string `toml:"name"`
	Workspace               string `toml:"workspace"`
	BaseURL                 string `toml:"base_url"`
	APIKey                  string `toml:"api_key"`
	PollIntervalMillis      int    `toml:"poll_interval_millis"`
	ReconnectBackoffSeconds int    `toml:"reconnect_backoff_seconds"`
	RecentChatMessagesLimit int    `toml:"recent_chat_messages_limit"`
	ClientName              string `toml:"client_name"`
	ClientVersion           string `toml:"client_version"`
}

// applyManagedAgentsEnv lets ANTHROPIC_API_KEY / ATRYUM_MANAGED_AGENTS_API_KEY
// configure the bridge without TOML in the common single-account case. The env
// key is applied only when exactly zero or one entries are configured: with one
// entry it fills an empty api_key; with none it creates a default entry. When
// multiple entries are configured each must set its own api_key in TOML.
func (c *Config) applyManagedAgentsEnv() {
	envKey := os.Getenv("ATRYUM_MANAGED_AGENTS_API_KEY")
	if envKey == "" {
		envKey = os.Getenv("ANTHROPIC_API_KEY")
	}
	if envKey == "" {
		return
	}
	envWorkspace := os.Getenv("ATRYUM_MANAGED_AGENTS_WORKSPACE")
	switch len(c.ManagedAgents) {
	case 0:
		c.ManagedAgents = []ManagedAgentsConfig{{APIKey: envKey, Workspace: envWorkspace}}
	case 1:
		if c.ManagedAgents[0].APIKey == "" {
			c.ManagedAgents[0].APIKey = envKey
		}
		if c.ManagedAgents[0].Workspace == "" {
			c.ManagedAgents[0].Workspace = envWorkspace
		}
	}
}

// BackendConfig configures Atryum's startup connection check to the ValidMind
// backend. Environment variables override TOML when set.
type BackendConfig struct {
	BaseURL               string `toml:"base_url"`
	MachineKey            string `toml:"machine_key"`
	MachineSecret         string `toml:"machine_secret"`
	APIKey                string `toml:"api_key"`
	APISecret             string `toml:"api_secret"`
	ConnectionTimeoutSecs int    `toml:"connection_timeout_seconds"`
	// EvaluateTimeoutSecs is the HTTP timeout for /evaluate calls, which invoke
	// an LLM and can take much longer than a normal API round-trip. Defaults to
	// 120 seconds when unset or zero.
	EvaluateTimeoutSecs int `toml:"evaluate_timeout_seconds"`
}

// AuthDebugConfig contains local-only auth debugging switches.
type AuthDebugConfig struct {
	SkipVerify bool `toml:"skip_verify"`
}

// PolicyConfig selects the active approval policy provider at startup.
// Valid provider values: "always_approve", "manual_approval", "always_deny".
type PolicyConfig struct {
	Provider string `toml:"provider"`
}

type ServerConfig struct {
	ListenAddr     string `toml:"listen_addr"`
	PublicBaseURL  string `toml:"public_base_url"`
	AtryumInstance string `toml:"atryum_instance"`
	DatabasePath   string `toml:"database_path"`
	DatabaseURL    string `toml:"database_url"`
	LogLevel       string `toml:"log_level"`
}

type DefaultsConfig struct {
	RequestTimeoutSeconds int `toml:"request_timeout_seconds"`

	// StreamRelayEnabled is the kill-switch for the tools/call SSE relay
	// (see api.Handler.SetStreamRelayEnabled / docs/architecture.md). The
	// relay only ever activates when the agent's own request also sends
	// Accept: text/event-stream and the upstream answers with an SSE
	// body, so leaving this on by default is safe.
	StreamRelayEnabled bool `toml:"stream_relay_enabled"`
	// StreamHeaderTimeoutSeconds bounds waiting for the upstream's
	// response headers on a streaming tools/call — the connect phase,
	// before Atryum knows whether the response will be an SSE stream.
	// Zero falls back to RequestTimeoutSeconds, preserving today's
	// connect-phase behavior.
	StreamHeaderTimeoutSeconds int `toml:"stream_header_timeout_seconds"`
	// StreamIdleTimeoutSeconds bounds the gap between successive relayed
	// events once a stream has started; it resets on every event. Unlike
	// RequestTimeoutSeconds, this does not bound the call's total
	// duration — only how long it may go without producing anything.
	StreamIdleTimeoutSeconds int `toml:"stream_idle_timeout_seconds"`
	// StreamMaxDurationSeconds bounds the whole call once a stream has
	// started. Zero disables the bound (unlimited).
	StreamMaxDurationSeconds int `toml:"stream_max_duration_seconds"`
	// StreamAuditMaxEvents caps how many invocation.stream_event audit
	// rows get persisted per call; beyond the cap, events are still
	// relayed live to the agent but only counted, not stored
	// individually. Zero disables this count cap; the bounded audit queue
	// may still drop events under storage backpressure, which is reported
	// in the stream completion audit row.
	StreamAuditMaxEvents int `toml:"stream_audit_max_events"`
	// StreamAuditMaxEventBytes truncates each persisted stream_event
	// row's data field beyond this size. Zero disables truncation.
	StreamAuditMaxEventBytes int `toml:"stream_audit_max_event_bytes"`
}

type UpstreamConfig struct {
	Name           string            `toml:"name"`
	Mode           string            `toml:"mode"`
	BaseURL        string            `toml:"base_url"`
	TimeoutSeconds int               `toml:"timeout_seconds"`
	Enabled        bool              `toml:"enabled"`
	AuthToken      string            `toml:"auth_token"`
	Command        string            `toml:"command"`
	Args           []string          `toml:"args"`
	Env            map[string]string `toml:"env"`
	// Optional pre-shared OAuth credentials. Only consulted on the
	// bootstrap path (empty DB) — once a row exists in mcp_servers these
	// are ignored and the admin form / DCR take over. Use for ASes that
	// don't support Dynamic Client Registration (Slack, GitHub OAuth
	// Apps, etc.) where the admin has registered a client out-of-band.
	OAuthClientID     string `toml:"oauth_client_id"`
	OAuthClientSecret string `toml:"oauth_client_secret"`
	OAuthAuthorizeURL string `toml:"oauth_authorize_url"`
	OAuthTokenURL     string `toml:"oauth_token_url"`
	OAuthScopes       string `toml:"oauth_scopes"`
}

func Load(path string) (Config, error) {
	cfg := Config{
		Server: ServerConfig{
			ListenAddr:   ":8080",
			DatabasePath: "./atryum.db",
			LogLevel:     "info",
		},
		Backend: BackendConfig{
			ConnectionTimeoutSecs: 5,
		},
		Defaults: DefaultsConfig{
			RequestTimeoutSeconds:    30,
			StreamRelayEnabled:       true,
			StreamIdleTimeoutSeconds: 60,
			StreamMaxDurationSeconds: 600,
			StreamAuditMaxEvents:     100,
			StreamAuditMaxEventBytes: 4096,
		},
	}
	_, err := toml.DecodeFile(path, &cfg)
	if err != nil && !os.IsNotExist(err) {
		return cfg, err
	}
	cfg.Backend.ApplyEnv()
	cfg.applyManagedAgentsEnv()
	return cfg, nil
}

func (c *BackendConfig) ApplyEnv() {
	if value := os.Getenv("VM_BASE_URL"); value != "" {
		c.BaseURL = value
	}
	if value := os.Getenv("VM_MACHINE_KEY"); value != "" {
		c.MachineKey = value
	}
	if value := os.Getenv("VM_MACHINE_SECRET"); value != "" {
		c.MachineSecret = value
	}
	if value := os.Getenv("VM_API_KEY"); value != "" {
		c.APIKey = value
	}
	if value := os.Getenv("VM_API_SECRET"); value != "" {
		c.APISecret = value
	}
	if value := os.Getenv("VM_CONNECTION_TIMEOUT_SECONDS"); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil {
			c.ConnectionTimeoutSecs = parsed
		}
	}
	if c.ConnectionTimeoutSecs <= 0 {
		c.ConnectionTimeoutSecs = 5
	}
	if value := os.Getenv("VM_EVALUATE_TIMEOUT_SECONDS"); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil {
			c.EvaluateTimeoutSecs = parsed
		}
	}
	if c.EvaluateTimeoutSecs <= 0 {
		c.EvaluateTimeoutSecs = 120
	}
}
