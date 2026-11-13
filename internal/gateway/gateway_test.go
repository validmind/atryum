package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/validmind/atryum/internal/auth"
	"github.com/validmind/atryum/internal/invocation"
)

// fakeService records submits and answers with a scripted status.
type fakeService struct {
	mu        sync.Mutex
	status    invocation.Status
	decision  *invocation.Decision
	submitErr error
	panicky   bool
	delay     time.Duration
	pending   []invocation.InvocationResponse
	executing []invocation.InvocationResponse
	submits   []invocation.ExternalSubmitRequest
	recorded  []string // "<id>:<status>"
}

func (f *fakeService) Submit(_ context.Context, req invocation.ExternalSubmitRequest) (invocation.InvocationResponse, error) {
	if f.panicky {
		panic("boom")
	}
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.submits = append(f.submits, req)
	if f.submitErr != nil {
		return invocation.InvocationResponse{}, f.submitErr
	}
	return invocation.InvocationResponse{InvocationID: "inv_1", Status: f.status, Decision: f.decision, Asserted: req.Asserted}, nil
}

func (f *fakeService) List(_ context.Context, filter invocation.InvocationListFilter) (invocation.InvocationListResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch filter.Status {
	case string(invocation.StatusPendingApproval):
		return invocation.InvocationListResponse{Items: f.pending}, nil
	case string(invocation.StatusExecuting):
		return invocation.InvocationListResponse{Items: f.executing}, nil
	}
	return invocation.InvocationListResponse{}, nil
}

func (f *fakeService) RecordExecution(_ context.Context, id string, u invocation.ExternalExecutionUpdate) (invocation.InvocationResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recorded = append(f.recorded, id+":"+u.ExecutionStatus)
	return invocation.InvocationResponse{InvocationID: id}, nil
}

type event struct {
	kind    string
	payload map[string]any
}

func newHandler(svc *fakeService) (*Handler, *[]event) {
	var mu sync.Mutex
	events := &[]event{}
	return New(svc, Options{
		ConsoleLink: func(_ context.Context, inv, rev string) string {
			if rev != "" {
				return "https://aa.example/reviews/" + rev
			}
			return "https://aa.example/record/actions/" + inv
		},
		OnEvent: func(_ context.Context, kind string, p map[string]any) {
			mu.Lock()
			defer mu.Unlock()
			*events = append(*events, event{kind, p})
		},
	}), events
}

var gatewayKey = auth.Identity{AgentID: "gw-local", Method: auth.IdentityMethodAPIKey, KeyID: "k1", Capabilities: []string{auth.CapabilityAsserter}}

