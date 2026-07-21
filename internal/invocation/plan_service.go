package invocation

import (
	"context"
	"fmt"
	"log/slog"
	neturl "net/url"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"atryum/internal/auth"
)

const (
	planDefaultTTLSeconds = 3600
	planMaxTTLSeconds     = 86400
)

type planRepo interface {
	Create(ctx context.Context, p Plan) error
	Update(ctx context.Context, p Plan) error
	Get(ctx context.Context, id string) (Plan, error)
	List(ctx context.Context, filter PlanListFilter) ([]Plan, int, error)
	ListActiveByAgent(ctx context.Context, agentIDs []string) ([]Plan, error)
	ListRevisions(ctx context.Context, parentID string) ([]Plan, error)
}

type planEventRepo interface {
	Create(ctx context.Context, evt PlanEvent) error
	ListByPlan(ctx context.Context, planID string, filter EventListFilter) ([]PlanEvent, int, error)
}

// SetPlanStore installs the optional plan-submission feature. When it is not
// called the service behaves exactly as before: plan endpoints error and
// matchApprovedPlan never grants a pass. Every matching invocation is sent to
// the adherence judge before the plan pass is granted, except a provable
// read-only poll of the plan's own status (the fast pass). statusPollOrigins
// lists the hosts (host or host:port, or full URLs) this Atryum API is
// reachable on; the fast pass only trusts polls addressed to one of them.
func (s *Service) SetPlanStore(plans planRepo, events planEventRepo, judge PlanEvaluator, statusPollOrigins []string) {
	s.plans = plans
	s.planEvents = events
	s.planJudge = judge
	s.planPollOrigins = newPlanOriginSet(statusPollOrigins)
}

func (s *Service) plansEnabled() bool { return s.plans != nil }

// PlansEnabled reports whether the plan-submission feature is wired
// (SetPlanStore was called). Handlers use it to decide whether to advertise
// plan submission to agents; no rule configuration is required — a plan that
// matches no rule simply defaults to human review.
func (s *Service) PlansEnabled() bool { return s.plansEnabled() }

func (s *Service) recordPlanEvent(ctx context.Context, planID, eventType string, payload map[string]any) {
	if s.planEvents == nil {
		return
	}
	_ = s.planEvents.Create(ctx, PlanEvent{
		PlanID:    planID,
		EventType: eventType,
		Payload:   mustJSON(payload),
		CreatedAt: time.Now().UTC(),
	})
}

// SetPlanTTLBounds overrides the built-in plan TTL default and ceiling
// (usually from the `[plans]` TOML section). Non-positive values keep the
// built-in constants.
func (s *Service) SetPlanTTLBounds(defaultSeconds, maxSeconds int) {
	s.planDefaultTTL = defaultSeconds
	s.planMaxTTL = maxSeconds
}

func (s *Service) clampPlanTTL(seconds int) int {
	def, max := s.planDefaultTTL, s.planMaxTTL
	if def <= 0 {
		def = planDefaultTTLSeconds
	}
	if max <= 0 {
		max = planMaxTTLSeconds
	}
	if seconds <= 0 {
		return def
	}
	if seconds > max {
		return max
	}
	return seconds
}

// SubmitPlan records a proposed plan, evaluates each declared action against
// the ordinary invocation rules, then runs one mandatory whole-plan charter
// review. Callers poll GetPlan until the status leaves received/pending_approval.
func (s *Service) SubmitPlan(ctx context.Context, req PlanSubmitRequest) (Plan, error) {
	if !s.plansEnabled() {
		return Plan{}, fmt.Errorf("plan submission is not enabled")
	}
	// A verified OAuth identity always wins over the self-declared agent_id.
	// A plan pass is scoped to its agent, so an unidentifiable submitter
	// would grant a pass to every other anonymous caller — reject it.
	agentID := auth.AgentIDFromContext(ctx)
	if agentID == "" {
		agentID = strings.TrimSpace(req.AgentID)
	}
	if agentID == "" {
		return Plan{}, fmt.Errorf("agent identity is required to submit a plan (authenticate or set agent_id)")
	}
	if strings.TrimSpace(req.Goal) == "" {
		return Plan{}, fmt.Errorf("goal is required")
	}
	if len(req.Actions) == 0 {
		return Plan{}, fmt.Errorf("at least one action is required")
	}
	source := req.Source
	if source == "" {
		source = "external"
	}
	for i, a := range req.Actions {
		if strings.TrimSpace(a.Tool) == "" {
			return Plan{}, fmt.Errorf("actions[%d].tool is required", i)
		}
		if strings.TrimSpace(a.Server) == "" {
			req.Actions[i].Server = source
		}
	}
	var parent *Plan
	if req.RevisionOf != "" {
		p, err := s.plans.Get(ctx, req.RevisionOf)
		if err != nil {
			return Plan{}, fmt.Errorf("revision_of plan %s not found", req.RevisionOf)
		}
		if p.AgentID != agentID {
			return Plan{}, fmt.Errorf("revision_of plan %s belongs to a different agent", req.RevisionOf)
		}
		switch p.Status {
		case PlanStatusNeedsRevision, PlanStatusPendingApproval, PlanStatusApproved:
			// revisable
		default:
			return Plan{}, fmt.Errorf("plan %s cannot be revised from status %s", p.PlanID, p.Status)
		}
		parent = &p
	}

	now := time.Now().UTC()
	plan := Plan{
		PlanID:      "plan_" + uuid.NewString(),
		AgentID:     agentID,
		Source:      source,
		ThreadID:    req.ThreadID,
		Goal:        req.Goal,
		Rationale:   req.Rationale,
		Actions:     req.Actions,
		Status:      PlanStatusReceived,
		Revision:    1,
		TTLSeconds:  s.clampPlanTTL(req.TTLSeconds),
		SubmittedAt: now,
	}
	if parent != nil {
		id := parent.PlanID
		plan.ParentPlanID = &id
		plan.Revision = parent.Revision + 1
	}
	if req.ClientName != "" {
		v := req.ClientName
		plan.ClientName = &v
	}
	if req.ClientVersion != "" {
		v := req.ClientVersion
		plan.ClientVersion = &v
	}
	if err := s.plans.Create(ctx, plan); err != nil {
		return Plan{}, err
	}
	receivedPayload := map[string]any{
		"goal": plan.Goal, "source": source, "agent_id": agentID,
		"actions": plan.Actions, "revision": plan.Revision,
	}
	if plan.ParentPlanID != nil {
		receivedPayload["parent_plan_id"] = *plan.ParentPlanID
	}
	s.recordPlanEvent(ctx, plan.PlanID, "plan.received", receivedPayload)

	// Supersede the parent only after the revision exists: submitting a
	// revision of an approved plan revokes the old pass.
	if parent != nil {
		parent.Status = PlanStatusSuperseded
		if err := s.plans.Update(ctx, *parent); err != nil {
			slog.Warn("plan revision: could not supersede parent plan", "parent_plan_id", parent.PlanID, "error", err)
		} else {
			s.recordPlanEvent(ctx, parent.PlanID, "plan.superseded", map[string]any{"superseded_by": plan.PlanID})
		}
	}

	return s.evaluatePlanRules(ctx, plan, req.ChatContext)
}

