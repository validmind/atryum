package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/validmind/atryum/internal/auth"
	"github.com/validmind/atryum/internal/invocation"
)

// AssertionRejection is a call that claimed to act for an end user from a
// credential that may not (it lacks the "app" capability). No invocation is
// created for it; the embedder records it (AA: app.asserter_rejected).
type AssertionRejection struct {
	AgentID string
	Method  string
	Issuer  string
	KeyID   string
	// Path is the route ("/api/v1/external/invocations", "/mcp/{server}").
	Path string
	At   time.Time
}

// SetAssertionRejected installs the sink for AssertionRejections. It runs
// synchronously; its error is logged, never returned to the caller.
func (h *Handler) SetAssertionRejected(f func(ctx context.Context, r AssertionRejection) error) {
	h.assertionRejected = f
}

// assertOnBehalfOf turns an application's on_behalf_of into req.Asserted, or
// writes the refusal and returns false: 403 not_an_asserter when the
// credential lacks the "app" capability, 400 invalid_on_behalf_of when the
// block is malformed. A request without on_behalf_of passes untouched.
func (h *Handler) assertOnBehalfOf(w http.ResponseWriter, r *http.Request, req *invocation.ExternalSubmitRequest) bool {
	a, refusal := h.checkOnBehalfOf(r, req.OnBehalfOf)
	if refusal != nil {
		writeJSON(w, refusal.status, refusal.body())
		return false
	}
	req.Asserted = a
	return true
}

// onBehalfOfRefusal is why an end-user claim was refused.
type onBehalfOfRefusal struct {
	status  int
	code    string
	field   string
	message string
}

func (f *onBehalfOfRefusal) body() map[string]any {
	b := map[string]any{"code": f.code, "error": map[string]string{"message": f.message}}
	if f.field != "" {
		b["field"] = f.field
	}
	return b
}

// checkOnBehalfOf checks an end-user claim (from a body or MCP headers)
// against the caller's credential. nil o is no claim: (nil, nil).
func (h *Handler) checkOnBehalfOf(r *http.Request, o *invocation.OnBehalfOf) (*invocation.AssertedIdentity, *onBehalfOfRefusal) {
	if o == nil {
		return nil, nil
	}
	id, ok := auth.IdentityFromContext(r.Context())
	if !ok || !id.HasCapability(auth.CapabilityApp) {
		h.rejectAssertion(r, id)
		return nil, &onBehalfOfRefusal{status: http.StatusForbidden, code: "not_an_asserter", message: "this credential may not act for an end user"}
	}
	if err := o.Validate(); err != nil {
		f := &onBehalfOfRefusal{status: http.StatusBadRequest, code: "invalid_on_behalf_of", message: err.Error()}
		var fe *invocation.OnBehalfOfError
		if errors.As(err, &fe) {
			f.field = fe.Field
		}
		return nil, f
	}
	return o.Asserted(id.AgentID), nil
}

// MCP proxy headers an application sends to act for an end user.
const (
	headerAppAgent      = "X-Atryum-App-Agent"
	headerEndUser       = "X-Atryum-End-User"
	headerEndUserEmail  = "X-Atryum-End-User-Email"
	headerEndUserRoles  = "X-Atryum-End-User-Roles"
	headerEndUserTenant = "X-Atryum-End-User-Tenant"
	headerEndUserToken  = "X-Atryum-End-User-Token"
	headerWaitMS        = "X-Atryum-Wait-Ms"
)

// onBehalfOfFromHeaders reads an end-user claim from MCP proxy headers. It
// returns nil when none of them is set. Roles are comma-separated.
func onBehalfOfFromHeaders(hdr http.Header) (*invocation.OnBehalfOf, int) {
	present := false
	for _, k := range []string{headerAppAgent, headerEndUser, headerEndUserEmail, headerEndUserRoles, headerEndUserTenant, headerEndUserToken} {
		if _, ok := hdr[http.CanonicalHeaderKey(k)]; ok {
			present = true
		}
	}
	if !present {
		return nil, 0
	}
	o := &invocation.OnBehalfOf{AppAgent: strings.TrimSpace(hdr.Get(headerAppAgent)), Token: strings.TrimSpace(hdr.Get(headerEndUserToken))}
	if id := hdr.Get(headerEndUser); id != "" || hdr.Get(headerEndUserEmail) != "" || hdr.Get(headerEndUserRoles) != "" {
		u := &invocation.EndUser{ID: strings.TrimSpace(id), Email: strings.TrimSpace(hdr.Get(headerEndUserEmail))}
		for _, role := range strings.Split(hdr.Get(headerEndUserRoles), ",") {
			if role = strings.TrimSpace(role); role != "" {
				u.Roles = append(u.Roles, role)
			}
		}
		if _, ok := hdr[http.CanonicalHeaderKey(headerEndUserTenant)]; ok {
			t := strings.TrimSpace(hdr.Get(headerEndUserTenant))
			u.Tenant = &t
		}
		o.EndUser = u
	}
	wait, _ := strconv.Atoi(strings.TrimSpace(hdr.Get(headerWaitMS)))
	return o, wait
}

func (h *Handler) rejectAssertion(r *http.Request, id auth.Identity) {
	if h.assertionRejected == nil {
		return
	}
	rej := AssertionRejection{AgentID: id.AgentID, Method: id.Method, Issuer: id.Issuer, KeyID: id.KeyID, Path: r.URL.Path, At: time.Now().UTC()}
	if err := h.assertionRejected(r.Context(), rej); err != nil {
		h.debugf("record assertion rejection: %v", err)
	}
}
