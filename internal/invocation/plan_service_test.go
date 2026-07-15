package invocation_test

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"testing"
	"time"

	"atryum/internal/config"
	"atryum/internal/invocation"
	"atryum/internal/mcp"
	"atryum/internal/store"

	_ "modernc.org/sqlite"
)

type planJudgeStub struct {
	mu            sync.Mutex
	req           invocation.PlanEvaluateRequest
	resp          invocation.PlanEvaluateResponse
	err           error
	adherenceReq  invocation.PlanAdherenceRequest
	adherenceResp invocation.PlanAdherenceResponse
	adherenceErr  error
}

func (s *planJudgeStub) EvaluatePlan(_ context.Context, req invocation.PlanEvaluateRequest) (invocation.PlanEvaluateResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.req = req
	return s.resp, s.err
}

func (s *planJudgeStub) EvaluatePlanAdherence(_ context.Context, req invocation.PlanAdherenceRequest) (invocation.PlanAdherenceResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.adherenceReq = req
	return s.adherenceResp, s.adherenceErr
}

func (s *planJudgeStub) request() invocation.PlanEvaluateRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.req
}

func (s *planJudgeStub) adherenceRequest() invocation.PlanAdherenceRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.adherenceReq
}

// newPlanTestService builds a Service backed by real sqlite repos with the
// plan feature installed. Returns the service and the plans repo for direct
// row manipulation in expiry tests.
func newPlanTestService(t *testing.T, rules []invocation.ApprovalRule, agents invocation.AgentLookup, judge invocation.PlanEvaluator) (*invocation.Service, *store.PlansRepo) {
	t.Helper()
	db := newSQLiteTestDB(t)
	serverRepo := store.NewServerRepo(db)
	resolver := mcp.NewResolver(serverRepo, config.Config{})
	if err := resolver.BootstrapIfEmpty(context.Background()); err != nil {
		t.Fatal(err)
	}
	var rulesStore invocation.ApprovalRule
	_ = rulesStore
	svc := invocation.NewService(
		store.NewInvocationRepo(db),
		store.NewEventRepo(db),
		resolver,
		mcp.NewHTTPClient(),
		nil,
		5*time.Second,
		rulesStoreStub{rules: rules},
		agents,
		nil,
		nil,
	)
	plansRepo := store.NewPlansRepo(db)
	svc.SetPlanStore(plansRepo, store.NewPlanEventsRepo(db), judge, []string{"localhost:8080"})
	return svc, plansRepo
}

func planSubmit(agentID string, tools ...string) invocation.PlanSubmitRequest {
	actions := make([]invocation.PlanAction, 0, len(tools))
	for _, tool := range tools {
		actions = append(actions, invocation.PlanAction{Tool: tool})
	}
	return invocation.PlanSubmitRequest{
		AgentID: agentID,
		Goal:    "achieve x",
		Actions: actions,
	}
}

func TestSubmitPlanValidation(t *testing.T) {
	svc, _ := newPlanTestService(t, nil, nil, nil)
	ctx := context.Background()

	if _, err := svc.SubmitPlan(ctx, invocation.PlanSubmitRequest{Goal: "g", Actions: []invocation.PlanAction{{Tool: "Bash"}}}); err == nil || !strings.Contains(err.Error(), "agent identity") {
		t.Fatalf("expected agent identity error, got %v", err)
	}
	if _, err := svc.SubmitPlan(ctx, invocation.PlanSubmitRequest{AgentID: "a", Actions: []invocation.PlanAction{{Tool: "Bash"}}}); err == nil || !strings.Contains(err.Error(), "goal") {
		t.Fatalf("expected goal error, got %v", err)
	}
	if _, err := svc.SubmitPlan(ctx, invocation.PlanSubmitRequest{AgentID: "a", Goal: "g"}); err == nil || !strings.Contains(err.Error(), "action") {
		t.Fatalf("expected actions error, got %v", err)
	}
}