// evaluatePlanRules applies the ordinary invocation rules to every declared
// action. Each action walks the single configured priority list from top to
// bottom and stops at its first decisive rule, exactly like an invocation.
// A local-LLM ai_evaluation rule may select the LLM configuration for the
// single mandatory whole-plan charter review. Every AI rule consumes that same
// verdict; no rule can cause an additional plan-judge call.
//
// Any deny/revise verdict rejects the whole plan. Human approval, AI
// escalation, a rule-load failure, or an action with no matching rule sends the
// whole plan to a human. The plan is auto-approved only when every action is
// covered by auto_approve or an approving whole-plan AI evaluation.
func (s *Service) evaluatePlanRules(ctx context.Context, plan Plan, chatContext string) (Plan, error) {
	agentRec := s.resolveAgentRecord(ctx, plan.AgentID)
	var ruleStatus PlanStatus
	var ruleApproval *Approval
	var ruleFeedback string
	var charterLLMConfigID string
	var charterResult *planAIEvaluationResult
	if s.rules == nil {
		ruleStatus = PlanStatusPendingApproval
		ruleApproval = newApproval("human_required", "plan requires human approval because no invocation rules are configured", nil)
		result := s.evaluateMandatoryPlanCharter(ctx, plan, agentRec, chatContext, charterLLMConfigID)
		return s.finalizePlanAfterCharterReview(ctx, plan, result, ruleStatus, ruleApproval, ruleFeedback)
	}

	rules, err := s.rules.ListApprovalRules(ctx)
	if err != nil {
		slog.Error("plan submission: failed to load invocation rules; falling back to human approval",
			"plan_id", plan.PlanID, "error", err)
		ruleStatus = PlanStatusPendingApproval
		ruleApproval = newApproval("human_required", "plan requires human approval because invocation rules could not be loaded", nil)
		result := s.evaluateMandatoryPlanCharter(ctx, plan, agentRec, chatContext, charterLLMConfigID)
		return s.finalizePlanAfterCharterReview(ctx, plan, result, ruleStatus, ruleApproval, ruleFeedback)
	}

	var firstApprovalRuleID string
	var aiApprovalRuleID string
	var pendingRuleID string
	var pendingApproval *Approval
	var deniedRuleID string
	var deniedResult *planAIEvaluationResult

	for _, action := range plan.Actions {
		server := action.Server
		if server == "" {
			server = plan.Source
		}
		matched := matchRules(rules, server, action.Tool, agentRec.ID)
		if len(matched) == 0 {
			if pendingApproval == nil {
				pendingApproval = newApproval("human_required", "plan includes an action with no matching invocation rule", nil)
			}
			continue
		}

		decided := false
		for _, rule := range matched {
			switch rule.Action {
			case RuleActionAutoApprove:
				if firstApprovalRuleID == "" {
					firstApprovalRuleID = rule.ID
				}
				decided = true
			case RuleActionAutoDeny:
				feedback := "A planned action matches invocation deny rule " + rule.ID + ". Remove or replace that action before submitting another plan."
				deniedRuleID = rule.ID
				result := planAIEvaluationResult{Status: PlanStatusDenied,
					Approval: newApproval("auto_denied", "plan rejected: matched invocation auto_deny rule", nil), Feedback: feedback}
				deniedResult = &result
				decided = true
			case RuleActionAIEvaluation:
				if charterLLMConfigID == "" && rule.AtryumLLMConfigID != "" {
					charterLLMConfigID = rule.AtryumLLMConfigID
				}
				if charterResult == nil {
					result := s.evaluateMandatoryPlanCharter(ctx, plan, agentRec, chatContext, charterLLMConfigID)
					charterResult = &result
				}
				if charterResult.Verdict == "next_rule" {
					continue // next_rule
				}
				if charterResult.Status == PlanStatusDenied || charterResult.Status == PlanStatusNeedsRevision {
					deniedRuleID = rule.ID
					resultCopy := *charterResult
					deniedResult = &resultCopy
				}
				if charterResult.Status == PlanStatusPendingApproval {
					if pendingApproval == nil {
						pendingRuleID = rule.ID
						pendingApproval = charterResult.Approval
					}
				} else if charterResult.Status == PlanStatusApproved {
					if firstApprovalRuleID == "" {
						firstApprovalRuleID = rule.ID
					}
					if aiApprovalRuleID == "" {
						aiApprovalRuleID = rule.ID
					}
				}
				decided = true
			default: // human_approval and unknown actions fail closed
				if pendingApproval == nil {
					pendingRuleID = rule.ID
					pendingApproval = newApproval("human_required", "plan includes an action requiring human approval", nil)
				}
				decided = true
			}
			if decided {
				break
			}
		}
		if !decided && pendingApproval == nil {
			pendingApproval = newApproval("human_required", "plan includes an action with no decisive invocation rule", nil)
		}
	}

	if deniedResult != nil {
		plan.MatchedRuleID = ruleIDPtr(deniedRuleID)
		ruleStatus, ruleApproval, ruleFeedback = deniedResult.Status, deniedResult.Approval, deniedResult.Feedback
	} else if pendingApproval != nil {
		plan.MatchedRuleID = ruleIDPtr(pendingRuleID)
		ruleStatus, ruleApproval = PlanStatusPendingApproval, pendingApproval
	} else {
		if aiApprovalRuleID != "" {
			firstApprovalRuleID = aiApprovalRuleID
		}
		plan.MatchedRuleID = ruleIDPtr(firstApprovalRuleID)
		ruleStatus = PlanStatusApproved
		ruleApproval = newApproval("auto_approved", "all planned actions were approved by invocation rules", nil)
	}

	if charterResult == nil {
		result := s.evaluateMandatoryPlanCharter(ctx, plan, agentRec, chatContext, charterLLMConfigID)
		charterResult = &result
	}
	return s.finalizePlanAfterCharterReview(ctx, plan, *charterResult, ruleStatus, ruleApproval, ruleFeedback)
}

