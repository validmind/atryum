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

// From returns the tenant ctx acts for, or Default. A program that runs
// more than one tenant sets WithRequestTenant, and Atryum's connections then
// refuse a context that names none instead of acting for Default.
func From(ctx context.Context) string {
	if id, ok := Lookup(ctx); ok {
		return id
	}
	return Default
}

// Lookup returns the tenant ctx names, and whether it names one.
func Lookup(ctx context.Context) (string, bool) {
	id, _ := ctx.Value(key{}).(string)
	return id, id != ""
}
