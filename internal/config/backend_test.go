package config

import "testing"

func TestBackendConfigApplyEnvOverridesToml(t *testing.T) {
	t.Setenv("VM_BASE_URL", "https://backend.example")
	t.Setenv("VM_MACHINE_KEY", "env-key")
	t.Setenv("VM_MACHINE_SECRET", "env-secret")
	t.Setenv("VM_API_KEY", "env-api-key")
	t.Setenv("VM_API_SECRET", "env-api-secret")
	t.Setenv("VM_CONNECTION_TIMEOUT_SECONDS", "9")

	cfg := BackendConfig{
		BaseURL:               "https://toml.example",
		MachineKey:            "toml-key",
		MachineSecret:         "toml-secret",
		ConnectionTimeoutSecs: 3,
	}

	cfg.ApplyEnv()

	if cfg.BaseURL != "https://backend.example" {
		t.Fatalf("BaseURL = %q", cfg.BaseURL)
	}
	if cfg.MachineKey != "env-key" {
		t.Fatalf("MachineKey = %q", cfg.MachineKey)
	}
	if cfg.MachineSecret != "env-secret" {
		t.Fatalf("MachineSecret = %q", cfg.MachineSecret)
	}
	if cfg.APIKey != "env-api-key" {
		t.Fatalf("APIKey = %q", cfg.APIKey)
	}
	if cfg.APISecret != "env-api-secret" {
		t.Fatalf("APISecret = %q", cfg.APISecret)
	}
	if cfg.ConnectionTimeoutSecs != 9 {
		t.Fatalf("ConnectionTimeoutSecs = %d", cfg.ConnectionTimeoutSecs)
	}
}

func TestBackendConfigApplyEnvPreservesTomlWhenEnvUnset(t *testing.T) {
	cfg := BackendConfig{
		BaseURL:               "https://toml.example",
		MachineKey:            "toml-key",
		MachineSecret:         "toml-secret",
		ConnectionTimeoutSecs: 3,
	}

	cfg.ApplyEnv()

	if cfg.BaseURL != "https://toml.example" {
		t.Fatalf("BaseURL = %q", cfg.BaseURL)
	}
	if cfg.MachineKey != "toml-key" {
		t.Fatalf("MachineKey = %q", cfg.MachineKey)
	}
	if cfg.MachineSecret != "toml-secret" {
		t.Fatalf("MachineSecret = %q", cfg.MachineSecret)
	}
	if cfg.ConnectionTimeoutSecs != 3 {
		t.Fatalf("ConnectionTimeoutSecs = %d", cfg.ConnectionTimeoutSecs)
	}
}