func TestSubmitPlanDefaultsToHumanApproval(t *testing.T) {
	svc, _ := newPlanTestService(t, nil, nil, nil)
	plan, err := svc.SubmitPlan(context.Background(), planSubmit("agent-a", "Bash"))
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != invocation.PlanStatusPendingApproval {
		t.Fatalf("status = %s, want pending_approval", plan.Status)
	}
	if !strings.HasPrefix(plan.PlanID, "plan_") {
		t.Fatalf("plan id = %q", plan.PlanID)
	}
}

func TestSubmitPlanAutoApproveRule(t *testing.T) {
	rules := []invocation.ApprovalRule{{
		ID:        "rule-1",
		Action:    invocation.RuleActionAutoApprove,
		AppliesTo: invocation.RuleScopePlan,
		Enabled:   true,
	}}
	svc, _ := newPlanTestService(t, rules, nil, nil)
	plan, err := svc.SubmitPlan(context.Background(), planSubmit("agent-a", "Bash", "read_file"))
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != invocation.PlanStatusApproved {
		t.Fatalf("status = %s, want approved", plan.Status)
	}
	if plan.Approval == nil || plan.Approval.Status != "auto_approved" {
		t.Fatalf("approval = %+v", plan.Approval)
	}
	if plan.ExpiresAt == nil || !plan.ExpiresAt.After(time.Now().UTC()) {
		t.Fatalf("expires_at = %v", plan.ExpiresAt)
	}
	if plan.MatchedRuleID == nil || *plan.MatchedRuleID != "rule-1" {
		t.Fatalf("matched rule = %v", plan.MatchedRuleID)
	}
}

func TestSubmitPlanAutoDenyRule(t *testing.T) {
	rules := []invocation.ApprovalRule{{
		ID:        "rule-1",
		Action:    invocation.RuleActionAutoDeny,
		AppliesTo: invocation.RuleScopePlan,
		Enabled:   true,
	}}
	svc, _ := newPlanTestService(t, rules, nil, nil)
	plan, err := svc.SubmitPlan(context.Background(), planSubmit("agent-a", "Bash"))
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != invocation.PlanStatusDenied {
		t.Fatalf("status = %s, want denied", plan.Status)
	}
}

func TestPlanRuleRequiresEveryActionToolCovered(t *testing.T) {
	rules := []invocation.ApprovalRule{{
		ID:           "rule-1",
		Action:       invocation.RuleActionAutoApprove,
		AppliesTo:    invocation.RuleScopePlan,
		ToolPatterns: []string{"Bash"},
		Enabled:      true,
	}}
	svc, _ := newPlanTestService(t, rules, nil, nil)
	// One action (read_file) is outside the rule's tool patterns: the rule
	// must not fire and the plan falls back to human review.
	plan, err := svc.SubmitPlan(context.Background(), planSubmit("agent-a", "Bash", "read_file"))
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != invocation.PlanStatusPendingApproval {
		t.Fatalf("status = %s, want pending_approval", plan.Status)
	}
}

