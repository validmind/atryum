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
}

func TestLoadMissingConfigUsesDefaultsAndEnv(t *testing.T) {
	t.Setenv("VM_API_KEY", "env-api-key")
	t.Setenv("VM_API_SECRET", "env-api-secret")

	cfg, err := Load(filepath.Join(t.TempDir(), "missing.toml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.ListenAddr != ":8080" {
		t.Fatalf("ListenAddr = %q", cfg.Server.ListenAddr)
	}
	if cfg.Server.DatabasePath != "./atryum.db" {
		t.Fatalf("DatabasePath = %q", cfg.Server.DatabasePath)
	}
	if cfg.Backend.BaseURL != "" {
		t.Fatalf("Backend.BaseURL = %q, want empty (no default)", cfg.Backend.BaseURL)
	}
	if cfg.Backend.APIKey != "env-api-key" {
		t.Fatalf("Backend.APIKey = %q", cfg.Backend.APIKey)
	}
	if cfg.Backend.APISecret != "env-api-secret" {
		t.Fatalf("Backend.APISecret = %q", cfg.Backend.APISecret)
	}
}
