package auth

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"
)

// APIKeyConfig holds the static key/secret pair used to authenticate
// machine-to-machine callers of the read-only invocation reporting endpoints.
// Both fields must be non-empty for the middleware to enforce authentication;
// if either is empty the middleware is disabled (no-op).
type APIKeyConfig struct {
	Key    string `toml:"key"`
	Secret string `toml:"secret"`
}

// Enabled reports whether the configured key/secret are usable.
func (c APIKeyConfig) Enabled() bool {
	return strings.TrimSpace(c.Key) != "" && strings.TrimSpace(c.Secret) != ""
}

const (
	apiKeyHeader    = "X-API-Key"
	apiSecretHeader = "X-API-Secret"
)

// APIKeyMiddleware returns an http middleware that requires callers to send
// matching X-API-Key and X-API-Secret headers. When the config is not
// enabled (missing key or secret), the middleware refuses every request with
// 503 so we never accidentally expose unauthenticated read endpoints.
func APIKeyMiddleware(cfg APIKeyConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !cfg.Enabled() {
				writeAPIKeyError(w, http.StatusServiceUnavailable, "api key auth is not configured")
				return
			}
			gotKey := strings.TrimSpace(r.Header.Get(apiKeyHeader))
			gotSecret := strings.TrimSpace(r.Header.Get(apiSecretHeader))
			if gotKey == "" || gotSecret == "" {
				writeAPIKeyError(w, http.StatusUnauthorized, "missing api key credentials")
				return
			}
			keyOK := subtle.ConstantTimeCompare([]byte(gotKey), []byte(cfg.Key)) == 1
			secretOK := subtle.ConstantTimeCompare([]byte(gotSecret), []byte(cfg.Secret)) == 1
			if !keyOK || !secretOK {
				writeAPIKeyError(w, http.StatusUnauthorized, "invalid api key credentials")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func writeAPIKeyError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{"message": message},
	})
}
