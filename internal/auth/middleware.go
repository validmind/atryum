package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
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
	return MiddlewareWithOptions(v, resourceMetadataPath, MiddlewareOptions{})
}

type MiddlewareOptions struct {
	// SkipVerify is a local-only debug mode. It parses bearer JWT claims without
	// verifying signature, issuer, audience, expiry, or required scope.
	SkipVerify bool
	// DebugLogIdentity logs the extracted identity after successful validation.
	DebugLogIdentity bool
}

func MiddlewareWithOptions(v *Validator, resourceMetadataPath string, opts MiddlewareOptions) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if v == nil {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			metadataURL := absoluteURL(r, resourceMetadataPath)
			header := strings.TrimSpace(r.Header.Get("Authorization"))
			if header == "" {
				writeChallenge(w, http.StatusUnauthorized, "missing bearer token", "", metadataURL, challengeScope(v, nil))
				return
			}
			if !strings.HasPrefix(strings.ToLower(header), "bearer ") {
				writeChallenge(w, http.StatusUnauthorized, "invalid Authorization scheme", "invalid_request", metadataURL, challengeScope(v, nil))
				return
			}
			token := strings.TrimSpace(header[len("Bearer "):])
			identity, err := validateBearer(r.Context(), v, token, opts)
			if err != nil {
				ve, _ := err.(*ValidationError)
				scope := challengeScope(v, ve)
				switch {
				case ve != nil && ve.Result == ResultMissingScope:
					logAuthFailure(r, "insufficient_scope", ve.Description, scope)
					writeChallenge(w, http.StatusForbidden, ve.Description, "insufficient_scope", metadataURL, scope)
				case ve != nil:
					logAuthFailure(r, "invalid_token", ve.Description, scope)
					writeChallenge(w, http.StatusUnauthorized, ve.Description, "invalid_token", metadataURL, scope)
				default:
					logAuthFailure(r, "invalid_token", "invalid token", scope)
					writeChallenge(w, http.StatusUnauthorized, "invalid token", "invalid_token", metadataURL, scope)
				}
				return
			}
			if opts.DebugLogIdentity {
				logAuthSuccess(r, identity)
			}
			next.ServeHTTP(w, r.WithContext(WithIdentity(r.Context(), identity)))
		})
	}
}

func validateBearer(ctx context.Context, v *Validator, token string, opts MiddlewareOptions) (Identity, error) {
	if opts.SkipVerify {
		return v.DebugIdentity(token)
	}
	return v.Validate(ctx, token)
}

func logAuthFailure(r *http.Request, code string, description string, scope string) {
	if scope != "" {
		log.Printf("[auth] %s method=%s path=%s remote=%s scope=%q description=%q", code, r.Method, r.URL.Path, r.RemoteAddr, scope, description)
		return
	}
	log.Printf("[auth] %s method=%s path=%s remote=%s description=%q", code, r.Method, r.URL.Path, r.RemoteAddr, description)
}

func logAuthSuccess(r *http.Request, identity Identity) {
	log.Printf("[auth] valid_token method=%s path=%s remote=%s agent_id=%q issuer=%q subject=%q scope=%q", r.Method, r.URL.Path, r.RemoteAddr, identity.AgentID, identity.Issuer, identity.Subject, identity.Scope)
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

// challengeScope returns the scope to advertise in the WWW-Authenticate
// `scope=` parameter. When the validator already matched a specific config
// (i.e. the ValidationError carries a RequiredScope), that scope is used.
// Otherwise the function only returns a scope when every configured
// authorization server agrees on the same non-empty required_scope; if any
// configured issuer has no required scope (or scopes disagree), we omit
// `scope=` rather than mislead the client into requesting a scope that is
// not required for one of the configured issuers.
func challengeScope(v *Validator, ve *ValidationError) string {
	if ve != nil && ve.RequiredScope != "" {
		return ve.RequiredScope
	}
	if v == nil {
		return ""
	}
	configs := v.Configs()
	if len(configs) == 0 {
		return ""
	}
	common := configs[0].RequiredScope
	if common == "" {
		return ""
	}
	for _, c := range configs[1:] {
		if c.RequiredScope != common {
			return ""
		}
	}
	return common
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
