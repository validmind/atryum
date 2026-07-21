package config

import (
	"os"
	"strconv"

	"github.com/BurntSushi/toml"

	"atryum/internal/auth"
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
	// OTEL configures OpenTelemetry trace export. Disabled by default.
	OTEL OTELConfig `toml:"otel"`
}

// OTELConfig configures OpenTelemetry trace export over OTLP. When Enabled is
// false the tracer provider is a no-op and instrumentation has zero cost.
type OTELConfig struct {
	Enabled bool `toml:"enabled"`
	// Environment sets the deployment.environment resource attribute (Datadog
	// reads it as the `env` tag). Empty → atryum's instance identity
	// (atryum_instance, else public_base_url).
	Environment string `toml:"environment"`
	// Exporters lists the OTLP/HTTP destinations. Multiple entries push the same
	// spans to several backends at once (e.g. Langfuse + Datadog). Vendor-neutral:
	// each carries a raw endpoint + headers, no per-vendor code.
	Exporters []OTLPExporterConfig `toml:"exporters"`
	// CaptureContent gates whether the judge's prompt and completion TEXT is
	// attached to gen_ai spans (nil → true). Token counts, model, latency and
	// verdict are always emitted; this only controls the message content, which
	// carries the charter chain and the agent's tool arguments. Set false to keep
	// that content out of the external telemetry backend.
	CaptureContent *bool `toml:"capture_content"`
}

// OTLPExporterConfig is one OTLP/HTTP trace destination. The endpoint's scheme
// decides transport security (http → plaintext, https → TLS).
type OTLPExporterConfig struct {
	// Name labels the exporter in logs and error messages only, e.g. "langfuse"
	// or "datadog".
	Name     string `toml:"name"`
	Endpoint string `toml:"endpoint"`
	// Headers are sent verbatim on every export, e.g. a Datadog "DD-API-KEY".
	Headers map[string]string `toml:"headers"`
	// PublicKey/SecretKey are a convenience for backends that authenticate with a
	// Basic-auth key pair (Langfuse): when both are set, Atryum sends
	// Authorization: Basic base64(public_key:secret_key). The key pair selects the
	// Langfuse project, so a different pair per deployment routes per environment.
	// An explicit Authorization header in Headers wins over these.
	PublicKey string `toml:"public_key"`
	SecretKey string `toml:"secret_key"`
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
			RequestTimeoutSeconds: 30,
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
