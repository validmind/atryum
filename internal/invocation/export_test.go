package invocation

import "time"

// RecordVMJudgeGeneration exposes the unexported VM-path generation-span emitter
// to the external test package.
var RecordVMJudgeGeneration = recordVMJudgeGeneration

// SetApprovalPoll sets how often a held call re-reads its row.
func SetApprovalPoll(s *Service, d time.Duration) { s.approvalPoll = d }
