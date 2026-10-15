package atryum

import (
	"context"
	"testing"

	"github.com/validmind/atryum/internal/auth"
	"github.com/validmind/atryum/internal/config"
)

func TestCheckAgentAuth(t *testing.T) {
	issuer := auth.Config{Enabled: true, Issuer: "https://idp", Audience: "aa"}
	for name, tc := range map[string]struct {
		cfg    config.Config
		env    string
		wantOK bool
	}{
		"no [[auth]]":            {cfg: config.Config{}},
		"disabled issuer":        {cfg: config.Config{Auth: []auth.Config{{Issuer: "https://idp", Audience: "aa"}}}},
		"enabled issuer":         {cfg: config.Config{Auth: []auth.Config{issuer}}, wantOK: true},
		"skip_verify in config":  {cfg: config.Config{Auth: []auth.Config{issuer}, AuthDebug: config.AuthDebugConfig{SkipVerify: true}}},
		"skip_verify in the env": {cfg: config.Config{Auth: []auth.Config{issuer}}, env: "1"},
		"plain http issuer":      {cfg: config.Config{Auth: []auth.Config{{Enabled: true, Issuer: "http://idp.bank", Audience: "aa"}}}},
		"plain http jwks":        {cfg: config.Config{Auth: []auth.Config{{Enabled: true, Issuer: "https://idp", Audience: "aa", JWKSURL: "http://idp.bank/jwks"}}}},
		"not a web url":          {cfg: config.Config{Auth: []auth.Config{{Enabled: true, Issuer: "ftp://idp", Audience: "aa"}}}},
		"http on loopback":       {cfg: config.Config{Auth: []auth.Config{{Enabled: true, Issuer: "http://127.0.0.1:9091", Audience: "aa", JWKSURL: "http://localhost:9091/jwks"}}}, wantOK: true},
		"http on ipv6 loopback":  {cfg: config.Config{Auth: []auth.Config{{Enabled: true, Issuer: "http://[::1]:9091", Audience: "aa"}}}, wantOK: true},
		"http on a lookalike":    {cfg: config.Config{Auth: []auth.Config{{Enabled: true, Issuer: "http://127.0.0.1.evil.example", Audience: "aa"}}}},
	} {
		t.Setenv("ATRYUM_AUTH_DEBUG_SKIP_VERIFY", tc.env)
		if err := checkAgentAuth(tc.cfg); (err == nil) != tc.wantOK {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

// Agent API keys default on for standalone Atryum and off under a decider;
// WithAgentAPIKeys overrides either way.
func TestAgentAPIKeysDefault(t *testing.T) {
	decider := WithDecider(func(context.Context, ToolCall) (Decision, error) { return Decision{}, nil })
	for name, tc := range map[string]struct {
		opts []Option
		want bool
	}{
		"standalone":             {nil, true},
		"standalone, off":        {[]Option{WithAgentAPIKeys(false)}, false},
		"decider":                {[]Option{decider}, false},
		"decider, opted in":      {[]Option{decider, WithAgentAPIKeys(true)}, true},
		"opted in, then decider": {[]Option{WithAgentAPIKeys(true), decider}, true},
	} {
		if got := buildOptions(tc.opts).agentAPIKeysEnabled(); got != tc.want {
			t.Errorf("%s: agentAPIKeysEnabled = %v, want %v", name, got, tc.want)
		}
	}
}
