package mcp

import (
	"context"
	"testing"

	"github.com/validmind/atryum/internal/config"
	"github.com/validmind/atryum/pkg/tenant"
)

// The config file's [[upstreams]] belong to the default tenant: another
// tenant with no server of that name finds none, and lists none.
func TestConfigUpstreamsAreTheDefaultTenants(t *testing.T) {
	cfg := config.Config{Upstreams: []config.UpstreamConfig{
		{Name: "hosted", Mode: "http", BaseURL: "http://127.0.0.1:1", AuthToken: "config-secret", Enabled: true},
	}}
	r := NewResolver(nil, cfg)
	if u, err := r.ResolveContext(context.Background(), "hosted"); err != nil || u.AuthToken != "config-secret" {
		t.Fatalf("default tenant: %+v, %v", u, err)
	}
	other := tenant.With(context.Background(), "larkspur")
	if u, err := r.ResolveContext(other, "hosted"); err == nil {
		t.Fatalf("another tenant resolved the config upstream: %+v", u)
	}
	if list, err := r.ListAll(other); err != nil || len(list) != 0 {
		t.Fatalf("another tenant listed %+v (%v), want none", list, err)
	}
}