func post(t *testing.T, h http.Handler, id *auth.Identity, path, body string) (int, map[string]any) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	if id != nil {
		r = r.WithContext(auth.WithIdentity(r.Context(), *id))
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func TestCheckRequiresAnAsserterKey(t *testing.T) {
	h, events := newHandler(&fakeService{status: invocation.StatusApproved})
	if code, _ := post(t, h, nil, "/api/v1/gateway/checks", `{"tool":"x"}`); code != http.StatusUnauthorized {
		t.Fatalf("no identity: status %d, want 401", code)
	}
	plain := auth.Identity{AgentID: "agent-1", Method: auth.IdentityMethodAPIKey, KeyID: "k2"}
	code, body := post(t, h, &plain, "/api/v1/gateway/checks", `{"tool":"x"}`)
	if code != http.StatusForbidden || body["code"] != "not_an_asserter" {
		t.Fatalf("plain key: %d %v, want 403 not_an_asserter", code, body)
	}
	if len(*events) != 1 || (*events)[0].kind != EventAsserterRejected {
		t.Fatalf("events = %v, want one %s", *events, EventAsserterRejected)
	}
	jwt := auth.Identity{AgentID: "agent-1", Method: auth.IdentityMethodJWT, Capabilities: []string{auth.CapabilityAsserter}}
	if code, _ := post(t, h, &jwt, "/api/v1/gateway/checks", `{"tool":"x"}`); code != http.StatusUnauthorized {
		t.Fatalf("JWT caller: status %d, want 401 (only keys may assert)", code)
	}
}

func TestAllowedCallIsMarkedRunningAndCarriesAssertedIdentity(t *testing.T) {
	svc := &fakeService{status: invocation.StatusApproved, decision: &invocation.Decision{Verdict: invocation.VerdictAllow, Reason: "rule: read-only"}}
	h, _ := newHandler(svc)
	code, body := post(t, h, &gatewayKey, "/api/v1/gateway/checks", `{"gateway_request_id":"r-1","tool":"get_ticket","server":"zendesk","arguments":{"id":7},"user":{"id":"emma","display":"Emma"},"agent":{"id":"support-bot"},"session_id":"s-1"}`)
	if code != 200 || body["verdict"] != VerdictAllow {
		t.Fatalf("got %d %v, want 200 allow", code, body)
	}
	if len(svc.recorded) != 1 || svc.recorded[0] != "inv_1:running" {
		t.Fatalf("recorded = %v, want inv_1:running", svc.recorded)
	}
	req := svc.submits[0]
	a := req.Asserted
	if a == nil || a.UserID != "emma" || a.UserDisplay != "Emma" || a.AgentID != "support-bot" || a.By != "gw-local" || a.SessionID != "s-1" || a.Vendor != "generic" {
		t.Fatalf("asserted = %+v", a)
	}
	if req.Source != "zendesk" || req.ClientSessionID != "s-1" || req.IdempotencyKey == nil || *req.IdempotencyKey != "r-1" || !strings.HasPrefix(req.GatewayCheckKey, "gck_") {
		t.Fatalf("submit = %+v", req)
	}
}

func TestDeniedCallKeepsTheReason(t *testing.T) {
	h, _ := newHandler(&fakeService{status: invocation.StatusDenied, decision: &invocation.Decision{Verdict: invocation.VerdictDeny, Reason: "No dropping databases (firm rule 2)."}})
	_, body := post(t, h, &gatewayKey, "/api/v1/gateway/checks", `{"tool":"drop_database","arguments":{}}`)
	if body["verdict"] != VerdictDeny || body["reason"] != "No dropping databases (firm rule 2)." {
		t.Fatalf("got %v", body)
	}
}

func TestHeldCallBlocksWithReviewLink(t *testing.T) {
	h, _ := newHandler(&fakeService{status: invocation.StatusPendingApproval, decision: &invocation.Decision{Verdict: invocation.VerdictPending, ReviewID: "rev_9"}})
	_, body := post(t, h, &gatewayKey, "/api/v1/gateway/checks", `{"tool":"refund_payment","arguments":{"id":7}}`)
	if body["verdict"] != VerdictBlock || body["code"] != CodeReviewOpened || body["review_url"] != "https://aa.example/reviews/rev_9" {
		t.Fatalf("got %v", body)
	}
	if !strings.Contains(body["reason"].(string), "https://aa.example/reviews/rev_9") {
		t.Fatalf("reason lacks the link: %v", body["reason"])
	}
}

func TestRepeatOfAHeldCallReturnsTheSameReviewWithoutSubmitting(t *testing.T) {
	svc := &fakeService{status: invocation.StatusPendingApproval, pending: []invocation.InvocationResponse{{InvocationID: "inv_open"}}}
	h, _ := newHandler(svc)
	_, body := post(t, h, &gatewayKey, "/api/v1/gateway/checks", `{"tool":"refund_payment","arguments":{"id":7}}`)
	if body["code"] != CodeReviewPending || body["invocation_id"] != "inv_open" || body["review_url"] != "https://aa.example/record/actions/inv_open" {
		t.Fatalf("got %v", body)
	}
	if len(svc.submits) != 0 {
		t.Fatalf("submitted %d times, want 0", len(svc.submits))
	}
}

func TestFailuresFailClosedWithStatus200(t *testing.T) {
	cases := map[string]*fakeService{
		"store error": {submitErr: errors.New("connection refused")},
		"panic":       {panicky: true},
		"too slow":    {status: invocation.StatusApproved, delay: 2 * time.Second},
		"odd state":   {status: invocation.StatusReceived},
	}
	for name, svc := range cases {
		t.Run(name, func(t *testing.T) {
			h, events := newHandler(svc)
			start := time.Now()
			code, body := post(t, h, &gatewayKey, "/api/v1/gateway/checks", `{"tool":"x","arguments":{},"deadline_ms":800}`)
			if code != 200 || body["verdict"] != VerdictDeny || body["code"] != CodeUnavailable {
				t.Fatalf("got %d %v, want 200 deny aa_unavailable", code, body)
			}
			if !strings.HasPrefix(body["reason"].(string), "Agent Authority unavailable") {
				t.Fatalf("reason = %v", body["reason"])
			}
			if took := time.Since(start); took > time.Second {
				t.Fatalf("answered after %v, past the 800ms budget", took)
			}
			if name != "odd state" && (len(*events) == 0 || (*events)[0].kind != EventCheckFailed) {
				t.Fatalf("events = %v, want %s", *events, EventCheckFailed)
			}
		})
	}
}

func TestUnreadableArgumentsAreDenied(t *testing.T) {
	h, _ := newHandler(&fakeService{status: invocation.StatusApproved})
	for _, args := range []string{`[1,2]`, `"not json"`, `42`} {
		_, body := post(t, h, &gatewayKey, "/api/v1/gateway/checks", `{"tool":"x","arguments":`+args+`}`)
		if body["verdict"] != VerdictDeny || body["code"] != CodeUnreadableArguments {
			t.Fatalf("arguments %s: got %v", args, body)
		}
	}
	// A JSON string holding an object is accepted (Kuadrant sends that).
	_, body := post(t, h, &gatewayKey, "/api/v1/gateway/checks", `{"tool":"x","arguments":"{\"id\":7}"}`)
	if body["verdict"] != VerdictAllow {
		t.Fatalf("string-wrapped object: got %v", body)
	}
}

func TestCheckKeyIgnoresKeyOrderAndWhitespaceButNotValuesOrUser(t *testing.T) {
	k := func(user, args string) string {
		key, err := CheckKey("t1", "gw", user, "srv", "refund", json.RawMessage(args))
		if err != nil {
			t.Fatal(err)
		}
		return key
	}
	base := k("emma", `{"id":7,"note":"a"}`)
	if k("emma", `{ "note":"a",  "id":7 }`) != base {
		t.Fatal("key order or whitespace changed the key")
	}
	if k("emma", `"{\"id\":7,\"note\":\"a\"}"`) != base {
		t.Fatal("a string-wrapped object changed the key")
	}
	if k("emma", `{"id":8,"note":"a"}`) == base || k("raj", `{"id":7,"note":"a"}`) == base || k("", `{"id":7,"note":"a"}`) == base {
		t.Fatal("different arguments or user produced the same key")
	}
	if k("emma", `{"id":7.0,"note":"a"}`) == base {
		t.Fatal("7.0 and 7 should stay distinct: numbers keep their digits")
	}
}

func TestOutcomeAttachesToTheCallAndNeverFailsTheGateway(t *testing.T) {
	svc := &fakeService{executing: []invocation.InvocationResponse{{InvocationID: "inv_run"}}}
	h, events := newHandler(svc)
	code, body := post(t, h, &gatewayKey, "/api/v1/gateway/outcomes", `{"tool":"get_ticket","status":"failed","error":{"message":"upstream 500"}}`)
	if code != 200 || body["verdict"] != VerdictAllow {
		t.Fatalf("got %d %v", code, body)
	}
	if len(svc.recorded) != 1 || svc.recorded[0] != "inv_run:failed" {
		t.Fatalf("recorded = %v", svc.recorded)
	}
	svc.executing = nil
	if code, _ := post(t, h, &gatewayKey, "/api/v1/gateway/outcomes", `{"tool":"get_ticket","status":"completed"}`); code != 200 {
		t.Fatalf("unmatched outcome: status %d, want 200", code)
	}
	if len(*events) != 1 || (*events)[0].kind != EventOutcomeUnrecorded {
		t.Fatalf("events = %v, want %s", *events, EventOutcomeUnrecorded)
	}
}

func TestKuadrantKeyInPathBecomesBearerAndLeavesThePath(t *testing.T) {
	var gotAuth, gotPath string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
	})
	r := httptest.NewRequest(http.MethodPost, "/api/v1/gateway/kuadrant/k/atr_secret/v1/checks", nil)
	PathTokenToBearer(next).ServeHTTP(httptest.NewRecorder(), r)
	if gotAuth != "Bearer atr_secret" || gotPath != "/api/v1/gateway/kuadrant/v1/checks" {
		t.Fatalf("auth=%q path=%q", gotAuth, gotPath)
	}
}