// finalizePlanAfterCharterReview runs exactly one local-LLM review of the
// complete plan after invocation-rule evaluation. A charter rejection always
// wins over an approval or human-review result from the rules. An unavailable
// or inconclusive judge fails closed to human approval; it never auto-approves.
func (s *Service) finalizePlanAfterCharterReview(ctx context.Context, plan Plan, charterResult planAIEvaluationResult, ruleStatus PlanStatus, ruleApproval *Approval, ruleFeedback string) (Plan, error) {
	switch charterResult.Status {
	case PlanStatusDenied, PlanStatusNeedsRevision:
		return s.finalizePlanDecision(ctx, plan, charterResult.Status, charterResult.Approval, charterResult.Feedback)
	case PlanStatusPendingApproval:
		if ruleStatus == PlanStatusDenied || ruleStatus == PlanStatusNeedsRevision {
			return s.finalizePlanDecision(ctx, plan, ruleStatus, ruleApproval, ruleFeedback)
		}
		return s.finalizePlanDecision(ctx, plan, PlanStatusPendingApproval, charterResult.Approval, charterResult.Feedback)
	default: // approved charter review preserves the invocation-rule outcome
		return s.finalizePlanDecision(ctx, plan, ruleStatus, ruleApproval, ruleFeedback)
	}
}

func (s *Service) evaluateMandatoryPlanCharter(ctx context.Context, plan Plan, agentRec AgentRecord, chatContext, llmConfigID string) planAIEvaluationResult {
	if s.planJudge == nil {
		return planAIEvaluationResult{Status: PlanStatusPendingApproval,
			Approval: newApproval("ai_escalated", "mandatory plan charter review unavailable (falling back to human_approval)", nil)}
	}
	if strings.TrimSpace(agentRec.Charter) == "" {
		return planAIEvaluationResult{Status: PlanStatusDenied,
			Approval: newApproval("auto_denied", "mandatory plan charter review denied: no charter configured for this agent", nil)}
	}

	evalCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	resp, err := s.planJudge.EvaluatePlan(evalCtx, PlanEvaluateRequest{
		AtryumLLMConfigID: llmConfigID,
		Charter:           agentRec.Charter,
		Source:            plan.Source,
		Goal:              plan.Goal,
		Rationale:         plan.Rationale,
		Context:           combineEvaluationContext("", chatContext),
		Actions:           plan.Actions,
	})
	if err != nil {
		slog.Error("mandatory plan charter review failed; falling back to human approval", "plan_id", plan.PlanID, "error", err)
		return planAIEvaluationResult{Status: PlanStatusPendingApproval,
			Approval: newApproval("ai_escalated", "mandatory plan charter review failed (falling back to human_approval)", nil)}
	}

	switch resp.Verdict {
	case "approved":
		return planAIEvaluationResult{Status: PlanStatusApproved, Verdict: resp.Verdict,
			Approval: newApproval("charter_approved", "mandatory plan charter review approved: "+resp.Reason, resp.Confidence)}
	case "denied":
		return planAIEvaluationResult{Status: PlanStatusDenied, Verdict: resp.Verdict,
			Approval: newApproval("auto_denied", "mandatory plan charter review denied: "+resp.Reason, resp.Confidence)}
	case "revise":
		feedback := resp.Feedback
		if feedback == "" {
			feedback = resp.Reason
		}
		return planAIEvaluationResult{Status: PlanStatusNeedsRevision, Verdict: resp.Verdict,
			Approval: newApproval("ai_revise", "mandatory plan charter review requested revision: "+resp.Reason, resp.Confidence), Feedback: feedback}
	case "next_rule":
		return planAIEvaluationResult{Status: PlanStatusPendingApproval, Verdict: resp.Verdict,
			Approval: newApproval("ai_escalated", "mandatory plan charter review did not establish compliance: "+resp.Reason, resp.Confidence)}
	default: // human_approval, next_rule, and unknown verdicts require a human
		return planAIEvaluationResult{Status: PlanStatusPendingApproval, Verdict: resp.Verdict,
			Approval: newApproval("ai_escalated", "mandatory plan charter review requires human approval: "+resp.Reason, resp.Confidence)}
	}
}

func ruleIDPtr(id string) *string {
	if id == "" {
		return nil
	}
	return &id
}

type planAIEvaluationResult struct {
	Status   PlanStatus
	Verdict  string
	Approval *Approval
	Feedback string
}

// finalizePlanDecision persists a plan's evaluated status and emits the
// matching lifecycle event.
func (s *Service) finalizePlanDecision(ctx context.Context, plan Plan, status PlanStatus, approval *Approval, feedback string) (Plan, error) {
	now := time.Now().UTC()
	plan.Status = status
	plan.Approval = approval
	plan.Feedback = feedback
	switch status {
	case PlanStatusApproved:
		exp := now.Add(time.Duration(plan.TTLSeconds) * time.Second)
		plan.ExpiresAt = &exp
		plan.DecidedAt = &now
	case PlanStatusDenied, PlanStatusNeedsRevision, PlanStatusCancelled:
		plan.DecidedAt = &now
	}
	if err := s.plans.Update(ctx, plan); err != nil {
		return Plan{}, err
	}
	payload := map[string]any{"status": string(status)}
	if approval != nil {
		payload["approval_status"] = approval.Status
		if approval.Reason != nil {
			payload["reason"] = *approval.Reason
		}
	}
	if feedback != "" {
		payload["feedback"] = feedback
	}
	if plan.MatchedRuleID != nil {
		payload["matched_rule_id"] = *plan.MatchedRuleID
	}
	if plan.ExpiresAt != nil {
		payload["expires_at"] = plan.ExpiresAt.Format(time.RFC3339)
	}
	s.recordPlanEvent(ctx, plan.PlanID, "plan."+planEventSuffix(status), payload)
	return plan, nil
}

func planEventSuffix(status PlanStatus) string {
	switch status {
	case PlanStatusPendingApproval:
		return "pending_approval"
	default:
		return string(status)
	}
}

