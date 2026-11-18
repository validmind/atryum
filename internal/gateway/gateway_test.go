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

// oneCall is a model response proposing one call to refund_payment.
func oneCall(extra string) string {
	return `{"response":{"type":"message","role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"refund_payment","input":{"id":7}}],"stop_reason":"tool_use"}` + extra + `}`
}

// firstCall is the verdict for the first call in an llm/output answer.
func firstCall(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	calls, _ := body["calls"].([]any)
	if len(calls) == 0 {
		t.Fatalf("no calls in %v", body)
	}
	return calls[0].(map[string]any)
}

func TestRoutesRequireAnAsserterKey(t *testing.T) {
	h, events := newHandler(&fakeService{status: invocation.StatusApproved})
	if code, _ := post(t, h, nil, "/api/v1/gateway/llm/output", oneCall("")); code != http.StatusUnauthorized {
		t.Fatalf("no identity: status %d, want 401", code)
	}
	plain := auth.Identity{AgentID: "agent-1", Method: auth.IdentityMethodAPIKey, KeyID: "k2"}
	code, body := post(t, h, &plain, "/api/v1/gateway/llm/output", oneCall(""))
	if code != http.StatusForbidden || body["code"] != "not_an_asserter" {
		t.Fatalf("plain key: %d %v, want 403 not_an_asserter", code, body)
	}
	if len(*events) != 1 || (*events)[0].kind != EventAsserterRejected {
		t.Fatalf("events = %v, want one %s", *events, EventAsserterRejected)
	}
	jwt := auth.Identity{AgentID: "agent-1", Method: auth.IdentityMethodJWT, Capabilities: []string{auth.CapabilityAsserter}}
	if code, _ := post(t, h, &jwt, "/api/v1/gateway/llm/output", oneCall("")); code != http.StatusUnauthorized {
		t.Fatalf("JWT caller: status %d, want 401 (only keys may assert)", code)
	}
}

func TestOnlyTheAIGatewayRoutesAreServed(t *testing.T) {
	h, _ := newHandler(&fakeService{status: invocation.StatusApproved})
	for _, path := range []string{"/api/v1/gateway/checks", "/api/v1/gateway/outcomes", "/api/v1/gateway/truefoundry/pre", "/api/v1/gateway/nope"} {
		if code, _ := post(t, h, &gatewayKey, path, `{}`); code != http.StatusNotFound {
			t.Fatalf("%s: status %d, want 404", path, code)
		}
	}
}

func TestAllowedCallIsMarkedRunningAndCarriesAssertedIdentity(t *testing.T) {
	svc := &fakeService{status: invocation.StatusApproved, decision: &invocation.Decision{Verdict: invocation.VerdictAllow, Reason: "rule: read-only"}}
	h, _ := newHandler(svc)
	code, body := post(t, h, &gatewayKey, "/api/v1/gateway/llm/output", oneCall(`,"user":{"id":"emma","display":"Emma"},"agent":{"id":"support-bot"},"session_id":"s-1"`))
	if code != 200 || firstCall(t, body)["verdict"] != VerdictAllow {
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
	if req.ClientSessionID != "s-1:emma" || req.ClientName != "generic-llm-gateway" || req.IdempotencyKey == nil || *req.IdempotencyKey != callKey("emma", "s-1", "toolu_1") || req.RequestID == nil || *req.RequestID != "toolu_1" || !strings.HasPrefix(req.GatewayCheckKey, "gck_") {
		t.Fatalf("submit = %+v", req)
	}
}

func TestRepeatOfAHeldCallReturnsTheSameReviewWithoutSubmitting(t *testing.T) {
	svc := &fakeService{status: invocation.StatusPendingApproval, pending: []invocation.InvocationResponse{{InvocationID: "inv_open"}}}
	h, _ := newHandler(svc)
	_, body := post(t, h, &gatewayKey, "/api/v1/gateway/llm/output", oneCall(""))
	c := firstCall(t, body)
	if c["code"] != CodeReviewPending || c["invocation_id"] != "inv_open" || c["review_url"] != "https://aa.example/record/actions/inv_open" {
		t.Fatalf("got %v", c)
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
			code, body := post(t, h, &gatewayKey, "/api/v1/gateway/llm/output", oneCall(`,"deadline_ms":800`))
			c := firstCall(t, body)
			if code != 200 || c["verdict"] != VerdictDeny || c["code"] != CodeUnavailable {
				t.Fatalf("got %d %v, want 200 deny aa_unavailable", code, c)
			}
			if !strings.HasPrefix(c["reason"].(string), "Agent Authority unavailable") {
				t.Fatalf("reason = %v", c["reason"])
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

func TestCaptureNeverLogsTheEchoedKey(t *testing.T) {
	body := `{"requestBody":{},"config":{"url":"https://aa.example/api/v1/gateway/truefoundry/llm/input","headers":{"Authorization":"Bearer atr_secret"}}}`
	if got := redactConfigHeaders([]byte(body)); strings.Contains(got, "atr_secret") || !strings.Contains(got, "[redacted]") {
		t.Fatalf("capture leaked the echoed key: %s", got)
	}
}
