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
	ListActiveByAgent(ctx context.Context, agentID string) ([]Plan, error)
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

// SubmitPlan records a proposed plan and evaluates it against plan-scoped
// approval rules. Callers poll GetPlan until the status leaves
// received/pending_approval; the returned Plan reflects the immediate outcome
// for auto_approve/auto_deny/ai_evaluation rules.
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

// evaluatePlanRules runs plan-scoped approval rules in priority order and
// persists the resulting status. Plans never silently auto-approve: with no
// matching rule the plan waits for a human.
func (s *Service) evaluatePlanRules(ctx context.Context, plan Plan, chatContext string) (Plan, error) {
	agentRec := s.resolveAgentRecord(ctx, plan.AgentID)

	if s.rules != nil {
		if approvalRules, err := s.rules.ListApprovalRules(ctx); err == nil {
			// A plan cannot use an AI (or an auto-approve rule) to bypass a
			// deny rule that applies to any of its individual steps. A denial
			// asks the agent to revise the batch before any side effect occurs.
			for _, rule := range approvalRules {
				if rule.Action != RuleActionAutoDeny || !planSafetyRuleMatches(rule, plan.Source, plan.Actions, agentRec.ID) {
					continue
				}
				plan.MatchedRuleID = ruleIDPtr(rule.ID)
				feedback := "A planned action matches a deny rule. Revise the plan to remove or replace that action."
				return s.finalizePlanDecision(ctx, plan, PlanStatusNeedsRevision,
					newApproval("auto_denied", "plan requires revision: matched deny rule", nil), feedback)
			}

			// Likewise, if any step requires human approval, the entire plan
			// waits for a human. AI evaluation may still be useful for plans
			// without such steps, but it must not approve around this gate.
			for _, rule := range approvalRules {
				if rule.Action != RuleActionHumanApproval || !planSafetyRuleMatches(rule, plan.Source, plan.Actions, agentRec.ID) {
					continue
				}
				plan.MatchedRuleID = ruleIDPtr(rule.ID)
				return s.finalizePlanDecision(ctx, plan, PlanStatusPendingApproval,
					newApproval("human_required", "plan includes an action requiring human approval", nil), "")
			}

			for _, rule := range matchPlanRules(approvalRules, plan.Source, plan.Actions, agentRec.ID) {
				r := rule
				if r.ID != "" {
					id := r.ID
					plan.MatchedRuleID = &id
				}
				switch r.Action {
				case RuleActionAutoApprove:
					return s.finalizePlanDecision(ctx, plan, PlanStatusApproved,
						newApproval("auto_approved", "matched approval rule (auto_approve)", nil), "")
				case RuleActionAutoDeny:
					return s.finalizePlanDecision(ctx, plan, PlanStatusDenied,
						newApproval("auto_denied", "matched approval rule (auto_deny)", nil), "")
				case RuleActionAIEvaluation:
					verdict, done := s.runPlanAIEvaluation(ctx, &r, &plan, agentRec, chatContext)
					if !done {
						plan.MatchedRuleID = nil
						continue // next_rule: keep iterating
					}
					return verdict, nil
				default: // human_approval
					return s.finalizePlanDecision(ctx, plan, PlanStatusPendingApproval, nil, "")
				}
			}
		}
	}
	// No matching rule: default to human review.
	plan.MatchedRuleID = nil
	return s.finalizePlanDecision(ctx, plan, PlanStatusPendingApproval, nil, "")
}

func ruleIDPtr(id string) *string {
	if id == "" {
		return nil
	}
	return &id
}

// planSafetyRuleMatches reports whether a deny or human-approval rule applies
// to at least one planned action. Invocation-scoped rules use each action's
// server (or the plan source when omitted); plan-scoped rules use the plan
// source. Grants remain governed by matchPlanRules, which requires a rule to
// cover the whole plan before it can approve it.
func planSafetyRuleMatches(rule ApprovalRule, source string, actions []PlanAction, agentCUID string) bool {
	if !rule.Enabled || !matchAgentCUIDs(rule.AgentCUIDs, agentCUID) {
		return false
	}
	for _, action := range actions {
		// Judge each action by the server it actually targets, whatever the
		// rule's scope: a deny/human rule scoped to server B must catch an
		// action explicitly declared on B even when the plan was submitted
		// through another source.
		server := action.Server
		if server == "" {
			server = source
		}
		if matchPatterns(rule.ServerPatterns, server) && matchPatterns(rule.ToolPatterns, action.Tool) {
			return true
		}
	}
	return false
}

