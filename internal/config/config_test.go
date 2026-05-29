package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadServerDatabaseURL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "atryum.toml")
	if err := os.WriteFile(path, []byte(`[server]
listen_addr = ":9090"
public_base_url = "https://atryum.example.com"
database_path = "./atryum.db"
database_url = "postgresql://postgres:password@127.0.0.1:5432/postgres"
log_level = "debug"

[backend]
base_url = "https://backend.example"
machine_key = "toml-key"
machine_secret = "toml-secret"
connection_timeout_seconds = 7
`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.DatabaseURL != "postgresql://postgres:password@127.0.0.1:5432/postgres" {
		t.Fatalf("DatabaseURL = %q", cfg.Server.DatabaseURL)
	}
	if cfg.Server.PublicBaseURL != "https://atryum.example.com" {
		t.Fatalf("PublicBaseURL = %q", cfg.Server.PublicBaseURL)
	}
	if cfg.Backend.BaseURL != "https://backend.example" {
		t.Fatalf("Backend.BaseURL = %q", cfg.Backend.BaseURL)
	}
	if cfg.Backend.MachineKey != "toml-key" {
		t.Fatalf("Backend.MachineKey = %q", cfg.Backend.MachineKey)
	}
	if cfg.Backend.MachineSecret != "toml-secret" {
		t.Fatalf("Backend.MachineSecret = %q", cfg.Backend.MachineSecret)
	}
	if cfg.Backend.ConnectionTimeoutSecs != 7 {
		t.Fatalf("Backend.ConnectionTimeoutSecs = %d", cfg.Backend.ConnectionTimeoutSecs)
	}
	if cfg.Defaults.ApprovalReuseWindowSeconds != 10 {
		t.Fatalf("Defaults.ApprovalReuseWindowSeconds = %d", cfg.Defaults.ApprovalReuseWindowSeconds)
	}
}
