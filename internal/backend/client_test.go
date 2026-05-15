package backend

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"atryum/internal/config"
)

func TestNewClientReturnsNilWhenBackendNotConfigured(t *testing.T) {
	client, err := NewClient(config.BackendConfig{})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if client != nil {
		t.Fatalf("expected nil client when backend is not configured")
	}
}

func TestNewClientRequiresCredentialsWhenBackendConfigured(t *testing.T) {
	_, err := NewClient(config.BackendConfig{BaseURL: "http://backend.test"})
	if err == nil {
		t.Fatalf("expected missing credentials error")
	}
}

func TestCheckConnectionSendsMachineCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != connectionPath {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("X-MACHINE-KEY"); got != "machine-key" {
			t.Errorf("X-MACHINE-KEY = %q", got)
		}
		if got := r.Header.Get("X-MACHINE-SECRET"); got != "machine-secret" {
			t.Errorf("X-MACHINE-SECRET = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"machine_user_cuid":"cmu123","service_name":"atryum"}`))
	}))
	defer server.Close()

	client, err := NewClient(config.BackendConfig{
		BaseURL:       server.URL,
		MachineKey:    "machine-key",
		MachineSecret: "machine-secret",
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	resp, err := client.CheckConnection(context.Background())
	if err != nil {
		t.Fatalf("CheckConnection: %v", err)
	}
	if !resp.OK || resp.MachineUserCUID != "cmu123" || resp.ServiceName != "atryum" {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestCheckConnectionReturnsErrorOnNonSuccessStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusUnauthorized)
	}))
	defer server.Close()

	client, err := NewClient(config.BackendConfig{
		BaseURL:       server.URL,
		MachineKey:    "machine-key",
		MachineSecret: "machine-secret",
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	if _, err := client.CheckConnection(context.Background()); err == nil {
		t.Fatalf("expected status error")
	}
}
