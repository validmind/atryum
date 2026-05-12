package policy

import "context"

// ManualApprovalProvider queues every tool call for a single human approver.
// This is the Yellow-channel baseline — use it when you want explicit oversight
// on every call without defining finer-grained charter rules.
type ManualApprovalProvider struct{}

func (ManualApprovalProvider) ID() string          { return "manual_approval" }
func (ManualApprovalProvider) DisplayName() string { return "Manual Approval" }
func (ManualApprovalProvider) Evaluate(_ context.Context, _ CallContext) (Decision, error) {
	return Decision{Disposition: DispositionHuman, Reason: "manual_approval policy"}, nil
}
