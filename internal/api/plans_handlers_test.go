package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"atryum/internal/invocation"
)

func newPlanStub() invocation.Plan {
	return invocation.Plan{
		PlanID:      "plan_123",
		AgentID:     "agent-a",
		Goal:        "upgrade dependency",
		Actions:     []invocation.PlanAction{{Tool: "Bash", Description: "npm audit"}},
		Status:      invocation.PlanStatusPendingApproval,
		Revision:    1,
		TTLSeconds:  3600,
		SubmittedAt: time.Now().UTC(),
	}
}

func TestExternalPlanSubmit(t *testing.T) {
	stub := &stubService{plan: newPlanStub()}
	h := NewHandler(stub, stubServerService{}, nil, nil, nil, nil, nil, nil, nil, nil)

	body := strings.NewReader(`{
		"agent_id": "agent-a",
		"goal": "upgrade dependency",
		"rationale": "three steps",
		"actions": [{"tool": "Bash", "description": "npm audit"}],
		"ttl_seconds": 1800
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/external/plans", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Atryum-Client-Name", "amp")
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if stub.planSubmitReq == nil {
		t.Fatal("SubmitPlan not called")
	}
	if stub.planSubmitReq.Goal != "upgrade dependency" || len(stub.planSubmitReq.Actions) != 1 {
		t.Fatalf("submit req = %+v", stub.planSubmitReq)
	}
	if stub.planSubmitReq.ClientName != "amp" {
		t.Fatalf("client name header fallback missing: %+v", stub.planSubmitReq)
	}
	if stub.planSubmitReq.TTLSeconds != 1800 {
		t.Fatalf("ttl = %d", stub.planSubmitReq.TTLSeconds)
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["plan_id"] != "plan_123" || resp["status"] != "pending_approval" {
		t.Fatalf("resp = %v", resp)
	}
}

func TestExternalPlanPollAndCancel(t *testing.T) {
	stub := &stubService{plan: newPlanStub()}
	h := NewHandler(stub, stubServerService{}, nil, nil, nil, nil, nil, nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/external/plans/plan_123", nil)
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("poll: expected 200, got %d body=%s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/external/plans/plan_123/cancel", nil)
	w = httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("cancel: expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if stub.planCancelID != "plan_123" {
		t.Fatalf("cancel id = %q", stub.planCancelID)
	}

	// Unsupported methods are rejected.
	req = httptest.NewRequest(http.MethodDelete, "/api/v1/external/plans/plan_123", nil)
	w = httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("delete: expected 405, got %d", w.Code)
	}
}

func TestExternalPlanEndpointsScopedToCallingAgent(t *testing.T) {
	// The stub plan belongs to "agent-a". With the auth validator unset the
	// noAuthAgentIDHint middleware turns ?agent_id= into the request identity,
	// exercising the same auth.AgentIDFromContext path a verified JWT would.
	stub := &stubService{plan: newPlanStub()}
	h := NewHandler(stub, stubServerService{}, nil, nil, nil, nil, nil, nil, nil, nil)

	// Owner can poll.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/external/plans/plan_123?agent_id=agent-a", nil)
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("owner poll: expected 200, got %d", w.Code)
	}

	// A different identified agent gets 404, not the plan.
	req = httptest.NewRequest(http.MethodGet, "/api/v1/external/plans/plan_123?agent_id=intruder", nil)
	w = httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("intruder poll: expected 404, got %d body=%s", w.Code, w.Body.String())
	}

	// A different identified agent cannot cancel; the service is never called.
	req = httptest.NewRequest(http.MethodPost, "/api/v1/external/plans/plan_123/cancel?agent_id=intruder", nil)
	w = httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("intruder cancel: expected 404, got %d", w.Code)
	}
	if stub.planCancelID != "" {
		t.Fatalf("CancelPlan must not be called for a non-owner, got id %q", stub.planCancelID)
	}

	// Owner can cancel.
	req = httptest.NewRequest(http.MethodPost, "/api/v1/external/plans/plan_123/cancel?agent_id=agent-a", nil)
	w = httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("owner cancel: expected 200, got %d", w.Code)
	}
	if stub.planCancelID != "plan_123" {
		t.Fatalf("cancel id = %q", stub.planCancelID)
	}

	// Anonymous callers (no identity at all) keep the existing open behavior.
	req = httptest.NewRequest(http.MethodGet, "/api/v1/external/plans/plan_123", nil)
	w = httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("anonymous poll: expected 200, got %d", w.Code)
	}
}

func TestAdminPlansListAndDetail(t *testing.T) {
	stub := &stubService{plan: newPlanStub()}
	h := NewHandler(stub, stubServerService{}, nil, nil, nil, nil, nil, nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/plans?status=pending_approval", nil)
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("list: expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	var list struct {
		Items []map[string]any `json:"items"`
		Total int              `json:"total"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if list.Total != 1 || len(list.Items) != 1 {
		t.Fatalf("list = %+v", list)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/admin/plans/plan_123", nil)
	w = httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("detail: expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	var detail map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail["plan_id"] != "plan_123" {
		t.Fatalf("detail = %v", detail)
	}
	if _, ok := detail["revisions"]; !ok {
		t.Fatal("detail missing revisions field")
	}
}

func TestAdminPlanDecisions(t *testing.T) {
	stub := &stubService{plan: newPlanStub()}
	h := NewHandler(stub, stubServerService{}, nil, nil, nil, nil, nil, nil, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/plans/plan_123/approve", strings.NewReader(`{"ttl_seconds": 600}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("approve: expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if stub.planApproveID != "plan_123" || stub.planTTL != 600 {
		t.Fatalf("approve call = id %q ttl %d", stub.planApproveID, stub.planTTL)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/admin/plans/plan_123/deny", strings.NewReader(`{"message": "too risky"}`))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("deny: expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if stub.planDenyID != "plan_123" || stub.planDenyMsg != "too risky" {
		t.Fatalf("deny call = id %q msg %q", stub.planDenyID, stub.planDenyMsg)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/admin/plans/plan_123/revise", strings.NewReader(`{"feedback": "pin the version"}`))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("revise: expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if stub.planReviseID != "plan_123" || stub.planFeedback != "pin the version" {
		t.Fatalf("revise call = id %q feedback %q", stub.planReviseID, stub.planFeedback)
	}

	// Revise without feedback is rejected before hitting the service.
	stub.planReviseID = ""
	req = httptest.NewRequest(http.MethodPost, "/api/v1/admin/plans/plan_123/revise", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("revise without feedback: expected 400, got %d", w.Code)
	}
	if stub.planReviseID != "" {
		t.Fatal("service must not be called when feedback is empty")
	}
}

func TestAdminRuleValidationAcceptsAppliesTo(t *testing.T) {
	if err := validateRuleInput(AdminRuleInput{Action: "human_approval", AppliesTo: "plan"}); err != nil {
		t.Fatalf("plan scope rejected: %v", err)
	}
	if err := validateRuleInput(AdminRuleInput{Action: "human_approval", AppliesTo: "invocation"}); err != nil {
		t.Fatalf("invocation scope rejected: %v", err)
	}
	if err := validateRuleInput(AdminRuleInput{Action: "human_approval"}); err != nil {
		t.Fatalf("empty scope rejected: %v", err)
	}
	if err := validateRuleInput(AdminRuleInput{Action: "human_approval", AppliesTo: "bogus"}); err == nil {
		t.Fatal("bogus scope accepted")
	}
}

func TestExternalPlanSubmitSourceQueryParam(t *testing.T) {
	stub := &stubService{plan: newPlanStub()}
	h := NewHandler(stub, stubServerService{}, nil, nil, nil, nil, nil, nil, nil, nil)

	// A body without a source takes the endpoint's ?source= — the channel the
	// plan hint bakes the harness source into.
	body := strings.NewReader(`{"agent_id":"agent-a","goal":"g","actions":[{"tool":"Bash"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/external/plans?source=amp", body)
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if stub.planSubmitReq == nil || stub.planSubmitReq.Source != "amp" {
		t.Fatalf("submit req = %+v, want source from query param", stub.planSubmitReq)
	}

	// An explicit body source wins over the query parameter.
	body = strings.NewReader(`{"agent_id":"agent-a","goal":"g","source":"pi","actions":[{"tool":"Bash"}]}`)
	req = httptest.NewRequest(http.MethodPost, "/api/v1/external/plans?source=amp", body)
	w = httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if stub.planSubmitReq == nil || stub.planSubmitReq.Source != "pi" {
		t.Fatalf("submit req = %+v, want body source to win", stub.planSubmitReq)
	}
}
