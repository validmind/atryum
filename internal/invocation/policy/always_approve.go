package policy

import "context"

// AlwaysApproveProvider auto-approves every tool call. Useful for development
// and testing where human oversight friction is not desired.
type AlwaysApproveProvider struct{}

func (AlwaysApproveProvider) ID() string          { return "always_approve" }
func (AlwaysApproveProvider) DisplayName() string { return "Always Approve" }
func (AlwaysApproveProvider) Evaluate(_ context.Context, _ CallContext) (Decision, error) {
	return Decision{Disposition: DispositionAuto, Reason: "always_approve policy"}, nil
}