func TestInvocationRulesIgnorePlanScopedRules(t *testing.T) {
	// A plan-scoped auto_deny rule must not affect regular invocation gating.
	rules := []invocation.ApprovalRule{{
		ID:        "rule-1",
		Action:    invocation.RuleActionAutoDeny,
		AppliesTo: invocation.RuleScopePlan,
		Enabled:   true,
	}}
	svc, _ := newPlanTestService(t, rules, nil, nil)
	resp, err := svc.Submit(context.Background(), invocation.ExternalSubmitRequest{
		Tool:    "Bash",
		AgentID: "agent-a",
		Input:   map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != invocation.StatusPendingApproval {
		t.Fatalf("status = %s, want pending_approval (plan rule must not deny invocations)", resp.Status)
	}
}

func TestSubmitPlanAIEvaluationVerdicts(t *testing.T) {
	agentRec := invocation.AgentRecord{ID: "cuid-a", Charter: "be careful"}
	agents := agentLookupStub{byAgentID: map[string]invocation.AgentRecord{"agent-a": agentRec}}
	rules := []invocation.ApprovalRule{{
		ID:                "rule-ai",
		Action:            invocation.RuleActionAIEvaluation,
		AppliesTo:         invocation.RuleScopePlan,
		AtryumLLMConfigID: "llm-1",
		Enabled:           true,
	}}

	cases := []struct {
		verdict      string
		feedback     string
		wantStatus   invocation.PlanStatus
		wantApproval string
	}{
		{"approved", "", invocation.PlanStatusApproved, "auto_approved"},
		{"denied", "", invocation.PlanStatusDenied, "auto_denied"},
		{"revise", "pin the version", invocation.PlanStatusNeedsRevision, "ai_revise"},
		{"human_approval", "", invocation.PlanStatusPendingApproval, "ai_escalated"},
	}
	for _, tc := range cases {
		t.Run(tc.verdict, func(t *testing.T) {
			judge := &planJudgeStub{resp: invocation.PlanEvaluateResponse{Verdict: tc.verdict, Reason: "r", Feedback: tc.feedback}}
			svc, _ := newPlanTestService(t, rules, agents, judge)
			plan, err := svc.SubmitPlan(context.Background(), planSubmit("agent-a", "Bash"))
			if err != nil {
				t.Fatal(err)
			}
			if plan.Status != tc.wantStatus {
				t.Fatalf("status = %s, want %s", plan.Status, tc.wantStatus)
			}
			if plan.Approval == nil || plan.Approval.Status != tc.wantApproval {
				t.Fatalf("approval = %+v, want status %s", plan.Approval, tc.wantApproval)
			}
			if tc.verdict == "revise" && plan.Feedback != "pin the version" {
				t.Fatalf("feedback = %q", plan.Feedback)
			}
			if judge.request().Charter != "be careful" {
				t.Fatalf("judge charter = %q", judge.request().Charter)
			}
		})
	}
}

func TestSubmitPlanAIEvaluationWithoutCharterDenies(t *testing.T) {
	rules := []invocation.ApprovalRule{{
		ID:                "rule-ai",
		Action:            invocation.RuleActionAIEvaluation,
		AppliesTo:         invocation.RuleScopePlan,
		AtryumLLMConfigID: "llm-1",
		Enabled:           true,
	}}
	judge := &planJudgeStub{resp: invocation.PlanEvaluateResponse{Verdict: "approved"}}
	svc, _ := newPlanTestService(t, rules, agentLookupStub{}, judge)
	plan, err := svc.SubmitPlan(context.Background(), planSubmit("agent-a", "Bash"))
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != invocation.PlanStatusDenied {
		t.Fatalf("status = %s, want denied when no charter", plan.Status)
	}
}

func TestApprovedPlanGrantsPassToMatchingSubmit(t *testing.T) {
	agents := agentLookupStub{byAgentID: map[string]invocation.AgentRecord{
		"agent-a": {ID: "agent-rec-a", Charter: "Only run planned safe commands."},
	}}
	judge := &planJudgeStub{adherenceResp: invocation.PlanAdherenceResponse{Verdict: "follows_plan", Reason: "matches human-approved plan"}}
	svc, _ := newPlanTestService(t, nil, agents, judge)
	ctx := context.Background()

	plan, err := svc.SubmitPlan(ctx, planSubmit("agent-a", "Bash", "read_file"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApprovePlan(ctx, plan.PlanID, 0); err != nil {
		t.Fatal(err)
	}

	// A declared tool auto-approves once the adherence judge confirms it,
	// with the plan recorded — even for a human-approved plan.
	resp, err := svc.Submit(ctx, invocation.ExternalSubmitRequest{Tool: "Bash", AgentID: "agent-a", Input: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != invocation.StatusApproved {
		t.Fatalf("status = %s, want approved", resp.Status)
	}
	if resp.Approval == nil || resp.Approval.Status != "plan_approved" {
		t.Fatalf("approval = %+v", resp.Approval)
	}
	if resp.PlanID == nil || *resp.PlanID != plan.PlanID {
		t.Fatalf("plan_id = %v, want %s", resp.PlanID, plan.PlanID)
	}
	if got := judge.adherenceRequest(); got.Plan.PlanID != plan.PlanID || got.ToolArgs == nil {
		t.Fatalf("human-approved plan must be adherence-judged, got %+v", got)
	}

	// An undeclared tool does not match the plan and hits normal gating.
	resp, err = svc.Submit(ctx, invocation.ExternalSubmitRequest{Tool: "delete_everything", AgentID: "agent-a", Input: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != invocation.StatusPendingApproval {
		t.Fatalf("undeclared tool status = %s, want pending_approval", resp.Status)
	}
	if resp.PlanID != nil {
		t.Fatalf("undeclared tool should not carry plan_id, got %v", *resp.PlanID)
	}

	// A different agent gets no pass.
	resp, err = svc.Submit(ctx, invocation.ExternalSubmitRequest{Tool: "Bash", AgentID: "agent-b", Input: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != invocation.StatusPendingApproval {
		t.Fatalf("other agent status = %s, want pending_approval", resp.Status)
	}
}

func TestAutoApprovedPlanRequiresAdherenceJudgeForMatchingSubmit(t *testing.T) {
	rules := []invocation.ApprovalRule{{
		ID: "rule-auto", Action: invocation.RuleActionAutoApprove,
		AppliesTo: invocation.RuleScopePlan, Enabled: true,
	}}
	agents := agentLookupStub{byAgentID: map[string]invocation.AgentRecord{
		"agent-a": {ID: "agent-rec-a", Charter: "Only run planned safe commands."},
	}}
	judge := &planJudgeStub{adherenceResp: invocation.PlanAdherenceResponse{Verdict: "outside_plan", Reason: "arguments exceed plan"}}
	svc, _ := newPlanTestService(t, rules, agents, judge)

	if _, err := svc.SubmitPlan(context.Background(), planSubmit("agent-a", "Bash")); err != nil {
		t.Fatal(err)
	}
	resp, err := svc.Submit(context.Background(), invocation.ExternalSubmitRequest{
		Tool: "Bash", AgentID: "agent-a", Input: map[string]any{"cmd": "rm -rf /"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != invocation.StatusDenied {
		t.Fatalf("status = %s, want denied after adherence rejection", resp.Status)
	}
	if resp.Approval == nil || resp.Approval.Status != "plan_denied" {
		t.Fatalf("approval = %+v, want plan_denied", resp.Approval)
	}
	if got := judge.adherenceRequest(); got.ToolArgs["cmd"] != "rm -rf /" {
		t.Fatalf("auto-approved plan must send concrete arguments to adherence judge, got %+v", got)
	}
}

func TestAIEvaluatedPlanRequiresAdherenceJudgeForMatchingSubmit(t *testing.T) {
	rules := []invocation.ApprovalRule{{
		ID:                "rule-plan-ai",
		Action:            invocation.RuleActionAIEvaluation,
		AppliesTo:         invocation.RuleScopePlan,
		AtryumLLMConfigID: "llm-1",
		Enabled:           true,
	}}
	agents := agentLookupStub{byAgentID: map[string]invocation.AgentRecord{
		"agent-a": {ID: "agent-rec-a", Charter: "Only run planned safe commands."},
	}}
	judge := &planJudgeStub{
		resp:          invocation.PlanEvaluateResponse{Verdict: "approved", Reason: "plan ok"},
		adherenceResp: invocation.PlanAdherenceResponse{Verdict: "follows_plan", Reason: "matches planned command"},
	}
	svc, _ := newPlanTestService(t, rules, agents, judge)
	ctx := context.Background()

	plan, err := svc.SubmitPlan(ctx, planSubmit("agent-a", "Bash"))
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != invocation.PlanStatusApproved {
		t.Fatalf("plan status = %s, want approved", plan.Status)
	}

	resp, err := svc.Submit(ctx, invocation.ExternalSubmitRequest{Tool: "Bash", AgentID: "agent-a", Input: map[string]any{"cmd": "echo ok"}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != invocation.StatusApproved || resp.Approval == nil || resp.Approval.Status != "plan_approved" {
		t.Fatalf("resp = status %s approval %+v, want plan approved", resp.Status, resp.Approval)
	}
	if resp.Approval.Reason == nil || !strings.Contains(*resp.Approval.Reason, "matches planned command") {
		t.Fatalf("approval reason = %v", resp.Approval.Reason)
	}
	got := judge.adherenceRequest()
	if got.Plan.PlanID != plan.PlanID || got.Action.Tool != "Bash" || got.ToolArgs["cmd"] != "echo ok" {
		t.Fatalf("adherence request = %+v", got)
	}
}

func TestPlanGatedCallDeniedWhenAdherenceJudgeRejects(t *testing.T) {
	rules := []invocation.ApprovalRule{{
		ID:                "rule-plan-ai",
		Action:            invocation.RuleActionAIEvaluation,
		AppliesTo:         invocation.RuleScopePlan,
		AtryumLLMConfigID: "llm-1",
		Enabled:           true,
	}}
	agents := agentLookupStub{byAgentID: map[string]invocation.AgentRecord{
		"agent-a": {ID: "agent-rec-a", Charter: "Only run planned safe commands."},
	}}
	judge := &planJudgeStub{
		resp:          invocation.PlanEvaluateResponse{Verdict: "approved", Reason: "plan ok"},
		adherenceResp: invocation.PlanAdherenceResponse{Verdict: "outside_plan", Reason: "command deletes unrelated files"},
	}
	svc, _ := newPlanTestService(t, rules, agents, judge)
	ctx := context.Background()

	if _, err := svc.SubmitPlan(ctx, planSubmit("agent-a", "Bash")); err != nil {
		t.Fatal(err)
	}
	resp, err := svc.Submit(ctx, invocation.ExternalSubmitRequest{Tool: "Bash", AgentID: "agent-a", Input: map[string]any{"cmd": "rm -rf tmp"}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != invocation.StatusDenied {
		t.Fatalf("status = %s, want denied when adherence judge rejects", resp.Status)
	}
	if resp.Approval == nil || resp.Approval.Status != "plan_denied" || !strings.Contains(*resp.Approval.Reason, "outside approved plan") {
		t.Fatalf("approval = %+v", resp.Approval)
	}
	if resp.PlanID == nil {
		t.Fatal("plan_id should be retained when adherence rejects")
	}
}

func TestAIEvaluatedPlanAllowsReadingOwnPlanStatus(t *testing.T) {
	rules := []invocation.ApprovalRule{{
		ID:                "rule-plan-ai",
		Action:            invocation.RuleActionAIEvaluation,
		AppliesTo:         invocation.RuleScopePlan,
		AtryumLLMConfigID: "llm-1",
		Enabled:           true,
	}}
	agents := agentLookupStub{byAgentID: map[string]invocation.AgentRecord{
		"agent-a": {ID: "agent-rec-a", Charter: "Only run planned safe commands."},
	}}
	judge := &planJudgeStub{
		resp:          invocation.PlanEvaluateResponse{Verdict: "approved", Reason: "plan ok"},
		adherenceResp: invocation.PlanAdherenceResponse{Verdict: "outside_plan", Reason: "would otherwise reject"},
	}
	svc, _ := newPlanTestService(t, rules, agents, judge)
	ctx := context.Background()

	plan, err := svc.SubmitPlan(ctx, planSubmit("agent-a", "Bash"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := `curl -fsS -H "Authorization: Bearer $ATRYUM_ACCESS_TOKEN" http://localhost:8080/api/v1/external/plans/` + plan.PlanID + ` | jq .`
	resp, err := svc.Submit(ctx, invocation.ExternalSubmitRequest{Tool: "Bash", AgentID: "agent-a", Input: map[string]any{"cmd": cmd}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != invocation.StatusApproved || resp.Approval == nil || resp.Approval.Status != "plan_approved" {
		t.Fatalf("resp = status %s approval %+v, want plan approved", resp.Status, resp.Approval)
	}
	if resp.Approval.Reason == nil || !strings.Contains(*resp.Approval.Reason, "status") {
		t.Fatalf("approval reason = %v", resp.Approval.Reason)
	}
	if got := judge.adherenceRequest(); got.Plan.PlanID != "" {
		t.Fatalf("adherence judge should not be called for plan status read, got %+v", got)
	}
}

// A status poll with anything else riding along must not take the fast pass:
// the adherence judge has to see the entire call, and its rejection denies
// the call outright.
func TestPlanStatusPollWithExtraCommandGoesToJudge(t *testing.T) {
	rules := []invocation.ApprovalRule{{
		ID:                "rule-plan-ai",
		Action:            invocation.RuleActionAIEvaluation,
		AppliesTo:         invocation.RuleScopePlan,
		AtryumLLMConfigID: "llm-1",
		Enabled:           true,
	}}
	agents := agentLookupStub{byAgentID: map[string]invocation.AgentRecord{
		"agent-a": {ID: "agent-rec-a", Charter: "Only run planned safe commands."},
	}}
	judge := &planJudgeStub{
		resp:          invocation.PlanEvaluateResponse{Verdict: "approved", Reason: "plan ok"},
		adherenceResp: invocation.PlanAdherenceResponse{Verdict: "outside_plan", Reason: "poll is chained to a destructive command"},
	}
	svc, _ := newPlanTestService(t, rules, agents, judge)
	ctx := context.Background()

	plan, err := svc.SubmitPlan(ctx, planSubmit("agent-a", "Bash"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := `curl -fsS http://localhost:8080/api/v1/external/plans/` + plan.PlanID + ` && rm -rf /`
	resp, err := svc.Submit(ctx, invocation.ExternalSubmitRequest{Tool: "Bash", AgentID: "agent-a", Input: map[string]any{"cmd": cmd}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != invocation.StatusDenied {
		t.Fatalf("status = %s, want denied when adherence judge rejects", resp.Status)
	}
	got := judge.adherenceRequest()
	if got.Plan.PlanID != plan.PlanID {
		t.Fatalf("adherence judge was not called, got %+v", got)
	}
	if args, _ := got.ToolArgs["cmd"].(string); args != cmd {
		t.Fatalf("judge must see the entire call, got cmd = %q", args)
	}
}

func TestPendingPlanGrantsNoPass(t *testing.T) {
	svc, _ := newPlanTestService(t, nil, nil, nil)
	ctx := context.Background()
	if _, err := svc.SubmitPlan(ctx, planSubmit("agent-a", "Bash")); err != nil {
		t.Fatal(err)
	}
	resp, err := svc.Submit(ctx, invocation.ExternalSubmitRequest{Tool: "Bash", AgentID: "agent-a", Input: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != invocation.StatusPendingApproval {
		t.Fatalf("status = %s, want pending_approval while plan is undecided", resp.Status)
	}
}

func TestExpiredPlanDoesNotMatchAndFlipsExpired(t *testing.T) {
	svc, plansRepo := newPlanTestService(t, nil, nil, nil)
	ctx := context.Background()

	plan, err := svc.SubmitPlan(ctx, planSubmit("agent-a", "Bash"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApprovePlan(ctx, plan.PlanID, 0); err != nil {
		t.Fatal(err)
	}
	// Force the pass into the past.
	stored, err := plansRepo.Get(ctx, plan.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	past := time.Now().UTC().Add(-time.Minute)
	stored.ExpiresAt = &past
	if err := plansRepo.Update(ctx, stored); err != nil {
		t.Fatal(err)
	}

	resp, err := svc.Submit(ctx, invocation.ExternalSubmitRequest{Tool: "Bash", AgentID: "agent-a", Input: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != invocation.StatusPendingApproval {
		t.Fatalf("status = %s, want pending_approval after expiry", resp.Status)
	}
	got, err := svc.GetPlan(ctx, plan.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != invocation.PlanStatusExpired {
		t.Fatalf("plan status = %s, want expired", got.Status)
	}
}

func TestPlanRevisionFlow(t *testing.T) {
	agents := agentLookupStub{byAgentID: map[string]invocation.AgentRecord{
		"agent-a": {ID: "agent-rec-a", Charter: "Only run planned safe commands."},
	}}
	judge := &planJudgeStub{adherenceResp: invocation.PlanAdherenceResponse{Verdict: "follows_plan"}}
	svc, _ := newPlanTestService(t, nil, agents, judge)
	ctx := context.Background()

	plan, err := svc.SubmitPlan(ctx, planSubmit("agent-a", "Bash"))
	if err != nil {
		t.Fatal(err)
	}

	// Revise requires feedback.
	if _, err := svc.RequestPlanRevision(ctx, plan.PlanID, "  "); err == nil {
		t.Fatal("expected error for empty feedback")
	}
	revised, err := svc.RequestPlanRevision(ctx, plan.PlanID, "add a dry-run step")
	if err != nil {
		t.Fatal(err)
	}
	if revised.Status != invocation.PlanStatusNeedsRevision || revised.Feedback != "add a dry-run step" {
		t.Fatalf("revised = %+v", revised)
	}

	// Agent polls, sees feedback, resubmits linked revision.
	req := planSubmit("agent-a", "Bash", "read_file")
	req.RevisionOf = plan.PlanID
	rev2, err := svc.SubmitPlan(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if rev2.Revision != 2 || rev2.ParentPlanID == nil || *rev2.ParentPlanID != plan.PlanID {
		t.Fatalf("rev2 = %+v", rev2)
	}
	parent, err := svc.GetPlan(ctx, plan.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	if parent.Status != invocation.PlanStatusSuperseded {
		t.Fatalf("parent status = %s, want superseded", parent.Status)
	}
	revs, err := svc.ListPlanRevisions(ctx, plan.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	if len(revs) != 1 || revs[0].PlanID != rev2.PlanID {
		t.Fatalf("revisions = %+v", revs)
	}

	// Approving the revision grants the pass to the revised action set.
	if _, err := svc.ApprovePlan(ctx, rev2.PlanID, 0); err != nil {
		t.Fatal(err)
	}
	resp, err := svc.Submit(ctx, invocation.ExternalSubmitRequest{Tool: "read_file", AgentID: "agent-a", Input: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != invocation.StatusApproved || resp.PlanID == nil || *resp.PlanID != rev2.PlanID {
		t.Fatalf("resp = status %s plan %v", resp.Status, resp.PlanID)
	}
}

func TestRevisionOfApprovedPlanRevokesPass(t *testing.T) {
	svc, _ := newPlanTestService(t, nil, nil, nil)
	ctx := context.Background()

	plan, err := svc.SubmitPlan(ctx, planSubmit("agent-a", "Bash"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApprovePlan(ctx, plan.PlanID, 0); err != nil {
		t.Fatal(err)
	}

	req := planSubmit("agent-a", "Bash")
	req.RevisionOf = plan.PlanID
	if _, err := svc.SubmitPlan(ctx, req); err != nil {
		t.Fatal(err)
	}

	// The superseded parent's pass no longer applies; the revision is pending.
	resp, err := svc.Submit(ctx, invocation.ExternalSubmitRequest{Tool: "Bash", AgentID: "agent-a", Input: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != invocation.StatusPendingApproval {
		t.Fatalf("status = %s, want pending_approval after supersede", resp.Status)
	}
}

func TestPlanRevisionGuards(t *testing.T) {
	svc, _ := newPlanTestService(t, nil, nil, nil)
	ctx := context.Background()

	plan, err := svc.SubmitPlan(ctx, planSubmit("agent-a", "Bash"))
	if err != nil {
		t.Fatal(err)
	}

	// Wrong agent cannot revise someone else's plan.
	req := planSubmit("agent-b", "Bash")
	req.RevisionOf = plan.PlanID
	if _, err := svc.SubmitPlan(ctx, req); err == nil || !strings.Contains(err.Error(), "different agent") {
		t.Fatalf("expected different-agent error, got %v", err)
	}

	// Denied plans cannot be revised.
	if _, err := svc.DenyPlan(ctx, plan.PlanID, "no"); err != nil {
		t.Fatal(err)
	}
	req = planSubmit("agent-a", "Bash")
	req.RevisionOf = plan.PlanID
	if _, err := svc.SubmitPlan(ctx, req); err == nil || !strings.Contains(err.Error(), "cannot be revised") {
		t.Fatalf("expected cannot-be-revised error, got %v", err)
	}
}

func TestCancelPlanRevokesPass(t *testing.T) {
	svc, _ := newPlanTestService(t, nil, nil, nil)
	ctx := context.Background()

	plan, err := svc.SubmitPlan(ctx, planSubmit("agent-a", "Bash"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApprovePlan(ctx, plan.PlanID, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CancelPlan(ctx, plan.PlanID); err != nil {
		t.Fatal(err)
	}
	resp, err := svc.Submit(ctx, invocation.ExternalSubmitRequest{Tool: "Bash", AgentID: "agent-a", Input: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != invocation.StatusPendingApproval {
		t.Fatalf("status = %s, want pending_approval after cancel", resp.Status)
	}
}

func TestPlanDecisionGuards(t *testing.T) {
	svc, _ := newPlanTestService(t, nil, nil, nil)
	ctx := context.Background()

	plan, err := svc.SubmitPlan(ctx, planSubmit("agent-a", "Bash"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApprovePlan(ctx, plan.PlanID, 0); err != nil {
		t.Fatal(err)
	}
	// Double-decide is rejected.
	if _, err := svc.ApprovePlan(ctx, plan.PlanID, 0); err == nil {
		t.Fatal("expected error approving an already-approved plan")
	}
	if _, err := svc.DenyPlan(ctx, plan.PlanID, ""); err == nil {
		t.Fatal("expected error denying an already-approved plan")
	}
	if _, err := svc.GetPlan(ctx, "plan_missing"); err != sql.ErrNoRows {
		t.Fatalf("get missing plan err = %v, want sql.ErrNoRows", err)
	}
}

func TestPlanFeatureDisabledWithoutSetPlanStore(t *testing.T) {
	// Without SetPlanStore the service behaves exactly as before.
	svc := newTestService(t, config.Config{})
	ctx := context.Background()

	if _, err := svc.SubmitPlan(ctx, planSubmit("agent-a", "Bash")); err == nil || !strings.Contains(err.Error(), "not enabled") {
		t.Fatalf("expected not-enabled error, got %v", err)
	}
	list, err := svc.ListPlans(ctx, invocation.PlanListFilter{})
	if err != nil || len(list.Items) != 0 {
		t.Fatalf("ListPlans = %+v, %v", list, err)
	}
	// Regular external gating still works with no plan store installed.
	resp, err := svc.Submit(ctx, invocation.ExternalSubmitRequest{Tool: "Bash", AgentID: "agent-a", Input: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != invocation.StatusPendingApproval {
		t.Fatalf("status = %s, want pending_approval", resp.Status)
	}
}

func TestPlanEventsRecorded(t *testing.T) {
	svc, _ := newPlanTestService(t, nil, nil, nil)
	ctx := context.Background()

	plan, err := svc.SubmitPlan(ctx, planSubmit("agent-a", "Bash"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApprovePlan(ctx, plan.PlanID, 0); err != nil {
		t.Fatal(err)
	}
	events, err := svc.PlanEvents(ctx, plan.PlanID, invocation.EventListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	types := make([]string, 0, len(events.Items))
	for _, e := range events.Items {
		types = append(types, e.Type)
	}
	want := []string{"plan.received", "plan.pending_approval", "plan.approved"}
	if len(types) != len(want) {
		t.Fatalf("events = %v, want %v", types, want)
	}
	for i := range want {
		if types[i] != want[i] {
			t.Fatalf("events = %v, want %v", types, want)
		}
	}
}
