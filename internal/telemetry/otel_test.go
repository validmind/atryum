package telemetry

import (
	"context"
	"encoding/base64"
	"testing"

	"atryum/internal/config"
)

func TestExporterHeadersBuildsBasicAuthFromKeyPair(t *testing.T) {
	got := exporterHeaders(config.OTLPExporterConfig{
		Name:      "langfuse",
		PublicKey: "pk-lf-1",
		SecretKey: "sk-lf-2",
	})
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("pk-lf-1:sk-lf-2"))
	if got["Authorization"] != want {
		t.Errorf("Authorization = %q, want %q", got["Authorization"], want)
	}
}

func TestExporterHeadersExplicitAuthWins(t *testing.T) {
	got := exporterHeaders(config.OTLPExporterConfig{
		PublicKey: "pk",
		SecretKey: "sk",
		Headers:   map[string]string{"Authorization": "Basic explicit"},
	})
	if got["Authorization"] != "Basic explicit" {
		t.Errorf("explicit Authorization should win, got %q", got["Authorization"])
	}
}

func TestExporterHeadersNoKeyPairNoAuth(t *testing.T) {
	got := exporterHeaders(config.OTLPExporterConfig{
		Headers: map[string]string{"DD-API-KEY": "x"},
	})
	if _, ok := got["Authorization"]; ok {
		t.Error("did not expect an Authorization header without a key pair")
	}
	if got["DD-API-KEY"] != "x" {
		t.Errorf("DD-API-KEY passthrough failed: %q", got["DD-API-KEY"])
	}
}

func TestSetupDisabledIsNoop(t *testing.T) {
	shutdown, err := Setup(context.Background(), config.OTELConfig{Enabled: false}, "prod-org")
	if err != nil {
		t.Fatalf("disabled Setup returned error: %v", err)
	}
	if shutdown == nil {
		t.Fatal("expected non-nil shutdown")
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("noop shutdown returned error: %v", err)
	}
}

func TestSetupEnabledRequiresExporters(t *testing.T) {
	_, err := Setup(context.Background(), config.OTELConfig{Enabled: true}, "prod-org")
	if err == nil {
		t.Fatal("expected error when enabled with no exporters")
	}
}

func TestSetupEnabledBuildsProvider(t *testing.T) {
	cfg := config.OTELConfig{
		Enabled: true,
		Exporters: []config.OTLPExporterConfig{
			{Name: "langfuse", Endpoint: "http://127.0.0.1:1/api/public/otel/v1/traces"},
			{Name: "datadog", Endpoint: "http://127.0.0.1:1/v1/traces", Headers: map[string]string{"DD-API-KEY": "x"}},
		},
	}
	// Exporter setup is lazy (no connection until export), so two bogus endpoints
	// still produce a working provider + shutdown.
	shutdown, err := Setup(context.Background(), cfg, "prod-org")
	if err != nil {
		t.Fatalf("enabled Setup returned error: %v", err)
	}
	defer func() { _ = shutdown(context.Background()) }()
	if shutdown == nil {
		t.Fatal("expected non-nil shutdown")
	}
}

func TestBuildResourceHasEnvironment(t *testing.T) {
	res := buildResource("prod-org")
	var gotEnv, gotSvc string
	for _, kv := range res.Attributes() {
		switch string(kv.Key) {
		case "deployment.environment":
			gotEnv = kv.Value.AsString()
		case "service.name":
			gotSvc = kv.Value.AsString()
		}
	}
	if gotEnv != "prod-org" {
		t.Errorf("deployment.environment = %q, want %q", gotEnv, "prod-org")
	}
	if gotSvc != "atryum" {
		t.Errorf("service.name = %q, want %q", gotSvc, "atryum")
	}
}
