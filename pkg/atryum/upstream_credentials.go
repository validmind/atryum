package atryum

import (
	"context"
	"errors"

	"github.com/validmind/atryum/internal/auth"
	"github.com/validmind/atryum/internal/mcp"
)

// UpstreamPurpose says why Atryum is about to contact an upstream.
type UpstreamPurpose = mcp.Purpose

const (
	// UpstreamList is tools/list and similar.
	UpstreamList = mcp.PurposeList
	// UpstreamCall is a tools/call, resolved before the decider runs.
	UpstreamCall = mcp.PurposeCall
	// UpstreamExecute is just before a call runs, including a held call
	// being released.
	UpstreamExecute = mcp.PurposeExecute
	// UpstreamForward is any other forwarded method.
	UpstreamForward = mcp.PurposeForward
	// UpstreamInitialize is a client starting a session. Nothing goes to the
	// upstream: the function may only return an UpstreamUnavailable to give
	// the client a notice, and must do no more than look the caller up.
	UpstreamInitialize = mcp.PurposeInitialize
)

// Tool is a tool as an MCP server lists it.
type Tool = mcp.Tool

// UpstreamUnavailable is returned (as the error) by the credential function
// when the caller can't use the server yet, such as a person who hasn't
// connected their account. Atryum then doesn't contact the upstream: a
// tools/list is answered with Tools, so the client sees something to read
// instead of an error it would swallow, an initialize adds Instructions for
// the client's model, and any other contact fails with Message.
type UpstreamUnavailable struct {
	Message      string
	Tools        []Tool
	Instructions string
}

func (u *UpstreamUnavailable) Error() string { return u.Message }

// UpstreamContact describes one upstream contact, for WithUpstreamCredentials.
type UpstreamContact struct {
	Server       string // the server's name (mcp_servers.name)
	Purpose      UpstreamPurpose
	AgentID      string // the verified caller; empty only without agent auth
	Issuer       string
	Method       string // jwt, api_key, managed_agent or no_auth
	KeyID        string
	InvocationID string // set for UpstreamExecute
}

// UpstreamCredential is what to send to the upstream for one contact.
type UpstreamCredential struct {
	// BearerToken replaces the server's own token when non-empty.
	BearerToken string
	// Headers are merged over the server's own headers.
	Headers map[string]string
	// SessionKey keys the cached upstream MCP session; "" means the server
	// name. Callers with different credentials must use different keys.
	SessionKey string
	// OnUnauthorized, if set, is called once, synchronously, before a 401
	// from the upstream reaches the caller. It must return quickly and make
	// no network call.
	OnUnauthorized func(ctx context.Context)
}

// UpstreamCredentials decides the credential for each upstream contact.
type UpstreamCredentials func(ctx context.Context, c UpstreamContact) (UpstreamCredential, error)

// WithUpstreamCredentials makes Atryum ask fn for the credential to use on
// every upstream contact: tools/list, tools/call (before the decider), other
// forwarded methods, and again just before a call executes, so a held call
// released later uses the credential as it stands then.
//
// A non-nil error aborts that contact, and its message reaches the caller
// (tools/list: JSON-RPC -32000; a call: invocation.failed). A zero
// UpstreamCredential uses only the server's own token and headers. With this
// option set, Atryum's stored OAuth credentials (oauth_credentials) are never
// applied, on any path. Atryum never logs the token or header values.
func WithUpstreamCredentials(fn UpstreamCredentials) Option {
	return func(o *options) {
		o.upstreamCredentials = fn
	}
}

func upstreamCredentialProvider(fn UpstreamCredentials) mcp.CredentialProvider {
	return func(ctx context.Context, upstream mcp.Upstream) (mcp.Upstream, error) {
		contact := UpstreamContact{
			Server:       upstream.Name,
			Purpose:      mcp.PurposeFromContext(ctx),
			InvocationID: mcp.InvocationIDFromContext(ctx),
		}
		if id, ok := auth.IdentityFromContext(ctx); ok {
			contact.AgentID = id.AgentID
			contact.Issuer = id.Issuer
			contact.Method = id.Method
			contact.KeyID = id.KeyID
		}
		cred, err := fn(ctx, contact)
		if err != nil {
			var u *UpstreamUnavailable
			if errors.As(err, &u) {
				return mcp.Upstream{}, &mcp.Unavailable{Message: u.Message, Tools: u.Tools, Instructions: u.Instructions}
			}
			return mcp.Upstream{}, err
		}
		if cred.BearerToken != "" {
			upstream.AuthToken = cred.BearerToken
		}
		if len(cred.Headers) > 0 {
			merged := make([]mcp.AuthHeader, 0, len(upstream.AuthHeaders)+len(cred.Headers))
			for _, h := range upstream.AuthHeaders {
				if _, replaced := cred.Headers[h.Name]; !replaced {
					merged = append(merged, h)
				}
			}
			for name, value := range cred.Headers {
				merged = append(merged, mcp.AuthHeader{Name: name, Value: value})
			}
			upstream.AuthHeaders = merged
		}
		upstream.SessionKey = cred.SessionKey
		upstream.OnUnauthorized = cred.OnUnauthorized
		return upstream, nil
	}
}