// runPlanAIEvaluation judges the plan with the locally-configured LLM.
// Returns done=false when the verdict defers to the next rule.
func (s *Service) runPlanAIEvaluation(ctx context.Context, rule *ApprovalRule, plan *Plan, agentRec AgentRecord, chatContext string) (Plan, bool) {
	if rule.AtryumLLMConfigID == "" || s.planJudge == nil {
		// VM-backend plan judging is not supported yet; fail open to a human.
		slog.Warn("plan ai_evaluation: no local LLM config on rule (VM backend not supported for plans); falling back to human_approval",
			"rule_id", rule.ID, "plan_id", plan.PlanID)
		p, _ := s.finalizePlanDecision(ctx, *plan, PlanStatusPendingApproval,
			newApproval("ai_escalated", "plan ai_evaluation unavailable (falling back to human_approval)", nil), "")
		return p, true
	}
	if agentRec.Charter == "" {
		slog.Error("plan ai_evaluation: agent has no charter configured; denying plan",
			"rule_id", rule.ID, "plan_id", plan.PlanID, "agent_id", plan.AgentID)
		p, _ := s.finalizePlanDecision(ctx, *plan, PlanStatusDenied,
			newApproval("auto_denied", "plan ai_evaluation denied: no charter configured for this agent", nil), "")
		return p, true
	}

	evalCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	resp, err := s.planJudge.EvaluatePlan(evalCtx, PlanEvaluateRequest{
		AtryumLLMConfigID: rule.AtryumLLMConfigID,
		Charter:           agentRec.Charter,
		Source:            plan.Source,
		Goal:              plan.Goal,
		Rationale:         plan.Rationale,
		Context:           combineEvaluationContext("", chatContext),
		Actions:           plan.Actions,
	})
	if err != nil {
		slog.Error("plan ai_evaluation: LLM call failed; falling back to human_approval",
			"rule_id", rule.ID, "plan_id", plan.PlanID, "error", err)
		p, _ := s.finalizePlanDecision(ctx, *plan, PlanStatusPendingApproval,
			newApproval("ai_escalated", "plan ai_evaluation: LLM call failed (falling back to human_approval)", nil), "")
		return p, true
	}

	switch resp.Verdict {
	case "approved":
		p, _ := s.finalizePlanDecision(ctx, *plan, PlanStatusApproved,
			newApproval("auto_approved", "plan ai_evaluation approved: "+resp.Reason, resp.Confidence), "")
		return p, true
	case "denied":
		p, _ := s.finalizePlanDecision(ctx, *plan, PlanStatusDenied,
			newApproval("auto_denied", "plan ai_evaluation denied: "+resp.Reason, resp.Confidence), "")
		return p, true
	case "revise":
		feedback := resp.Feedback
		if feedback == "" {
			feedback = resp.Reason
		}
		p, _ := s.finalizePlanDecision(ctx, *plan, PlanStatusNeedsRevision,
			newApproval("ai_revise", "plan ai_evaluation requested revision: "+resp.Reason, resp.Confidence), feedback)
		return p, true
	case "human_approval":
		p, _ := s.finalizePlanDecision(ctx, *plan, PlanStatusPendingApproval,
			newApproval("ai_escalated", "plan ai_evaluation requires human approval: "+resp.Reason, resp.Confidence), "")
		return p, true
	default: // "next_rule"
		slog.Info("plan ai_evaluation: LLM deferred to next rule", "rule_id", rule.ID, "plan_id", plan.PlanID)
		return Plan{}, false
	}
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
// with a declared action matching (server, tool). It is the deterministic
// plan-pass check run before rule matching in Invoke/Submit; it never matches
// when the plan feature is unused or the caller is anonymous.
// The third return value reports that a plan action matched the tool/server,
// but could not be selected unambiguously. Callers must not fall through to
// ordinary approval rules in that case.
func (s *Service) matchApprovedPlan(ctx context.Context, agentID, server, tool string) (approvedPlanMatch, bool, bool) {
	if !s.plansEnabled() || agentID == "" || tool == "" {
		return approvedPlanMatch{}, false, false
	}
	plans, err := s.plans.ListActiveByAgent(ctx, agentID)
	if err != nil {
		slog.Warn("plan pass lookup failed; falling back to normal gating", "agent_id", agentID, "error", err)
		return approvedPlanMatch{}, false, false
	}
	for _, plan := range plans {
		plan = s.expireIfStale(ctx, plan)
		if plan.Status != PlanStatusApproved {
			continue
		}
		var candidates []approvedPlanMatch
		for i, a := range plan.Actions {
			if a.Tool == tool && a.Server == server {
				candidates = append(candidates, approvedPlanMatch{Plan: plan, Action: a, ActionIndex: i})
			}
		}
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

// planGateOutcome is the disposition the plan gate assigns to an invocation.
// The plan flow is exclusive: approval rules are never consulted for a call
// gated by a plan.
type planGateOutcome int

const (
	// planGateApprove grants the plan pass: a status poll or a judged match.
	planGateApprove planGateOutcome = iota
	// planGateDeny rejects the call outright: the adherence judge found it
	// outside the approved plan.
	planGateDeny
	// planGateHuman sends the call straight to human approval: adherence
	// could not be established (no judge, no charter, judge error) or the
	// judge asked for escalation.
	planGateHuman
)

// approvedPlanPass gates an invocation that matched an approved plan: the
// agent's newest approved plan declaring this tool. A status poll of the plan
// fast-passes; everything else must be confirmed by the adherence judge.
func (s *Service) approvedPlanPass(ctx context.Context, match approvedPlanMatch, agentRec AgentRecord, server, tool string, input map[string]any, extraContext string) (string, *float64, planGateOutcome) {
	if planStatusFastPass(s.planPollOrigins, match.Plan.PlanID, input) {
		return "matched approved plan " + match.Plan.PlanID + ": accessing approved plan status", nil, planGateApprove
	}
	if rule, ok := s.matchInvocationAutoDeny(ctx, agentRec, server, tool); ok {
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
// follows the approved plan; when several actions match, select the latest one
// so execution-order tracking treats every earlier matching step as completed.
// A call with no positive match is escalated when the judge is uncertain and
// denied when it is outside every candidate.
func (s *Service) ambiguousApprovedPlanPass(ctx context.Context, plan Plan, agentRec AgentRecord, server, tool string, input map[string]any, extraContext string) (approvedPlanMatch, string, *float64, planGateOutcome) {
	if planStatusFastPass(s.planPollOrigins, plan.PlanID, input) {
		return approvedPlanMatch{Plan: plan}, "matched approved plan " + plan.PlanID + ": accessing approved plan status", nil, planGateApprove
	}
	if rule, ok := s.matchInvocationAutoDeny(ctx, agentRec, server, tool); ok {
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
	for i, action := range plan.Actions {
		if action.Tool != tool || action.Server != server {
			continue
		}
		if found && i < highestStep {
			continue
		}
		candidate := approvedPlanMatch{Plan: plan, Action: action, ActionIndex: i}
		reason, confidence, outcome := s.judgePlanAdherence(ctx, plan, action, agentRec, server, tool, input, extraContext)
		switch outcome {
		case planGateApprove:
			matches = append(matches, candidate)
			matchReason, matchConfidence = reason, confidence
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
		// Matches are collected in plan order. Recording the latest positive
		// match prevents a later combined call from being followed by an
		// earlier action that the same call already satisfied.
		return matches[len(matches)-1], matchReason, matchConfidence, planGateApprove
	}
	if uncertain {
		return approvedPlanMatch{Plan: plan}, "plan adherence could not confirm that the call follows an approved action", nil, planGateHuman
	}
	return approvedPlanMatch{Plan: plan}, "tool call is outside every matching action in approved plan " + plan.PlanID, nil, planGateDeny
}

// matchInvocationAutoDeny reapplies invocation-scoped deny rules at execution
// time. Rules can change after a plan is approved, so an approved plan never
// bypasses a current deny policy before its adherence judge runs.
func (s *Service) matchInvocationAutoDeny(ctx context.Context, agentRec AgentRecord, server, tool string) (ApprovalRule, bool) {
	if s.rules == nil {
		return ApprovalRule{}, false
	}
	rules, err := s.rules.ListApprovalRules(ctx)
	if err != nil {
		slog.Warn("could not evaluate invocation deny rules before plan adherence", "server", server, "tool", tool, "error", err)
		return ApprovalRule{}, false
	}
	for _, rule := range matchRules(rules, server, tool, agentRec.ID) {
		if rule.Action == RuleActionAutoDeny {
			return rule, true
		}
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
