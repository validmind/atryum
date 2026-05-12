package config

import "github.com/BurntSushi/toml"

type Config struct {
	Server    ServerConfig     `toml:"server"`
	Defaults  DefaultsConfig   `toml:"defaults"`
	Policy    PolicyConfig     `toml:"policy"`
	Upstreams []UpstreamConfig `toml:"upstreams"`
}

// PolicyConfig selects the active approval policy provider at startup.
// Valid provider values: "always_approve", "manual_approval", "always_deny", "timed_approve".
// DurationMinutes and TimedApproveFallback are only used by "timed_approve".
// TimedApproveFallback defaults to "manual_approval" when empty.
type PolicyConfig struct {
	Provider             string `toml:"provider"`
	DurationMinutes      int    `toml:"duration_minutes"`
	TimedApproveFallback string `toml:"timed_approve_fallback"`
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
