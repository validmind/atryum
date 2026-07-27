package atryum

import (
	"context"

	"github.com/validmind/atryum/internal/access"
)

// Capability is an application-neutral permission understood by Atryum's
// access layer. Embedders map their own roles or policy model to these values.
type Capability = access.Capability

const (
	CapabilityReadResources = access.CapabilityReadResources
	CapabilityUpdateAgents  = access.CapabilityUpdateAgents
	CapabilityDecidePlans   = access.CapabilityDecidePlans
	CapabilityAdmin         = access.CapabilityAdmin
)

// Principal is the access decision returned for one verified token email.
// ActorID is opaque to Atryum and is used only when recording review actors.
type Principal = access.Principal

// AccessResolver maps a verified email to current access. It is called on
// every request so assignment changes take effect without restarting Atryum.
type AccessResolver interface {
	ResolveAccess(ctx context.Context, email string) (Principal, error)
}

// ErrUnknownIdentity distinguishes a valid token whose owner has not been
// provisioned. Atryum maps it to HTTP 403.
var ErrUnknownIdentity = access.ErrUnknownIdentity