// GetPlan returns a plan by ID, lazily expiring an approved plan whose TTL
// has elapsed.
func (s *Service) GetPlan(ctx context.Context, id string) (Plan, error) {
	if !s.plansEnabled() {
		return Plan{}, fmt.Errorf("plan submission is not enabled")
	}
	plan, err := s.plans.Get(ctx, id)
	if err != nil {
		return Plan{}, err
	}
	return s.expireIfStale(ctx, plan), nil
}

func (s *Service) expireIfStale(ctx context.Context, plan Plan) Plan {
	if plan.Status != PlanStatusApproved || plan.ExpiresAt == nil || time.Now().UTC().Before(*plan.ExpiresAt) {
		return plan
	}
	plan.Status = PlanStatusExpired
	if err := s.plans.Update(ctx, plan); err != nil {
		slog.Warn("could not mark plan expired", "plan_id", plan.PlanID, "error", err)
		return plan
	}
	s.recordPlanEvent(ctx, plan.PlanID, "plan.expired", map[string]any{"expired_at": plan.ExpiresAt.Format(time.RFC3339)})
	return plan
}

// completePlanAfterSuccessfulFinalAction closes an approved plan once its
// final declared action has successfully executed. PlanStepIndex is assigned
// only after adherence review selects an action, so invocations without a
// concrete plan match cannot complete a plan. Failures are logged instead of
// changing the already-persisted successful invocation result.
func (s *Service) completePlanAfterSuccessfulFinalAction(ctx context.Context, inv Invocation) {
	if s.plans == nil || inv.PlanID == nil || inv.PlanStepIndex == nil {
		return
	}
	plan, err := s.plans.Get(ctx, *inv.PlanID)
	if err != nil {
		slog.Warn("could not load plan after successful action", "plan_id", *inv.PlanID, "invocation_id", inv.InvocationID, "error", err)
		return
	}
	if plan.Status != PlanStatusApproved || len(plan.Actions) == 0 || *inv.PlanStepIndex != len(plan.Actions)-1 {
		return
	}
	completedAt := time.Now().UTC()
	plan.Status = PlanStatusCompleted
	// A completed plan can no longer grant a pass. Close its validity window
	// at the same timestamp used for the completion event so the UI does not
	// continue showing the original future TTL.
	plan.ExpiresAt = &completedAt
	if err := s.plans.Update(ctx, plan); err != nil {
		slog.Warn("could not complete plan after final action", "plan_id", plan.PlanID, "invocation_id", inv.InvocationID, "error", err)
		return
	}
	s.recordPlanEvent(ctx, plan.PlanID, "plan.completed", map[string]any{
		"completed_at":  completedAt.Format(time.RFC3339),
		"invocation_id": inv.InvocationID,
	})
}

func (s *Service) ListPlans(ctx context.Context, filter PlanListFilter) (PlanListResponse, error) {
	if !s.plansEnabled() {
		return PlanListResponse{Items: []Plan{}}, nil
	}
	if filter.Limit == 0 {
		filter.Limit = 50
	}
	items, total, err := s.plans.List(ctx, filter)
	if err != nil {
		return PlanListResponse{}, err
	}
	for i := range items {
		items[i] = s.expireIfStale(ctx, items[i])
	}
	if items == nil {
		items = []Plan{}
	}
	return PlanListResponse{Items: items, Total: total, Offset: filter.Offset, Limit: filter.Limit}, nil
}

// ListPlanRevisions returns the direct revisions of a plan, oldest first.
func (s *Service) ListPlanRevisions(ctx context.Context, id string) ([]Plan, error) {
	if !s.plansEnabled() {
		return []Plan{}, nil
	}
	revs, err := s.plans.ListRevisions(ctx, id)
	if err != nil {
		return nil, err
	}
	if revs == nil {
		revs = []Plan{}
	}
	return revs, nil
}

// ApprovePlan records a human approval. ttlSeconds of 0 uses the TTL the
// agent requested at submission, but always reapplies the current deployment
// bounds so lowering the configured ceiling also covers pending stored plans.
func (s *Service) ApprovePlan(ctx context.Context, id string, ttlSeconds int) (Plan, error) {
	if !s.plansEnabled() {
		return Plan{}, fmt.Errorf("plan submission is not enabled")
	}
	plan, err := s.plans.Get(ctx, id)
	if err != nil {
		return Plan{}, err
	}
	if plan.Status != PlanStatusPendingApproval {
		return Plan{}, fmt.Errorf("plan %s is not pending approval (status=%s)", id, plan.Status)
	}
	requestedTTL := plan.TTLSeconds
	if ttlSeconds > 0 {
		requestedTTL = ttlSeconds
	}
	plan.TTLSeconds = s.clampPlanTTL(requestedTTL)
	approval := &Approval{Status: humanDecisionStatus(plan.Approval, true), Reason: stringPtr("human")}
	return s.finalizePlanDecision(ctx, plan, PlanStatusApproved, approval, "")
}

// DenyPlan records a human denial with an optional message.
func (s *Service) DenyPlan(ctx context.Context, id string, message string) (Plan, error) {
	if !s.plansEnabled() {
		return Plan{}, fmt.Errorf("plan submission is not enabled")
	}
	plan, err := s.plans.Get(ctx, id)
	if err != nil {
		return Plan{}, err
	}
	if plan.Status != PlanStatusPendingApproval {
		return Plan{}, fmt.Errorf("plan %s is not pending approval (status=%s)", id, plan.Status)
	}
	reason := "human"
	if message != "" {
		reason = message
	}
	approval := &Approval{Status: humanDecisionStatus(plan.Approval, false), Reason: stringPtr(reason)}
	return s.finalizePlanDecision(ctx, plan, PlanStatusDenied, approval, "")
}

// RequestPlanRevision returns the plan to the agent with reviewer feedback.
// The agent is expected to submit a new plan with revision_of set.
func (s *Service) RequestPlanRevision(ctx context.Context, id string, feedback string) (Plan, error) {
	if !s.plansEnabled() {
		return Plan{}, fmt.Errorf("plan submission is not enabled")
	}
	if strings.TrimSpace(feedback) == "" {
		return Plan{}, fmt.Errorf("feedback is required to request a revision")
	}
	plan, err := s.plans.Get(ctx, id)
	if err != nil {
		return Plan{}, err
	}
	if plan.Status != PlanStatusPendingApproval {
		return Plan{}, fmt.Errorf("plan %s is not pending approval (status=%s)", id, plan.Status)
	}
	approval := &Approval{Status: "revise_requested", Reason: stringPtr("human")}
	return s.finalizePlanDecision(ctx, plan, PlanStatusNeedsRevision, approval, feedback)
}

