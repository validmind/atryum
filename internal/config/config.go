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
}

// BackendConfig configures Atryum's startup connection check to the ValidMind
// backend. Environment variables override TOML when set.
type BackendConfig struct {
	BaseURL               string `toml:"base_url"`
	MachineKey            string `toml:"machine_key"`
	MachineSecret         string `toml:"machine_secret"`
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
	ListenAddr    string `toml:"listen_addr"`
	PublicBaseURL string `toml:"public_base_url"`
	DatabasePath  string `toml:"database_path"`
	DatabaseURL   string `toml:"database_url"`
	LogLevel      string `toml:"log_level"`
}

type DefaultsConfig struct {
	RequestTimeoutSeconds      int `toml:"request_timeout_seconds"`
	ApprovalReuseWindowSeconds int `toml:"approval_reuse_window_seconds"`
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
	cfg.Backend.ApplyEnv()
	return cfg, err
}

func (c *BackendConfig) ApplyEnv() {
	if value := os.Getenv("ATRYUM_BACKEND_BASE_URL"); value != "" {
		c.BaseURL = value
	}
	if value := os.Getenv("ATRYUM_MACHINE_KEY"); value != "" {
		c.MachineKey = value
	}
	if value := os.Getenv("ATRYUM_MACHINE_SECRET"); value != "" {
		c.MachineSecret = value
	}
	if value := os.Getenv("ATRYUM_BACKEND_CONNECTION_TIMEOUT_SECONDS"); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil {
			c.ConnectionTimeoutSecs = parsed
		}
	}
	if c.ConnectionTimeoutSecs <= 0 {
		c.ConnectionTimeoutSecs = 5
	}
	if value := os.Getenv("ATRYUM_BACKEND_EVALUATE_TIMEOUT_SECONDS"); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil {
			c.EvaluateTimeoutSecs = parsed
		}
	}
	if c.EvaluateTimeoutSecs <= 0 {
		c.EvaluateTimeoutSecs = 120
	}
}
