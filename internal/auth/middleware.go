package auth

import (
	"crypto/subtle"
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
	// SkipVerify is a local-only debug mode. When true, the middleware is a
	// complete pass-through: the Authorization header is ignored, no token is
	// required, and no identity is set on the request context.
	SkipVerify bool
	// DebugLogIdentity logs the extracted identity after successful validation.
	DebugLogIdentity bool
}

func MiddlewareWithOptions(v *Validator, resourceMetadataPath string, opts MiddlewareOptions) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if v == nil || opts.SkipVerify {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			metadataURL := absoluteURL(r, resourceMetadataPath)
			header := strings.TrimSpace(r.Header.Get("Authorization"))
			if header == "" {
				scope := challengeScope(v, nil)
				if opts.DebugLogIdentity {
					logAuthFailure(r, "invalid_token", "missing bearer token", scope)
				}
				writeChallenge(w, http.StatusUnauthorized, "missing bearer token", "", metadataURL, scope)
				return
			}
			if !strings.HasPrefix(strings.ToLower(header), "bearer ") {
				writeChallenge(w, http.StatusUnauthorized, "invalid Authorization scheme", "invalid_request", metadataURL, challengeScope(v, nil))
				return
			}
			token := strings.TrimSpace(header[len("Bearer "):])
			identity, err := v.Validate(r.Context(), token)
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

// AdminMiddleware returns an http middleware that authenticates admin API
// requests. It accepts two credential paths:
//
//  1. Machine-key path: when the request carries matching X-API-Key and
//     X-API-Secret headers (checked against apiKeyCfg), the request is
//     admitted immediately as a trusted machine caller without requiring a
//     JWT. This allows server-to-server callers (e.g. the ValidMind backend
//     proxy) to use the same static credentials they already have rather than
//     obtaining a Keycloak token.
//
//  2. Bearer-token path: when no API-key headers are present the middleware
//     falls through to the existing OAuth JWT validation via ValidateAdmin.
//
// When v is nil, AdminEnabled() is false, or SkipVerify is set, the
// middleware is a no-op (auth disabled).
func AdminMiddleware(v *Validator, apiKeyCfg APIKeyConfig, opts MiddlewareOptions) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if v == nil || !v.AdminEnabled() || opts.SkipVerify {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Machine-key fast path: if the caller sends matching API key/secret
			// headers, admit as a trusted admin without requiring a JWT.
			if apiKeyCfg.Enabled() {
				gotKey := strings.TrimSpace(r.Header.Get(apiKeyHeader))
				gotSecret := strings.TrimSpace(r.Header.Get(apiSecretHeader))
				if gotKey != "" || gotSecret != "" {
					keyOK := subtle.ConstantTimeCompare([]byte(gotKey), []byte(apiKeyCfg.Key)) == 1
					secretOK := subtle.ConstantTimeCompare([]byte(gotSecret), []byte(apiKeyCfg.Secret)) == 1
					if keyOK && secretOK {
						if opts.DebugLogIdentity {
							log.Printf("[auth] valid_admin_machine_key method=%s path=%s remote=%s", r.Method, r.URL.Path, r.RemoteAddr)
						}
						next.ServeHTTP(w, r)
						return
					}
					// Keys were present but wrong — reject immediately; don't
					// fall through to Bearer so we don't leak which path failed.
					logAuthFailure(r, "invalid_token", "invalid api key credentials", "")
					writeAPIKeyError(w, http.StatusUnauthorized, "invalid api key credentials")
					return
				}
			}

			// Bearer-token path for human admin UI logins.
			header := strings.TrimSpace(r.Header.Get("Authorization"))
			if header == "" {
				logAuthFailure(r, "invalid_token", "missing bearer token", "")
				writeChallenge(w, http.StatusUnauthorized, "missing bearer token", "", "", "")
				return
			}
			if !strings.HasPrefix(strings.ToLower(header), "bearer ") {
				logAuthFailure(r, "invalid_request", "invalid Authorization scheme", "")
				writeChallenge(w, http.StatusUnauthorized, "invalid Authorization scheme", "invalid_request", "", "")
				return
			}
			token := strings.TrimSpace(header[len("Bearer "):])
			identity, err := v.ValidateAdmin(r.Context(), token)
			if err != nil {
				ve, _ := err.(*ValidationError)
				switch {
				case ve != nil && ve.Result == ResultMissingAdminClaim:
					logAuthFailure(r, "insufficient_scope", ve.Description, "")
					writeChallenge(w, http.StatusForbidden, ve.Description, "insufficient_scope", "", "")
				case ve != nil:
					logAuthFailure(r, "invalid_token", ve.Description, "")
					writeChallenge(w, http.StatusUnauthorized, ve.Description, "invalid_token", "", "")
				default:
					logAuthFailure(r, "invalid_token", "invalid token", "")
					writeChallenge(w, http.StatusUnauthorized, "invalid token", "invalid_token", "", "")
				}
				return
			}
			if opts.DebugLogIdentity {
				log.Printf("[auth] valid_admin_token method=%s path=%s remote=%s issuer=%q subject=%q email=%q", r.Method, r.URL.Path, r.RemoteAddr, identity.Issuer, identity.Subject, identity.Email)
			}
			next.ServeHTTP(w, r)
		})
	}
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
