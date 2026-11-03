package mcp

import "context"

// Purpose says why Atryum is about to contact an upstream. A
// CredentialProvider reads it from the context.
type Purpose string

const (
	PurposeList    Purpose = "list"    // tools/list and similar
	PurposeCall    Purpose = "call"    // tools/call, resolved before the decider runs
	PurposeExecute Purpose = "execute" // just before a call runs, a released held call included
	PurposeForward Purpose = "forward" // other forwarded methods
)

type purposeKey struct{}
type invocationKey struct{}

// WithPurpose returns ctx carrying p.
func WithPurpose(ctx context.Context, p Purpose) context.Context {
	return context.WithValue(ctx, purposeKey{}, p)
}

// PurposeFromContext returns the purpose on ctx, or PurposeCall.
func PurposeFromContext(ctx context.Context) Purpose {
	if p, ok := ctx.Value(purposeKey{}).(Purpose); ok {
		return p
	}
	return PurposeCall
}

// WithInvocationID returns ctx carrying the invocation being executed.
func WithInvocationID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, invocationKey{}, id)
}

// InvocationIDFromContext returns the invocation on ctx, if any.
func InvocationIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(invocationKey{}).(string)
	return id
}
