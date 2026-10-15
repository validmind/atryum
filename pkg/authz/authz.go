// Package authz is the open-core authorization seam. It defines the human
// Principal attached to operator-API requests, the Actions the built-in
// handlers ask about, and the Authorizer interface that answers them.
//
// The stock atryum binary uses Default, a flat model: admins may do anything,
// members may read and issue keys for agents they belong to. Embedding
// programs (see pkg/atryum.WithAuthorizer) can replace it with richer
// roles, groups or external policy without forking the handlers.
package authz

import "context"

// Role is the coarse user role stored on the users table.
type Role string

const (
	RoleAdmin  Role = "admin"
	RoleMember Role = "member"
)

// Method records how a principal authenticated.
type Method string

const (
	// MethodJWT is an IdP-issued bearer token validated against [[auth]].
	MethodJWT Method = "jwt"
	// MethodMachineKey is the static [api_key] key/secret pair used by
	// server-to-server callers (e.g. the ValidMind backend proxy).
	MethodMachineKey Method = "machine_key"
	// MethodNone means inbound auth is disabled for this deployment; the
	// request carries a synthetic admin principal.
	MethodNone Method = "none"
)

// Principal is the authenticated human (or trusted machine) behind an
// operator-API request. UserID is empty for machine and no-auth principals.
type Principal struct {
	UserID  string
	Issuer  string
	Subject string
	Email   string
	Name    string
	Role    Role
	Method  Method
}

// IsAdmin reports whether the principal holds the admin role.
func (p Principal) IsAdmin() bool { return p.Role == RoleAdmin }

// Action names something a handler wants to do on behalf of a principal.
type Action string

const (
	// ActionAdmin gates every operator endpoint that has no finer-grained
	// action yet (servers, rules, settings, users, ...).
	ActionAdmin Action = "admin"
	// ActionAgentRead is viewing one agent and its members/keys metadata.
	ActionAgentRead Action = "agent:read"
	// ActionAgentKeysManage is issuing and revoking API keys for an agent.
	ActionAgentKeysManage Action = "agent:keys:manage"
	// ActionAgentMembersManage is adding/removing users on an agent.
	ActionAgentMembersManage Action = "agent:members:manage"
	// ActionAgentWrite is editing or deleting an agent.
	ActionAgentWrite Action = "agent:write"
)

// Resource identifies what an Action targets. Fields are optional; an Action
// that is not agent-scoped leaves AgentID empty.
type Resource struct {
	AgentID string
}

// Authorizer decides whether a principal may perform an action on a resource.
// Implementations must be safe for concurrent use. Returning (false, nil)
// yields 403; a non-nil error yields 500.
type Authorizer interface {
	Can(ctx context.Context, p Principal, action Action, res Resource) (bool, error)
}

// MembershipChecker is the slice of the agent_members store Default needs.
type MembershipChecker interface {
	IsMember(ctx context.Context, agentID, userID string) (bool, error)
}

// Default is the open-core policy: admins may do anything; members may read
// agents they belong to and manage keys on them. Everything else is denied.
type Default struct {
	Members MembershipChecker
}

// Can implements Authorizer.
func (d Default) Can(ctx context.Context, p Principal, action Action, res Resource) (bool, error) {
	if p.IsAdmin() {
		return true, nil
	}
	switch action {
	case ActionAgentRead, ActionAgentKeysManage:
		if res.AgentID == "" || p.UserID == "" || d.Members == nil {
			return false, nil
		}
		return d.Members.IsMember(ctx, res.AgentID, p.UserID)
	default:
		return false, nil
	}
}

type ctxKey int

const principalKey ctxKey = iota

// WithPrincipal returns a child context carrying the principal.
func WithPrincipal(parent context.Context, p Principal) context.Context {
	return context.WithValue(parent, principalKey, p)
}

// PrincipalFromContext returns the request principal, if the operator auth
// middleware attached one.
func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey).(Principal)
	return p, ok
}
