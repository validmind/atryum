package auth

import "context"

type ctxKey int

const identityKey ctxKey = iota

// WithIdentity returns a child context carrying the given Identity.
func WithIdentity(parent context.Context, id Identity) context.Context {
	return context.WithValue(parent, identityKey, id)
}

// IdentityFromContext returns the authenticated Identity if one is present.
func IdentityFromContext(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(identityKey).(Identity)
	return id, ok
}

// AgentIDFromContext is a convenience returning just the agent id.
func AgentIDFromContext(ctx context.Context) string {
	if id, ok := IdentityFromContext(ctx); ok {
		return id.AgentID
	}
	return ""
}
