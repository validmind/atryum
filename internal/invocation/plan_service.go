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
// matchApprovedPlan never grants a pass. statusPollOrigins lists the hosts
// (host or host:port, or full URLs) this Atryum API is reachable on; the
// plan-status fast pass only trusts polls addressed to one of them.
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

func clampPlanTTL(seconds int) int {
	if seconds <= 0 {
		return planDefaultTTLSeconds
	}
	if seconds > planMaxTTLSeconds {
		return planMaxTTLSeconds
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
	for i, a := range req.Actions {
		if strings.TrimSpace(a.Tool) == "" {
			return Plan{}, fmt.Errorf("actions[%d].tool is required", i)
		}
	}
	source := req.Source
	if source == "" {
		source = "external"
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
		TTLSeconds:  clampPlanTTL(req.TTLSeconds),
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
		Context:           chatContext,
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

// ApprovePlan records a human approval. ttlSeconds of 0 keeps the TTL the
// agent requested at submission.
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
	if ttlSeconds > 0 {
		plan.TTLSeconds = clampPlanTTL(ttlSeconds)
	}
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
	Plan   Plan
	Action PlanAction
}

// matchApprovedPlan returns the newest approved, unexpired plan of the agent
// with a declared action matching (server, tool). It is the deterministic
// plan-pass check run before rule matching in Invoke/Submit; it never matches
// when the plan feature is unused or the caller is anonymous.
func (s *Service) matchApprovedPlan(ctx context.Context, agentID, server, tool string) (approvedPlanMatch, bool) {
	if !s.plansEnabled() || agentID == "" || tool == "" {
		return approvedPlanMatch{}, false
	}
	plans, err := s.plans.ListActiveByAgent(ctx, agentID)
	if err != nil {
		slog.Warn("plan pass lookup failed; falling back to normal gating", "agent_id", agentID, "error", err)
		return approvedPlanMatch{}, false
	}
	for _, plan := range plans {
		plan = s.expireIfStale(ctx, plan)
		if plan.Status != PlanStatusApproved {
			continue
		}
		for _, a := range plan.Actions {
			if a.Tool == tool && (a.Server == "" || a.Server == server) {
				return approvedPlanMatch{Plan: plan, Action: a}, true
			}
		}
	}
	return approvedPlanMatch{}, false
}

func (s *Service) approvedPlanPass(ctx context.Context, match approvedPlanMatch, agentRec AgentRecord, server, tool string, input map[string]any, extraContext string) (string, *float64, bool) {
	plan := match.Plan
	reason := "matched approved plan " + plan.PlanID
	if planStatusFastPass(s.planPollOrigins, plan.PlanID, input) {
		return reason + ": accessing approved plan status", nil, true
	}
	if plan.MatchedRuleID == nil || s.rules == nil || s.planJudge == nil {
		return reason, nil, true
	}

	rule, ok := s.planAdherenceRule(ctx, *plan.MatchedRuleID)
	if !ok || rule.Action != RuleActionAIEvaluation || rule.AtryumLLMConfigID == "" {
		return reason, nil, true
	}
	if agentRec.Charter == "" {
		slog.Warn("plan adherence judge skipped: agent has no charter; falling back to normal gating",
			"plan_id", plan.PlanID, "agent_id", plan.AgentID, "rule_id", rule.ID)
		return "approved plan " + plan.PlanID + " requires adherence review but agent has no charter", nil, false
	}

	evalCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	resp, err := s.planJudge.EvaluatePlanAdherence(evalCtx, PlanAdherenceRequest{
		AtryumLLMConfigID: rule.AtryumLLMConfigID,
		Charter:           agentRec.Charter,
		Plan:              plan,
		Action:            match.Action,
		ServerName:        server,
		ToolName:          tool,
		ToolArgs:          input,
		Context:           extraContext,
	})
	if err != nil {
		slog.Error("plan adherence judge failed; falling back to normal gating",
			"plan_id", plan.PlanID, "rule_id", rule.ID, "error", err)
		return "approved plan " + plan.PlanID + " requires adherence review but judge failed", nil, false
	}

	switch resp.Verdict {
	case "follows_plan":
		if strings.TrimSpace(resp.Reason) != "" {
			reason += ": " + resp.Reason
		}
		return reason, resp.Confidence, true
	case "outside_plan":
		slog.Info("plan adherence judge rejected plan pass",
			"plan_id", plan.PlanID, "rule_id", rule.ID, "tool", tool, "reason", resp.Reason)
		return "tool call is outside approved plan " + plan.PlanID + ": " + resp.Reason, resp.Confidence, false
	default:
		slog.Info("plan adherence judge escalated plan pass",
			"plan_id", plan.PlanID, "rule_id", rule.ID, "tool", tool, "reason", resp.Reason)
		return "tool call needs human review for approved plan " + plan.PlanID + ": " + resp.Reason, resp.Confidence, false
	}
}

func (s *Service) planAdherenceRule(ctx context.Context, ruleID string) (ApprovalRule, bool) {
	rules, err := s.rules.ListApprovalRules(ctx)
	if err != nil {
		slog.Warn("plan adherence rule lookup failed; allowing deterministic plan pass",
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
// request from a plain GET or write the response anywhere: read-only flags,
// timeouts, and headers. Notably absent: -X/--request, -d/--data*, -o/-O.
const planPollCurlAllowedArg = `-[fsSLkv]+` +
	`|--(?:fail|silent|show-error|location|insecure|compressed)` +
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
