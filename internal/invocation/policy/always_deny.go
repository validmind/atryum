package policy

import "context"

// AlwaysDenyProvider hard-denies every tool call. Useful as a safe default when
// no explicit policy has been configured, or to lock down an environment quickly.
type AlwaysDenyProvider struct{}

func (AlwaysDenyProvider) ID() string          { return "always_deny" }
func (AlwaysDenyProvider) DisplayName() string { return "Always Deny" }
func (AlwaysDenyProvider) Evaluate(_ context.Context, _ CallContext) (Decision, error) {
	return Decision{Disposition: DispositionNever, Reason: "always_deny policy"}, nil
}
