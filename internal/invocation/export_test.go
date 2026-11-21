package invocation

import (
	"context"
	"time"
)

// RecordVMJudgeGeneration exposes the unexported VM-path generation-span emitter
// to the external test package.
var RecordVMJudgeGeneration = recordVMJudgeGeneration

// SetApprovalPoll sets how often a held call re-reads its row.
func SetApprovalPoll(s *Service, d time.Duration) { s.approvalPoll, s.wakePoll = d, d }

// SetPolls sets the poll interval without and with a connected DecisionWaker.
func SetPolls(s *Service, approval, wake time.Duration) { s.approvalPoll, s.wakePoll = approval, wake }

// SetRedecideAfter sets how long a submitted call stays received before a
// retry with its idempotency key decides it again.
func SetRedecideAfter(s *Service, d time.Duration) { s.redecideAfter = d }

// BuildSessionContext exposes the judge's session history for sessionID,
// without the call exclude.
func BuildSessionContext(s *Service, sessionID, exclude string) (string, int) {
	return s.buildSessionContext(context.Background(), sessionID, exclude)
}