// ExpirePlan immediately revokes an approved plan's pass.
func (s *Service) ExpirePlan(ctx context.Context, id string) (Plan, error) {
	if !s.plansEnabled() {
		return Plan{}, fmt.Errorf("plan submission is not enabled")
	}
	plan, err := s.plans.Get(ctx, id)
	if err != nil {
		return Plan{}, err
	}
	if plan.Status != PlanStatusApproved {
		return Plan{}, fmt.Errorf("plan %s cannot be expired from status %s", id, plan.Status)
	}
	now := time.Now().UTC()
	plan.ExpiresAt = &now
	return s.finalizePlanDecision(ctx, plan, PlanStatusExpired, plan.Approval, plan.Feedback)
}

// CancelPlan lets the submitting agent withdraw a plan. Cancelling an
// approved plan revokes its pass.
func (s *Service) CancelPlan(ctx context.Context, id string) (Plan, error) {
	if !s.plansEnabled() {
		return Plan{}, fmt.Errorf("plan submission is not enabled")
	}
	plan, err := s.plans.Get(ctx, id)
	if err != nil {
		return Plan{}, err
	}
	switch plan.Status {
	case PlanStatusReceived, PlanStatusPendingApproval, PlanStatusNeedsRevision, PlanStatusApproved:
		// cancellable
	default:
		return Plan{}, fmt.Errorf("plan %s cannot be cancelled from status %s", id, plan.Status)
	}
	return s.finalizePlanDecision(ctx, plan, PlanStatusCancelled, plan.Approval, plan.Feedback)
}

// PlanEvents lists a plan's lifecycle events.
func (s *Service) PlanEvents(ctx context.Context, id string, filter EventListFilter) (EventListResponse, error) {
	if !s.plansEnabled() || s.planEvents == nil {
		return EventListResponse{Items: []EventResponse{}}, nil
	}
	if filter.Limit == 0 {
		filter.Limit = 200
	}
	events, total, err := s.planEvents.ListByPlan(ctx, id, filter)
	if err != nil {
		return EventListResponse{}, err
	}
	items := make([]EventResponse, 0, len(events))
	for _, evt := range events {
		items = append(items, EventResponse{Type: evt.EventType, Timestamp: evt.CreatedAt, Data: evt.Payload})
	}
	return EventListResponse{Items: items, Total: total, Offset: filter.Offset, Limit: filter.Limit}, nil
}

type approvedPlanMatch struct {
	Plan        Plan
	Action      PlanAction
	ActionIndex int
}

// matchApprovedPlan returns the newest approved, unexpired plan of the agent
// that contains an action for the invocation's server. Exact tool matches are
// preferred, but a tool-name mismatch still selects the active plan so aliases
// and harness naming differences are resolved by the adherence judge instead
// of falling through to ordinary approval rules.
//
// The lookup widens beyond the exact runtime agentID to every id registered
// on the same agent record (agents.agent_ids). Without auth, a plan
// submission and the invocation reporting its execution can arrive tagged
// with different self-declared or hinted agent ids for what is really the
// same agent (e.g. an MCP client's self-declared agent_id for atryum.plan.submit
// vs. a hook's separately-configured ATRYUM_AGENT_ID). Requiring byte-for-byte
// equality between those two would leave every plan submitted this way
// unmatchable; registering both ids on one agent record (or authenticating,
// where the verified identity is used consistently everywhere) is how an
// operator makes them the same agent for this purpose.
//
// The third return value reports that more than one action could describe the
// call. Callers must ask the adherence judge to select among those actions.
func (s *Service) matchApprovedPlan(ctx context.Context, agentID, server, tool string) (approvedPlanMatch, bool, bool) {
	if !s.plansEnabled() || agentID == "" || tool == "" {
		return approvedPlanMatch{}, false, false
	}
	// Widen only on a direct agent_ids hit for this exact agentID — never via
	// resolveAgentRecord's "default agent" fallback, which activates for any
	// unrecognized id. Widening off that fallback would let two unrelated
	// anonymous callers land on the same default record and cross-match each
	// other's plans.
	//
	// The exact agentID is always kept alongside any resolved aliases rather
	// than replaced: GetByAgentID can resolve a Claude managed-agent binding
	// (a separate table keyed on claude_agent_id) to an agent record whose
	// own agent_ids array doesn't happen to list that binding id. Dropping
	// agentID in that case would make an authenticated managed agent unable
	// to match its own just-submitted plan.
	agentIDs := []string{agentID}
	if s.agents != nil {
		if rec, err := s.agents.GetByAgentID(ctx, agentID); err == nil && len(rec.AgentIDs) > 0 {
			agentIDs = dedupeAgentIDs(agentIDs, rec.AgentIDs)
		}
	}
	plans, err := s.plans.ListActiveByAgent(ctx, agentIDs)
	if err != nil {
		slog.Warn("plan pass lookup failed; falling back to normal gating", "agent_id", agentID, "error", err)
		return approvedPlanMatch{}, false, false
	}
	for _, plan := range plans {
		plan = s.expireIfStale(ctx, plan)
		if plan.Status != PlanStatusApproved {
			continue
		}
		candidates := planActionCandidates(plan, server, tool)
		if len(candidates) == 0 {
			continue
		}
		if len(candidates) == 1 {
			return candidates[0], true, false
		}
		return approvedPlanMatch{Plan: plan}, false, true
	}
	return approvedPlanMatch{}, false, false
}

