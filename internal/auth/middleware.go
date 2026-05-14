package auth

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// Middleware returns an http.Handler that requires a valid OAuth bearer token
// before invoking next. resourceMetadataPath is the absolute path of the
// protected-resource-metadata endpoint and is advertised in the
// WWW-Authenticate challenge per RFC 9728 so MCP clients can discover the
// authorization server. When v is nil, the middleware is a no-op (auth
// disabled).
func Middleware(v *Validator, resourceMetadataPath string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if v == nil {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			metadataURL := absoluteURL(r, resourceMetadataPath)
			header := strings.TrimSpace(r.Header.Get("Authorization"))
			if header == "" {
				writeChallenge(w, http.StatusUnauthorized, "missing bearer token", "", metadataURL, requiredScope(v))
				return
			}
			if !strings.HasPrefix(strings.ToLower(header), "bearer ") {
				writeChallenge(w, http.StatusUnauthorized, "invalid Authorization scheme", "invalid_request", metadataURL, requiredScope(v))
				return
			}
			token := strings.TrimSpace(header[len("Bearer "):])
			identity, err := v.Validate(r.Context(), token)
			if err != nil {
				ve, _ := err.(*ValidationError)
				switch {
				case ve != nil && ve.Result == ResultMissingScope:
					writeChallenge(w, http.StatusForbidden, ve.Description, "insufficient_scope", metadataURL, requiredScope(v))
				case ve != nil:
					writeChallenge(w, http.StatusUnauthorized, ve.Description, "invalid_token", metadataURL, requiredScope(v))
				default:
					writeChallenge(w, http.StatusUnauthorized, "invalid token", "invalid_token", metadataURL, requiredScope(v))
				}
				return
			}
			next.ServeHTTP(w, r.WithContext(WithIdentity(r.Context(), identity)))
		})
	}
}

func absoluteURL(r *http.Request, path string) string {
	if path == "" {
		return ""
	}
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if proto := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); proto != "" {
		scheme = proto
	}
	host := r.Host
	if forwardedHost := strings.TrimSpace(r.Header.Get("X-Forwarded-Host")); forwardedHost != "" {
		host = forwardedHost
	}
	if host == "" {
		return path
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return scheme + "://" + host + path
}

func requiredScope(v *Validator) string {
	for _, c := range v.Configs() {
		if c.RequiredScope != "" {
			return c.RequiredScope
		}
	}
	return ""
}

func writeChallenge(w http.ResponseWriter, status int, description string, errorCode string, resourceMetadataURL string, scope string) {
	parts := []string{`Bearer realm="atryum"`}
	if errorCode != "" {
		parts = append(parts, fmt.Sprintf(`error=%q`, errorCode))
	}
	if description != "" {
		parts = append(parts, fmt.Sprintf(`error_description=%q`, description))
	}
	if scope != "" {
		parts = append(parts, fmt.Sprintf(`scope=%q`, scope))
	}
	if resourceMetadataURL != "" {
		parts = append(parts, fmt.Sprintf(`resource_metadata=%q`, resourceMetadataURL))
	}
	w.Header().Set("WWW-Authenticate", strings.Join(parts, ", "))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error":             errorCodeOrDefault(errorCode, status),
		"error_description": description,
	})
}

func errorCodeOrDefault(code string, status int) string {
	if code != "" {
		return code
	}
	if status == http.StatusForbidden {
		return "insufficient_scope"
	}
	return "invalid_token"
}