func TestKuadrantGuardrailMapsVerdicts(t *testing.T) {
	pre := `{"model":"aa","messages":[{"role":"user","name":"refund_payment","content":"{\"id\":7}","config":"tool"}],"guardrails":{"config_ids":["aa"]}}`
	allowSvc := &fakeService{status: invocation.StatusApproved}
	h, _ := newHandler(allowSvc)
	if _, body := post(t, h, &gatewayKey, "/api/v1/gateway/kuadrant/v1/checks", pre); body["status"] != "passed" {
		t.Fatalf("allow: got %v", body)
	}
	if a := allowSvc.submits[0]; a.Tool != "refund_payment" || a.Input["id"] != json.Number("7") || a.Asserted.UserID != "" || a.Asserted.Vendor != "kuadrant" {
		t.Fatalf("submit = %+v asserted=%+v", a, a.Asserted)
	}
	h, _ = newHandler(&fakeService{status: invocation.StatusPendingApproval, decision: &invocation.Decision{ReviewID: "rev_1"}})
	_, body := post(t, h, &gatewayKey, "/api/v1/gateway/kuadrant/v1/checks", pre)
	if body["status"] != "blocked" || body["rail"] != "agent-authority" || !strings.Contains(body["content"].(string), "rev_1") {
		t.Fatalf("held: got %v", body)
	}
	postCall := `{"model":"aa","messages":[{"role":"assistant","name":"refund_payment","content":"refunded"}]}`
	svc := &fakeService{executing: []invocation.InvocationResponse{{InvocationID: "inv_run"}}}
	h, _ = newHandler(svc)
	if _, body := post(t, h, &gatewayKey, "/api/v1/gateway/kuadrant/v1/checks", postCall); body["status"] != "passed" || len(svc.recorded) != 1 {
		t.Fatalf("post: got %v recorded=%v", body, svc.recorded)
	}
}

