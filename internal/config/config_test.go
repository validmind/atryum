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

func TestLoadAuthAdminClaimValueAcceptsBool(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "atryum.toml")
	if err := os.WriteFile(path, []byte(`[[auth]]
enabled = true
issuer = "https://idp.example/"
audience = "atryum"
admin_enabled = true
admin_client_id = "admin-client"
admin_claim = "atryum_admin"
admin_claim_value = true
`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Auth[0].Normalized().AdminClaimValue; got != "true" {
		t.Fatalf("AdminClaimValue = %q", got)
	}
}

func TestLoadManagedAgentsRecentChatMessagesLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "atryum.toml")
	if err := os.WriteFile(path, []byte(`[[managed_agents]]
name = "default"
workspace = "workspace"
api_key = "sk-test"
recent_chat_messages_limit = 42
`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.ManagedAgents) != 1 {
		t.Fatalf("managed agents count = %d", len(cfg.ManagedAgents))
	}
	if cfg.ManagedAgents[0].RecentChatMessagesLimit != 42 {
		t.Fatalf("RecentChatMessagesLimit = %d", cfg.ManagedAgents[0].RecentChatMessagesLimit)
	}
}

func TestLoadPlansTTLBounds(t *testing.T) {
	dir := t.TempDir()

	write := func(name, body string) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("write config: %v", err)
		}
		return path
	}

	cfg, err := Load(filepath.Join(dir, "missing.toml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Plans.DefaultTTLSeconds != 3600 || cfg.Plans.MaxTTLSeconds != 86400 {
		t.Fatalf("built-in plan TTL bounds = %+v", cfg.Plans)
	}

	cfg, err = Load(write("explicit.toml", "[plans]\ndefault_ttl_seconds = 300\nmax_ttl_seconds = 1200\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Plans.DefaultTTLSeconds != 300 || cfg.Plans.MaxTTLSeconds != 1200 {
		t.Fatalf("explicit plan TTL bounds = %+v", cfg.Plans)
	}

	// When only the ceiling is set below the built-in default, the default
	// follows it down instead of erroring on a value the operator never wrote.
	cfg, err = Load(write("low-max.toml", "[plans]\nmax_ttl_seconds = 600\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Plans.DefaultTTLSeconds != 600 || cfg.Plans.MaxTTLSeconds != 600 {
		t.Fatalf("low-max plan TTL bounds = %+v", cfg.Plans)
	}

	if _, err = Load(write("contradictory.toml", "[plans]\ndefault_ttl_seconds = 700\nmax_ttl_seconds = 600\n")); err == nil {
		t.Fatal("default above max must fail to load")
	}
	if _, err = Load(write("negative.toml", "[plans]\nmax_ttl_seconds = -1\n")); err == nil {
		t.Fatal("negative ttl must fail to load")
	}
}
