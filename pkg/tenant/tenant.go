// Package tenant carries the tenant a request acts for. Atryum's Postgres
// connections set app.tenant_id from it before each statement, and row-level
// security keeps every query to that tenant's rows (see internal/store).
// Embedding programs put the tenant on the request's context with With.
package tenant

import "context"

// Default is the tenant of a single-tenant Atryum, and of a context that
// names none.
const Default = "default"

type key struct{}

// With returns ctx acting for tenant id.
func With(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, key{}, id)
}

// From returns the tenant ctx acts for, or Default.
// ponytail: falls back to Default so single-tenant callers need no change;
// make a missing tenant an error once an embedder runs more than one.
func From(ctx context.Context) string {
	if id, _ := ctx.Value(key{}).(string); id != "" {
		return id
	}
	return Default
}
