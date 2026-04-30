package config

import "github.com/BurntSushi/toml"

type Config struct {
	Server    ServerConfig     `toml:"server"`
	Defaults  DefaultsConfig   `toml:"defaults"`
	Upstreams []UpstreamConfig `toml:"upstreams"`
}

type ServerConfig struct {
	ListenAddr   string `toml:"listen_addr"`
	DatabasePath string `toml:"database_path"`
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
