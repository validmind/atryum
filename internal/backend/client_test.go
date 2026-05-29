package backend

import (
	"context"
	"encoding/json"
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

func TestSummarizeInvocationSendsContractAndDecodesSummary(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q", r.Method)
		}
		if r.URL.Path != summarizeInvocationPath {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("X-MACHINE-KEY"); got != "machine-key" {
			t.Errorf("X-MACHINE-KEY = %q", got)
		}
		if got := r.Header.Get("X-MACHINE-SECRET"); got != "machine-secret" {
			t.Errorf("X-MACHINE-SECRET = %q", got)
		}
		if got := r.Header.Get("X-Org-CUID"); got != "org_123" {
			t.Errorf("X-Org-CUID = %q", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q", got)
		}
		if got := r.Header.Get("Accept"); got != "application/json" {
			t.Errorf("Accept = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode request body: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"summary":"Read /tmp/a and returned hello."}`))
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

	resp, err := client.SummarizeInvocation(context.Background(), SummarizeInvocationRequest{
		ModelConfigCUID: "model_abc",
		OrgCUID:         "org_123",
		Invocation: map[string]any{
			"invocation_id": "inv_123",
			"input":         map[string]any{"path": "/tmp/a"},
		},
	})
	if err != nil {
		t.Fatalf("SummarizeInvocation: %v", err)
	}
	if resp.Summary != "Read /tmp/a and returned hello." {
		t.Fatalf("summary = %q", resp.Summary)
	}
	if gotBody["model_config_cuid"] != "model_abc" {
		t.Fatalf("model_config_cuid = %#v", gotBody["model_config_cuid"])
	}
	if gotBody["org_cuid"] != "org_123" {
		t.Fatalf("org_cuid = %#v", gotBody["org_cuid"])
	}
	invocation, ok := gotBody["invocation"].(map[string]any)
	if !ok {
		t.Fatalf("invocation = %#v", gotBody["invocation"])
	}
	if invocation["invocation_id"] != "inv_123" {
		t.Fatalf("invocation_id = %#v", invocation["invocation_id"])
	}
	input, ok := invocation["input"].(map[string]any)
	if !ok || input["path"] != "/tmp/a" {
		t.Fatalf("input = %#v", invocation["input"])
	}
}
