package config

import (
	"github.com/BurntSushi/toml"

	"atryum/internal/auth"
)

type Config struct {
	Server    ServerConfig     `toml:"server"`
	Defaults  DefaultsConfig   `toml:"defaults"`
	Policy    PolicyConfig     `toml:"policy"`
	Upstreams []UpstreamConfig `toml:"upstreams"`
	// Auth holds zero or more inbound OAuth bearer-token validators
	// (e.g. one entry for Keycloak, one for Auth0). When empty, the agent-
	// facing /mcp/ routes remain anonymous.
	Auth []auth.Config `toml:"auth"`
}

// PolicyConfig selects the active approval policy provider at startup.
// Valid provider values: "always_approve", "manual_approval", "always_deny".
type PolicyConfig struct {
	Provider string `toml:"provider"`
}

type ServerConfig struct {
	ListenAddr   string `toml:"listen_addr"`
	DatabasePath string `toml:"database_path"`
	DatabaseURL  string `toml:"database_url"`
	LogLevel     string `toml:"log_level"`
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
}

func Load(path string) (Config, error) {
	cfg := Config{
		Server: ServerConfig{
			ListenAddr:   ":8080",
			DatabasePath: "./atryum.db",
			LogLevel:     "info",
		},
		Defaults: DefaultsConfig{
			RequestTimeoutSeconds: 30,
		},
	}
	_, err := toml.DecodeFile(path, &cfg)
	return cfg, err
}
