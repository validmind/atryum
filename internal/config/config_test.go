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
	if !cfg.Defaults.StreamRelayEnabled {
		t.Fatal("Defaults.StreamRelayEnabled = false, want true (safe default: doubly gated on agent Accept + upstream content-type)")
	}
	if cfg.Defaults.StreamIdleTimeoutSeconds != 60 {
		t.Fatalf("Defaults.StreamIdleTimeoutSeconds = %d, want 60", cfg.Defaults.StreamIdleTimeoutSeconds)
	}
	if cfg.Defaults.StreamMaxDurationSeconds != 600 {
		t.Fatalf("Defaults.StreamMaxDurationSeconds = %d, want 600", cfg.Defaults.StreamMaxDurationSeconds)
	}
	if cfg.Defaults.StreamAuditMaxEvents != 100 {
		t.Fatalf("Defaults.StreamAuditMaxEvents = %d, want 100", cfg.Defaults.StreamAuditMaxEvents)
	}
	if cfg.Defaults.StreamAuditMaxEventBytes != 4096 {
		t.Fatalf("Defaults.StreamAuditMaxEventBytes = %d, want 4096", cfg.Defaults.StreamAuditMaxEventBytes)
	}
	if cfg.Defaults.StreamMaxMessageBytes != 4*1024*1024 {
		t.Fatalf("Defaults.StreamMaxMessageBytes = %d, want 4194304", cfg.Defaults.StreamMaxMessageBytes)
	}
	if cfg.Defaults.StreamHeaderTimeoutSeconds != 0 {
		t.Fatalf("Defaults.StreamHeaderTimeoutSeconds = %d, want 0 (falls back to RequestTimeoutSeconds at the call site)", cfg.Defaults.StreamHeaderTimeoutSeconds)
	}
}

func TestLoadStreamRelayCanBeDisabledViaTOML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "atryum.toml")
	if err := os.WriteFile(path, []byte("[defaults]\nstream_relay_enabled = false\nstream_idle_timeout_seconds = 30\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Defaults.StreamRelayEnabled {
		t.Fatal("Defaults.StreamRelayEnabled = true, want false (explicitly disabled in TOML)")
	}
	if cfg.Defaults.StreamIdleTimeoutSeconds != 30 {
		t.Fatalf("Defaults.StreamIdleTimeoutSeconds = %d, want 30 (explicit TOML override)", cfg.Defaults.StreamIdleTimeoutSeconds)
	}
	// Fields the TOML fragment didn't mention keep their Go-level default,
	// proving partial overrides don't blow away the rest of [defaults].
	if cfg.Defaults.StreamMaxDurationSeconds != 600 {
		t.Fatalf("Defaults.StreamMaxDurationSeconds = %d, want 600 (untouched default)", cfg.Defaults.StreamMaxDurationSeconds)
	}
	if cfg.Defaults.RequestTimeoutSeconds != 30 {
		t.Fatalf("Defaults.RequestTimeoutSeconds = %d, want 30 (untouched default)", cfg.Defaults.RequestTimeoutSeconds)
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
