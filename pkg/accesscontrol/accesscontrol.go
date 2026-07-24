// Package accesscontrol defines generic extension-facing authentication and
// authorization contracts. It deliberately has no domain-specific account
// model: embedding programs decide what a verified identity resolves to.
package accesscontrol

import (
	"context"
	"net/http"
)

const CapabilityPrivileged = "privileged"

// VerifiedIdentity contains claims from a cryptographically verified JWT.
type VerifiedIdentity struct {
	Issuer  string
	Subject string
	Name    string
	Email   string
}

// Subject is the opaque authorization result supplied by an embedding
// program. ScopeID partitions scoped records, ActorID labels audit decisions,
// and capabilities control privileged routes.
type Subject struct {
	ScopeID      int64           `json:"scope_id"`
	ActorID      string          `json:"actor_id"`
	Capabilities map[string]bool `json:"capabilities"`
	Machine      bool            `json:"-"`
}

func (s Subject) Has(capability string) bool {
	return s.Capabilities != nil && s.Capabilities[capability]
}

// Resolver maps a verified identity to an opaque access subject.
type Resolver func(context.Context, VerifiedIdentity) (subject Subject, found bool, err error)

type RouteRegistrar func(mux *http.ServeMux, protect func(http.Handler) http.Handler)

// Config supplies generic identity resolution and scoped invocation access to
// an embedded Atryum server.
type Config struct {
	Resolve                  Resolver
	ScopeColumn              string
	RegisterProtectedRoutes  RouteRegistrar
	RegisterPrivilegedRoutes RouteRegistrar
	ValidateStartup          func(ctx context.Context, machineCredentials bool) error
}

type contextKey struct{}

func WithSubject(ctx context.Context, subject Subject) context.Context {
	return context.WithValue(ctx, contextKey{}, subject)
}

func SubjectFromContext(ctx context.Context) (Subject, bool) {
	subject, ok := ctx.Value(contextKey{}).(Subject)
	return subject, ok
}
