package auth

import (
	"encoding/json"
	"net/http"
	"strings"
)

// ProtectedResourceMetadata is the OAuth 2.0 protected resource metadata
// document defined by RFC 9728. MCP clients use it to discover which
// authorization server to use.
type ProtectedResourceMetadata struct {
	Resource               string   `json:"resource"`
	AuthorizationServers   []string `json:"authorization_servers"`
	ScopesSupported        []string `json:"scopes_supported,omitempty"`
	BearerMethodsSupported []string `json:"bearer_methods_supported,omitempty"`
}

// ProtectedResourceHandler returns an http.Handler that serves the OAuth
// protected-resource metadata document for this resource server. resource is
// the canonical URL identifying the protected resource (typically the base
// URL of the MCP endpoint).
func ProtectedResourceHandler(v *Validator, resource string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		issuers := []string{}
		scopes := map[string]struct{}{}
		if v != nil {
			for _, c := range v.Configs() {
				if c.Issuer != "" {
					issuers = append(issuers, c.Issuer)
				}
				if c.RequiredScope != "" {
					scopes[c.RequiredScope] = struct{}{}
				}
			}
		}
		scopeList := make([]string, 0, len(scopes))
		for s := range scopes {
			scopeList = append(scopeList, s)
		}
		doc := ProtectedResourceMetadata{
			Resource:               strings.TrimRight(resource, "/"),
			AuthorizationServers:   issuers,
			ScopesSupported:        scopeList,
			BearerMethodsSupported: []string{"header"},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(doc)
	})
}