func TestTrueFoundryMapsUserAndVerdict(t *testing.T) {
	svc := &fakeService{status: invocation.StatusDenied, decision: &invocation.Decision{Reason: "User mcp may not delete tickets"}}
	h, _ := newHandler(svc)
	body := `{"requestBody":{"tool_name":"delete_ticket","arguments":{"id":3},"mcp_server_name":"zendesk"},"config":{},"context":{"user":{"subjectId":"u-42","subjectType":"user","subjectSlug":"mcp"},"metadata":{"session_id":"s-9"}}}`
	_, out := post(t, h, &gatewayKey, "/api/v1/gateway/truefoundry/pre", body)
	if out["verdict"] != false || out["message"] != "User mcp may not delete tickets" {
		t.Fatalf("got %v", out)
	}
	req := svc.submits[0]
	if req.Tool != "delete_ticket" || req.Source != "zendesk" || req.Asserted.UserID != "u-42" || req.Asserted.UserDisplay != "mcp" || req.ClientSessionID != "s-9" || req.Asserted.Vendor != "truefoundry" {
		t.Fatalf("submit = %+v asserted=%+v", req, req.Asserted)
	}
	// Capture mode records and blocks without deciding.
	_, out = post(t, h, &gatewayKey, "/api/v1/gateway/truefoundry/pre?capture=1", body)
	if out["verdict"] != false || len(svc.submits) != 1 {
		t.Fatalf("capture: got %v, submits=%d", out, len(svc.submits))
	}
}

