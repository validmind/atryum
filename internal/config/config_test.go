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
database_path = "./atryum.db"
database_url = "postgresql://postgres:password@127.0.0.1:5432/postgres"
log_level = "debug"
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
}