// dedupeAgentIDs merges id lists while preserving order and dropping repeats.
func dedupeAgentIDs(lists ...[]string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, list := range lists {
		for _, id := range list {
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// planActionCandidates returns actions on the invocation's server. Exact tool
// matches take precedence. When none exist, all actions on that server remain
// candidates so the adherence judge can handle tool aliases (for example, a
// plan saying "zsh" when the harness reports its shell tool as "bash") and can
// reject genuinely off-plan calls without losing the plan association.
func planActionCandidates(plan Plan, server, tool string) []approvedPlanMatch {
	var serverCandidates []approvedPlanMatch
	var exactCandidates []approvedPlanMatch
	for i, action := range plan.Actions {
		if action.Server != server {
			continue
		}
		candidate := approvedPlanMatch{Plan: plan, Action: action, ActionIndex: i}
		serverCandidates = append(serverCandidates, candidate)
		if action.Tool == tool {
			exactCandidates = append(exactCandidates, candidate)
		}
	}
	if len(exactCandidates) > 0 {
		return exactCandidates
	}
	return serverCandidates
}

// planGateOutcome is the disposition the plan gate assigns to an invocation.
// The plan flow is exclusive: ordinary invocation gating does not run again
// for a call governed by an approved plan. The first current invocation rule
// is consulted only to preserve ordered policy semantics (a leading deny still
// blocks; a leading approval/human/AI gate was satisfied by plan approval),
// then the independent adherence/charter judge decides the concrete call.
type planGateOutcome int

const (
	// planGateApprove grants the plan pass: a status poll or a judged match.
	planGateApprove planGateOutcome = iota
	// planGateApprovePoll grants the plan pass for a judge-confirmed status
	// poll. Unlike planGateApprove it must never bind a plan action or step
	// index: a poll is not the execution of a planned action, and binding
	// one would advance the execution-order high-water mark and could
	// complete the plan before its real actions run.
	planGateApprovePoll
	// planGateDeny rejects the call outright: the adherence judge found it
	// outside the approved plan.
	planGateDeny
	// planGateHuman sends the call straight to human approval: adherence
	// could not be established (no judge, no charter, judge error) or the
	// judge asked for escalation.
	planGateHuman
)

// approvedPlanPass gates an invocation against one candidate action from the
// agent's active approved plan. A status poll of the plan fast-passes;
// everything else must be confirmed by the adherence judge.
func (s *Service) approvedPlanPass(ctx context.Context, match approvedPlanMatch, agentRec AgentRecord, server, tool string, input map[string]any, extraContext string) (string, *float64, planGateOutcome) {
	if planStatusFastPass(s.planPollOrigins, match.Plan.PlanID, input) {
		return "matched approved plan " + match.Plan.PlanID + ": accessing approved plan status", nil, planGateApprove
	}
	if rule, denied := s.matchPlanExecutionRule(ctx, agentRec, server, tool); denied {
		return "tool call denied by matching rule " + rule.ID + " before plan adherence review", nil, planGateDeny
	}
	highestStep, found, err := s.highestExecutedPlanStep(ctx, match.Plan.PlanID)
	if err != nil {
		return "plan " + match.Plan.PlanID + " requires execution-order review", nil, planGateHuman
	}
	if found && match.ActionIndex < highestStep {
		return fmt.Sprintf("tool call would run plan step %d after step %d has already run", match.ActionIndex+1, highestStep+1), nil, planGateDeny
	}
	return s.judgePlanAdherence(ctx, match.Plan, match.Action, agentRec, server, tool, input, extraContext)
}

// ambiguousApprovedPlanPass resolves a call when several actions in the same
// approved plan use the same tool/server. Each candidate is independently
// checked by the adherence judge. Any positive match establishes that the call
// follows the approved plan; when several actions match (typically identical
// duplicate steps the judge cannot tell apart), the earliest un-executed one
// is bound so duplicates progress in declared order and never complete the
// plan early. A call with no positive match is escalated when the judge is
// uncertain and denied when it is outside every candidate.
func (s *Service) ambiguousApprovedPlanPass(ctx context.Context, plan Plan, agentRec AgentRecord, server, tool string, input map[string]any, extraContext string) (approvedPlanMatch, string, *float64, planGateOutcome) {
	if planStatusFastPass(s.planPollOrigins, plan.PlanID, input) {
		return approvedPlanMatch{Plan: plan}, "matched approved plan " + plan.PlanID + ": accessing approved plan status", nil, planGateApprove
	}
	if rule, denied := s.matchPlanExecutionRule(ctx, agentRec, server, tool); denied {
		return approvedPlanMatch{Plan: plan}, "tool call denied by matching rule " + rule.ID + " before plan adherence review", nil, planGateDeny
	}

	highestStep, found, err := s.highestExecutedPlanStep(ctx, plan.PlanID)
	if err != nil {
		return approvedPlanMatch{Plan: plan}, "plan " + plan.PlanID + " requires execution-order review", nil, planGateHuman
	}

	var matches []approvedPlanMatch
	var matchReason string
	var matchConfidence *float64
	uncertain := false
	for _, candidate := range planActionCandidates(plan, server, tool) {
		if found && candidate.ActionIndex < highestStep {
			continue
		}
		reason, confidence, outcome := s.judgePlanAdherence(ctx, plan, candidate.Action, agentRec, server, tool, input, extraContext)
		switch outcome {
		case planGateApprove:
			matches = append(matches, candidate)
			if len(matches) == 1 {
				matchReason, matchConfidence = reason, confidence
			}
		case planGateApprovePoll:
			// A status poll is a property of the concrete call, not of the
			// candidate action under consideration — grant the pass without
			// binding an action so the poll cannot advance plan progress.
			return approvedPlanMatch{Plan: plan}, reason, confidence, planGateApprovePoll
		case planGateHuman:
			uncertain = true
		case planGateDeny:
			// A charter violation applies to the concrete call regardless of
			// which plan action is considered, so it is safe to stop here.
			if strings.HasPrefix(reason, "tool call violates agent charter:") {
				return approvedPlanMatch{Plan: plan}, reason, confidence, planGateDeny
			}
		}
	}

	if len(matches) > 0 {
		// Matches are collected in plan order and every candidate is already
		// at or above the executed high-water mark. When several candidates
		// match — typically identical duplicate actions the judge cannot
		// tell apart (the call text is the same for both) — bind the
		// EARLIEST one: duplicates then progress in declared order, and a
		// first occurrence can never bind to the final duplicate and
		// complete the plan before the steps between them have run. The
		// trade-off is that a single call genuinely combining several
		// approved actions records only its earliest constituent, leaving
		// the plan open until the later steps run individually — strictly
		// safer than closing it early.
		return matches[0], matchReason, matchConfidence, planGateApprove
	}
	if uncertain {
		return approvedPlanMatch{Plan: plan}, "plan adherence could not confirm that the call follows an approved action", nil, planGateHuman
	}
	return approvedPlanMatch{Plan: plan}, "tool call is outside every matching action in approved plan " + plan.PlanID, nil, planGateDeny
}

// matchPlanExecutionRule reapplies the first matching invocation rule at
// execution time using ordinary priority order. A leading auto-deny still
// blocks a plan action (including a deny added after plan approval). Any other
// leading rule has already been satisfied by the approved plan: auto-approve
// needs no further gate, while human-approval and AI-evaluation were resolved
// during plan submission. The independent adherence judge still checks the
// concrete call against both the approved action and the charter afterward.
func (s *Service) matchPlanExecutionRule(ctx context.Context, agentRec AgentRecord, server, tool string) (ApprovalRule, bool) {
	if s.rules == nil {
		return ApprovalRule{}, false
	}
	rules, err := s.rules.ListApprovalRules(ctx)
	if err != nil {
		slog.Warn("could not evaluate invocation rules before plan adherence", "server", server, "tool", tool, "error", err)
		return ApprovalRule{}, false
	}
	for _, rule := range matchRules(rules, server, tool, agentRec.ID) {
		return rule, rule.Action == RuleActionAutoDeny
	}
	return ApprovalRule{}, false
}

// highestExecutedPlanStep returns the latest declared action that has been
// approved to run for a plan. A plan may skip steps, but never move backwards
// after a later step has been approved or started.
func (s *Service) highestExecutedPlanStep(ctx context.Context, planID string) (int, bool, error) {
	invs, _, err := s.invocations.List(ctx, InvocationListFilter{PlanID: planID, Limit: 500})
	if err != nil {
		slog.Warn("could not determine plan execution order; escalating to human approval", "plan_id", planID, "error", err)
		return 0, false, err
	}
	highest := -1
	for _, inv := range invs {
		if inv.PlanStepIndex == nil {
			continue
		}
		switch inv.Status {
		case StatusApproved, StatusExecuting, StatusSucceeded, StatusFailed:
			if *inv.PlanStepIndex > highest {
				highest = *inv.PlanStepIndex
			}
		}
	}
	return highest, highest >= 0, nil
}

// judgePlanAdherence submits the concrete call to the adherence judge and
// maps its verdict to a gate outcome. Every plan-gated call other than a
// status poll comes through here regardless of how the plan was approved —
// human-approved and auto-approved plans get no deterministic pass.
func (s *Service) judgePlanAdherence(ctx context.Context, plan Plan, action PlanAction, agentRec AgentRecord, server, tool string, input map[string]any, extraContext string) (string, *float64, planGateOutcome) {
	if s.planJudge == nil {
		slog.Warn("plan adherence judge unavailable; escalating plan-gated call to human approval",
			"plan_id", plan.PlanID, "tool", tool)
		return "plan " + plan.PlanID + " requires adherence review but no judge is configured", nil, planGateHuman
	}
	if agentRec.Charter == "" {
		slog.Warn("plan adherence judge skipped: agent has no charter; escalating to human approval",
			"plan_id", plan.PlanID, "agent_id", plan.AgentID)
		return "plan " + plan.PlanID + " requires adherence review but agent has no charter", nil, planGateHuman
	}

	// The evaluator configured by an ai_evaluation rule is also used for its
	// adherence check. Human-approved and auto-approved plans are judged the
	// same way with an empty config ID, which the evaluator resolves to its
	// default enabled LLM config.
	llmConfigID := ""
	if plan.MatchedRuleID != nil && s.rules != nil {
		if rule, ok := s.planAdherenceRule(ctx, *plan.MatchedRuleID); ok && rule.Action == RuleActionAIEvaluation {
			llmConfigID = rule.AtryumLLMConfigID
		}
	}

	evalCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	resp, err := s.planJudge.EvaluatePlanAdherence(evalCtx, PlanAdherenceRequest{
		AtryumLLMConfigID: llmConfigID,
		Charter:           agentRec.Charter,
		Plan:              plan,
		Action:            action,
		ServerName:        server,
		ToolName:          tool,
		ToolArgs:          input,
		Context:           combineEvaluationContext("", extraContext),
	})
	if err != nil {
		slog.Error("plan adherence judge failed; escalating to human approval",
			"plan_id", plan.PlanID, "error", err)
		return "plan " + plan.PlanID + " requires adherence review but judge failed", nil, planGateHuman
	}

	switch resp.Verdict {
	case "follows_plan":
		reason := "follows approved plan " + plan.PlanID
		if strings.TrimSpace(resp.Reason) != "" {
			reason += ": " + resp.Reason
		}
		return reason, resp.Confidence, planGateApprove
	case "status_poll":
		reason := "status poll of approved plan " + plan.PlanID
		if strings.TrimSpace(resp.Reason) != "" {
			reason += ": " + resp.Reason
		}
		return reason, resp.Confidence, planGateApprovePoll
	case "different_action":
		// The call belongs to the plan but not to this candidate: never a
		// match for it. Escalate rather than deny — when another candidate
		// is the right one, its own positive match wins over this.
		reason := "tool call executes a different action of approved plan " + plan.PlanID
		if strings.TrimSpace(resp.Reason) != "" {
			reason += ": " + resp.Reason
		}
		return reason, resp.Confidence, planGateHuman
	case "outside_plan", "violates_charter":
		slog.Info("plan adherence judge rejected plan-gated call",
			"plan_id", plan.PlanID, "tool", tool, "reason", resp.Reason)
		if resp.Verdict == "violates_charter" {
			return "tool call violates agent charter: " + resp.Reason, resp.Confidence, planGateDeny
		}
		return "tool call is outside approved plan " + plan.PlanID + ": " + resp.Reason, resp.Confidence, planGateDeny
	default:
		slog.Info("plan adherence judge escalated plan-gated call",
			"plan_id", plan.PlanID, "tool", tool, "reason", resp.Reason)
		return "tool call needs human review for approved plan " + plan.PlanID + ": " + resp.Reason, resp.Confidence, planGateHuman
	}
}

func (s *Service) planAdherenceRule(ctx context.Context, ruleID string) (ApprovalRule, bool) {
	rules, err := s.rules.ListApprovalRules(ctx)
	if err != nil {
		slog.Warn("plan adherence rule lookup failed; adherence judge will use no rule-specific LLM config",
			"rule_id", ruleID, "error", err)
		return ApprovalRule{}, false
	}
	for _, rule := range rules {
		if rule.ID == ruleID {
			return rule, true
		}
	}
	return ApprovalRule{}, false
}

// ─── Plan-status fast pass ────────────────────────────────────────────────
//
// The routine submit-then-poll loop would otherwise pay one adherence-judge
// LLM call per poll. The fast pass skips the judge ONLY when the tool input,
// taken as a whole, provably is a plain read-only poll of this plan's own
// status: one field must BE the poll (an anchored match, never substring
// containment) and every other field must be inert. Anything unaccounted for
// falls through to the judge with the full call — this path is an
// optimization, never a verdict.

// planPollMetadataKeys are input fields harnesses attach alongside a command
// but never execute (e.g. Claude Code's Bash "description"). Their content
// cannot change what runs, so they may accompany a poll command.
var planPollMetadataKeys = map[string]bool{
	"description": true, "explanation": true, "justification": true,
	"reason": true, "summary": true, "title": true,
}

// planPollScalarKeys are the only non-string fields a poll may carry. A
// boolean or number under any other key could give a tool operational
// meaning we cannot reason about, so it forfeits the fast pass.
var planPollScalarKeys = map[string]bool{
	"timeout": true, "timeout_ms": true, "timeout_seconds": true,
	"max_time": true, "connect_timeout": true,
	"run_in_background": true, "background": true,
}

// planOriginSet holds the normalized scheme://host origins this Atryum API
// is reachable on. The fast pass fails closed: a poll addressed to any other
// origin — even with the right path — goes to the adherence judge. Host
// alone is not enough: a matching path on an attacker-controlled host would
// exfiltrate the caller's bearer token, and an http downgrade of an https
// origin would expose it in cleartext.
type planOriginSet map[string]bool

// newPlanOriginSet normalizes entries to scheme://host[:port] keys with
// default ports stripped. The scheme is pinned: a scheme-less entry is
// treated as http (loopback convenience); trusting a host over https
// requires spelling out the https:// URL.
func newPlanOriginSet(entries []string) planOriginSet {
	set := planOriginSet{}
	for _, entry := range entries {
		entry = strings.TrimSpace(strings.ToLower(entry))
		if entry == "" {
			continue
		}
		if !strings.Contains(entry, "://") {
			entry = "http://" + entry
		}
		u, err := neturl.Parse(entry)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			continue
		}
		set[u.Scheme+"://"+stripDefaultPort(u.Scheme, u.Host)] = true
	}
	return set
}

func (o planOriginSet) contains(u *neturl.URL) bool {
	return o[u.Scheme+"://"+stripDefaultPort(u.Scheme, strings.ToLower(u.Host))]
}

func stripDefaultPort(scheme, host string) string {
	switch scheme {
	case "http":
		return strings.TrimSuffix(host, ":80")
	case "https":
		return strings.TrimSuffix(host, ":443")
	}
	return host
}

const planPollURLToken = `https?://[^\s"'` + "`" + `;|&<>(){}]+`

// planPollCurlAllowedArg whitelists curl arguments that cannot change the
// request from a plain GET, write the response anywhere, or weaken transport
// security: read-only flags, timeouts, and headers. Notably absent:
// -X/--request and -d/--data* (method/body changes), -o/-O (file writes),
// -k/--insecure (a network attacker could impersonate the trusted origin),
// and -L/--location (curl forwards -H headers — including Authorization —
// to every redirect target, so a redirect could carry the bearer token off
// the trusted origin; the status endpoint never redirects anyway).
const planPollCurlAllowedArg = `-[fsSv]+` +
	`|--(?:fail|silent|show-error|compressed)` +
	`|(?:-m|--max-time|--connect-timeout)\s+\d+` +
	`|-H\s+(?:"[^"]*"|'[^']*'|[^\s"']+)`

var (
	planPollCurlRe = regexp.MustCompile(
		`^curl(?:\s+(?:` + planPollCurlAllowedArg + `))*` +
			`\s+["']?(` + planPollURLToken + `)["']?` +
			`(?:\s+(?:` + planPollCurlAllowedArg + `))*\s*$`)
	planPollURLRe = regexp.MustCompile(`^` + planPollURLToken + `$`)
	// A single trailing pipe to a plain jq filter is the only compound shell
	// syntax a poll may use.
	planPollJQRe = regexp.MustCompile(`^\|\s*jq(?:\s+[A-Za-z0-9_.,\[\]"' -]*)?$`)
)

func planStatusFastPass(origins planOriginSet, planID string, input map[string]any) bool {
	if len(origins) == 0 || planID == "" || len(input) == 0 {
		return false
	}
	matched := false
	for key, value := range input {
		switch v := value.(type) {
		case string:
			if planStatusPollValue(origins, planID, v) {
				matched = true
				continue
			}
			if strings.EqualFold(key, "method") && strings.EqualFold(strings.TrimSpace(v), "get") {
				continue
			}
			if planPollMetadataKeys[strings.ToLower(key)] &&
				!strings.Contains(strings.ToLower(v), "/api/v1/external/plans/") {
				continue
			}
			return false
		case []any:
			if planStatusPollArgv(origins, planID, v) {
				matched = true
				continue
			}
			return false
		case bool, float64, int, int64, nil:
			if !planPollScalarKeys[strings.ToLower(key)] {
				return false
			}
		default:
			return false
		}
	}
	return matched
}

// planStatusPollArgv matches argv-style command fields: either the curl argv
// itself or a shell -c wrapper around a single poll command.
func planStatusPollArgv(origins planOriginSet, planID string, argv []any) bool {
	parts := make([]string, 0, len(argv))
	for _, item := range argv {
		s, ok := item.(string)
		if !ok {
			return false
		}
		parts = append(parts, s)
	}
	if len(parts) == 3 && (parts[0] == "bash" || parts[0] == "sh" || parts[0] == "zsh") &&
		(parts[1] == "-c" || parts[1] == "-lc") {
		return planStatusPollValue(origins, planID, parts[2])
	}
	return planStatusPollValue(origins, planID, strings.Join(parts, " "))
}

// planStatusPollValue reports whether the value in its ENTIRETY is a
// read-only fetch of the plan's status from a trusted origin: either the
// bare status URL, or a whitelisted curl GET of it, optionally piped to jq.
func planStatusPollValue(origins planOriginSet, planID, value string) bool {
	text := strings.TrimSpace(value)
	if text == "" || strings.ContainsAny(text, "`;&<>(){}\n\r") {
		return false
	}
	if i := strings.IndexByte(text, '|'); i >= 0 {
		if !planPollJQRe.MatchString(text[i:]) {
			return false
		}
		text = strings.TrimSpace(text[:i])
	}
	if planPollURLRe.MatchString(text) {
		return planPollURLMatches(origins, planID, text)
	}
	m := planPollCurlRe.FindStringSubmatch(text)
	if m == nil {
		return false
	}
	return planPollURLMatches(origins, planID, m[1])
}

func planPollURLMatches(origins planOriginSet, planID, raw string) bool {
	u, err := neturl.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") ||
		u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	if !origins.contains(u) {
		return false
	}
	return strings.EqualFold(strings.TrimSuffix(u.Path, "/"), "/api/v1/external/plans/"+planID)
}
