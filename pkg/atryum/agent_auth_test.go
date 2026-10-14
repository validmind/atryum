package atryum

import (
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
	} {
		t.Setenv("ATRYUM_AUTH_DEBUG_SKIP_VERIFY", tc.env)
		if err := checkAgentAuth(tc.cfg); (err == nil) != tc.wantOK {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}