// The shape the real TrueFoundry gateway sent on 2026-11-13: requestBody is
// only the tool arguments, the tool and server come from the guardrail's own
// config, and config.headers echoes the integration's secret.
func TestTrueFoundryRealPayloadShape(t *testing.T) {
	svc := &fakeService{status: invocation.StatusExecuting}
	h, _ := newHandler(svc)
	body := `{"requestBody":{"name":"not-a-tool","repoName":"acme/widgets"},` +
		`"config":{"url":"https://aa.example/api/v1/gateway/truefoundry/pre","headers":{"Authorization":"Bearer atr_secret"},"tool":"read_wiki_structure","server":"deepwiki"},` +
		`"context":{"user":{"subjectId":"u-1","subjectSlug":"pat@example.com","subjectType":"user","tenantName":"t","roles":["member"]},` +
		`"metadata":{"tfy_gateway_region":"CA"},"claims":{"sub":"sub-1","email":"pat@example.com"}}}`
	if _, out := post(t, h, &gatewayKey, "/api/v1/gateway/truefoundry/pre", body); out["verdict"] != true {
		t.Fatalf("got %v", out)
	}
	req := svc.submits[0]
	if req.Tool != "read_wiki_structure" || req.Source != "deepwiki" || req.Asserted.UserID != "u-1" || req.Asserted.UserDisplay != "pat@example.com" {
		t.Fatalf("submit = %+v asserted=%+v", req, req.Asserted)
	}
	if req.Input["repoName"] != "acme/widgets" || req.Input["name"] != "not-a-tool" {
		t.Fatalf("input = %v, want the whole requestBody", req.Input)
	}

	// Without config the call is unreadable, never guessed from an argument.
	svc = &fakeService{status: invocation.StatusExecuting}
	h, _ = newHandler(svc)
	noTool := `{"requestBody":{"name":"not-a-tool"},"config":{},"context":{"claims":{"sub":"sub-1","email":"pat@example.com"}}}`
	if _, out := post(t, h, &gatewayKey, "/api/v1/gateway/truefoundry/pre", noTool); out["verdict"] != false || len(svc.submits) != 0 {
		t.Fatalf("no tool: got %v submits=%d", out, len(svc.submits))
	}

	if got := redactConfigHeaders([]byte(body)); strings.Contains(got, "atr_secret") {
		t.Fatalf("capture leaked the echoed key: %s", got)
	}

	// Post hook: requestBody is the tool result, so the outcome is matched to
	// the newest running call of the tool, not keyed on the result as if it
	// were arguments.
	svc = &fakeService{executing: []invocation.InvocationResponse{{InvocationID: "inv_run"}}}
	h, _ = newHandler(svc)
	result := `{"content":[{"type":"text","text":"pages"}],"isError":false}`
	postBody := `{"requestBody":` + result + `,"responseBody":` + result + `,"config":{"tool":"read_wiki_structure","server":"deepwiki"},"context":{"user":{"subjectId":"u-1"}}}`
	if _, out := post(t, h, &gatewayKey, "/api/v1/gateway/truefoundry/post", postBody); out["verdict"] != true || len(svc.recorded) != 1 || svc.recorded[0] != "inv_run:completed" {
		t.Fatalf("post: got %v recorded=%v", out, svc.recorded)
	}
}
