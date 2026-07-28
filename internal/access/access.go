package access

import (
	"context"
	"errors"
)

type contextKey int

const principalKey contextKey = iota

type Capability string

const (
	CapabilityReadResources     Capability = "read_resources"
	CapabilityUpdateAgents      Capability = "update_agents"
	CapabilityDecideInvocations Capability = "decide_invocations"
	CapabilityDecidePlans       Capability = "decide_plans"
	CapabilityAdmin             Capability = "administrative_operations"
)

type Principal struct {
	ActorID      string
	AgentCUIDs   []string
	Capabilities []Capability
	Unrestricted bool
}

func (p Principal) Has(capability Capability) bool {
	if p.Unrestricted {
		return true
	}
	for _, candidate := range p.Capabilities {
		if candidate == capability {
			return true
		}
	}
	return false
}

func (p Principal) Assigned(agentCUID string) bool {
	if p.Unrestricted {
		return true
	}
	for _, assigned := range p.AgentCUIDs {
		if assigned == agentCUID {
			return true
		}
	}
	return false
}

type Resolver interface {
	ResolveAccess(ctx context.Context, email string) (Principal, error)
}

var ErrUnknownIdentity = errors.New("unknown access identity")

func WithPrincipal(ctx context.Context, principal Principal) context.Context {
	return context.WithValue(ctx, principalKey, principal)
}

func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	principal, ok := ctx.Value(principalKey).(Principal)
	return principal, ok
}
