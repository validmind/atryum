package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/validmind/atryum/internal/auth"
	"github.com/validmind/atryum/internal/invocation"
)

// llmService decides by tool name and keeps what it was told, like the real
// service: an invocation per submit, found again by request ID or check key.
type llmService struct {
	mu       sync.Mutex
	byTool   map[string]invocation.Status // default: approved
	reasons  map[string]string
	invs     []invocation.InvocationResponse
	keys     map[string]string // invocation ID -> check key
	clients  map[string]string // invocation ID -> client name
	submits  []invocation.ExternalSubmitRequest
	conflict bool // the first submit with an idempotency key conflicts
}

func (s *llmService) Submit(_ context.Context, req invocation.ExternalSubmitRequest) (invocation.InvocationResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.submits = append(s.submits, req)
	if s.conflict && req.IdempotencyKey != nil {
		return invocation.InvocationResponse{}, invocation.ErrIdempotencyConflict
	}
	status, ok := s.byTool[req.Tool]
	if !ok {
		status = invocation.StatusApproved
	}
	inv := invocation.InvocationResponse{InvocationID: fmt.Sprintf("inv_%d", len(s.invs)+1), ToolName: req.Tool, Status: status, RequestID: req.RequestID, Asserted: req.Asserted}
	if reason := s.reasons[req.Tool]; reason != "" {
		inv.Decision = &invocation.Decision{Reason: reason, ReviewID: "rev_" + req.Tool}
	}
	s.invs = append(s.invs, inv)
	if s.keys == nil {
		s.keys, s.clients = map[string]string{}, map[string]string{}
	}
	s.keys[inv.InvocationID], s.clients[inv.InvocationID] = req.GatewayCheckKey, req.ClientName
	return inv, nil
}

func (s *llmService) List(_ context.Context, f invocation.InvocationListFilter) (invocation.InvocationListResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []invocation.InvocationResponse
	for i := len(s.invs) - 1; i >= 0; i-- { // newest first
		inv := s.invs[i]
		switch {
		case f.Status != "" && string(inv.Status) != f.Status,
			f.RequestID != "" && (inv.RequestID == nil || *inv.RequestID != f.RequestID),
			f.GatewayCheckKey != "" && s.keys[inv.InvocationID] != f.GatewayCheckKey,
			f.ClientName != "" && s.clients[inv.InvocationID] != f.ClientName:
			continue
		}
		out = append(out, inv)
	}
	return invocation.InvocationListResponse{Items: out}, nil
}

func (s *llmService) RecordExecution(_ context.Context, id string, u invocation.ExternalExecutionUpdate) (invocation.InvocationResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.invs {
		if s.invs[i].InvocationID != id {
			continue
		}
		switch u.ExecutionStatus {
		case "running":
			s.invs[i].Status = invocation.StatusExecuting
		case "completed":
			s.invs[i].Status = invocation.StatusSucceeded
		case "failed":
			s.invs[i].Status = invocation.StatusFailed
		}
		return s.invs[i], nil
	}
	return invocation.InvocationResponse{}, invocation.ErrInvalidTransition
}

func (s *llmService) status(id string) invocation.Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, inv := range s.invs {
		if inv.InvocationID == id {
			return inv.Status
		}
	}
	return ""
}

func newLLMHandler(svc Service) (*Handler, *[]event) {
	var mu sync.Mutex
	events := &[]event{}
	return New(svc, Options{
		ConsoleLink: func(_ context.Context, inv, rev string) string { return "https://aa.example/reviews/" + rev },
		OnEvent: func(_ context.Context, kind string, p map[string]any) {
			mu.Lock()
			defer mu.Unlock()
			*events = append(*events, event{kind, p})
		},
	}), events
}

// postLLM posts a JSON value and decodes the answer into out.
func postLLM(t *testing.T, h http.Handler, path string, in, out any) {
	t.Helper()
	body, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(body)))
	r = r.WithContext(auth.WithIdentity(r.Context(), gatewayKey))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("%s: status %d, want 200: %s", path, w.Code, w.Body)
	}
	if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
		t.Fatalf("%s: %v in %s", path, err, w.Body)
	}
}

func raw(s string) json.RawMessage { return json.RawMessage(s) }

const anthropicTwoCalls = `{"id":"msg_1","type":"message","role":"assistant","model":"claude","content":[
	{"type":"text","text":"I'll look, then refund."},
	{"type":"tool_use","id":"toolu_A","name":"mcp__zendesk__get_ticket","input":{"id":7}},
	{"type":"tool_use","id":"toolu_B","name":"refund_payment","input":{"amount":900}}],
	"stop_reason":"tool_use","usage":{"input_tokens":10,"output_tokens":20}}`

func TestOutputWithNoCallsIsReturnedByteForByte(t *testing.T) {
	svc := &llmService{}
	h, _ := newLLMHandler(svc)
	body := `{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"Hello  <b>"}],"stop_reason":"end_turn"}`
	var out LLMOutputResult
	postLLM(t, h, "/api/v1/gateway/llm/output", LLMOutput{Response: raw(body)}, &out)
	if out.Verdict != VerdictAllow || out.Changed || len(out.Calls) != 0 || len(svc.submits) != 0 {
		t.Fatalf("got %+v, submits=%d", out, len(svc.submits))
	}
	var sent, back any
	_ = json.Unmarshal([]byte(body), &sent)
	_ = json.Unmarshal(out.Response, &back)
	if fmt.Sprint(sent) != fmt.Sprint(back) {
		t.Fatalf("response changed: %s", out.Response)
	}
}

func TestOutputDecidesEachCallAndWithholdsTheDeniedOne(t *testing.T) {
	svc := &llmService{byTool: map[string]invocation.Status{"refund_payment": invocation.StatusDenied}, reasons: map[string]string{"refund_payment": "No refunds over $500 (firm rule F-07)."}}
	h, _ := newLLMHandler(svc)
	var out LLMOutputResult
	postLLM(t, h, "/api/v1/gateway/llm/output", LLMOutput{
		Response: raw(anthropicTwoCalls), Request: raw(`{"messages":[{"role":"user","content":"Refund ticket 7 in full"}]}`),
		User: &Principal{ID: "emma", Display: "Emma"}, SessionID: "s-1",
	}, &out)
	if out.Verdict != VerdictDeny || !out.Changed || out.Code != CodeAdjusted || len(out.Calls) != 2 {
		t.Fatalf("got %+v", out)
	}
	if c := out.Calls[0]; c.CallID != "toolu_A" || c.Verdict != VerdictAllow || c.Server != "zendesk" || c.Tool != "get_ticket" {
		t.Fatalf("first call = %+v", c)
	}
	if c := out.Calls[1]; c.CallID != "toolu_B" || c.Verdict != VerdictDeny || c.Server != "" {
		t.Fatalf("second call = %+v", c)
	}
	var resp struct {
		Content []struct {
			Type, ID, Text string
		}
		StopReason string `json:"stop_reason"`
		Usage      map[string]int
	}
	if err := json.Unmarshal(out.Response, &resp); err != nil {
		t.Fatal(err)
	}
	// The allowed call is untouched, the denied one is now a sentence in its
	// place, and the turn still stops for the tool that may run.
	if len(resp.Content) != 3 || resp.Content[1].ID != "toolu_A" || resp.Content[2].Type != "text" || resp.StopReason != "tool_use" || resp.Usage["output_tokens"] != 20 {
		t.Fatalf("adjusted = %s", out.Response)
	}
	if note := resp.Content[2].Text; !strings.Contains(note, "refund_payment") || !strings.Contains(note, "No refunds over $500 (firm rule F-07).") || !strings.Contains(note, "did not run") {
		t.Fatalf("notice = %q", note)
	}
	// Each call was submitted under the model's call ID, as the LLM path,
	// with the asserted person and what they asked.
	byTool := map[string]invocation.ExternalSubmitRequest{}
	for _, s := range svc.submits {
		byTool[s.Tool] = s
	}
	got := byTool["get_ticket"]
	if got.RequestID == nil || *got.RequestID != "toolu_A" || got.Source != "zendesk" || got.ClientName != "generic-llm-gateway" || got.Asserted.UserID != "emma" || got.ClientSessionID != "s-1 · emma" || !strings.Contains(got.SessionContext, "Refund ticket 7 in full") {
		t.Fatalf("submit = %+v", got)
	}
	if svc.status("inv_1") == invocation.StatusApproved && svc.status("inv_2") == invocation.StatusApproved {
		t.Fatal("the allowed call was not marked running")
	}
}

func TestOutputWithEveryCallWithheldEndsTheTurn(t *testing.T) {
	svc := &llmService{byTool: map[string]invocation.Status{"refund_payment": invocation.StatusPendingApproval}, reasons: map[string]string{"refund_payment": "held"}}
	h, _ := newLLMHandler(svc)
	body := `{"type":"message","role":"assistant","content":[{"type":"tool_use","id":"toolu_B","name":"refund_payment","input":{"amount":900}}],"stop_reason":"tool_use"}`
	var out LLMOutputResult
	postLLM(t, h, "/api/v1/gateway/llm/output", LLMOutput{Format: "anthropic", Response: raw(body)}, &out)
	if out.Verdict != VerdictBlock || out.Calls[0].ReviewURL != "https://aa.example/reviews/rev_refund_payment" {
		t.Fatalf("got %+v", out)
	}
	s := string(out.Response)
	if strings.Contains(s, `"tool_use"`) || !strings.Contains(s, `"stop_reason":"end_turn"`) || !strings.Contains(s, "https://aa.example/reviews/rev_refund_payment") || !strings.Contains(s, "retry the same call") {
		t.Fatalf("adjusted = %s", s)
	}
}

func TestOutputOpenAIChat(t *testing.T) {
	svc := &llmService{byTool: map[string]invocation.Status{"drop_table": invocation.StatusDenied}, reasons: map[string]string{"drop_table": "No destructive SQL."}}
	h, _ := newLLMHandler(svc)
	body := `{"id":"chatcmpl-1","object":"chat.completion","choices":[{"index":0,"finish_reason":"tool_calls","message":{"role":"assistant","content":null,"tool_calls":[
		{"id":"call_1","type":"function","function":{"name":"run_query","arguments":"{\"sql\":\"select 1\"}"}},
		{"id":"call_2","type":"function","function":{"name":"drop_table","arguments":"{\"table\":\"users\"}"}}]}}]}`
	var out LLMOutputResult
	postLLM(t, h, "/api/v1/gateway/llm/output", LLMOutput{Response: raw(body)}, &out)
	var resp struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content   string
				ToolCalls []struct{ ID string } `json:"tool_calls"`
			}
		}
	}
	_ = json.Unmarshal(out.Response, &resp)
	c := resp.Choices[0]
	if len(c.Message.ToolCalls) != 1 || c.Message.ToolCalls[0].ID != "call_1" || c.FinishReason != "tool_calls" || !strings.Contains(c.Message.Content, "No destructive SQL.") {
		t.Fatalf("adjusted = %s", out.Response)
	}
	if svc.submits[0].Input["sql"] == nil && svc.submits[1].Input["sql"] == nil {
		t.Fatalf("arguments were not parsed from JSON text: %+v", svc.submits)
	}

	// Every call withheld: no tool_calls key, and the turn stops.
	svc.byTool["run_query"] = invocation.StatusDenied
	postLLM(t, h, "/api/v1/gateway/llm/output", LLMOutput{Response: raw(strings.ReplaceAll(body, "call_", "callx_"))}, &out)
	if s := string(out.Response); strings.Contains(s, "tool_calls\":[") || !strings.Contains(s, `"finish_reason":"stop"`) {
		t.Fatalf("adjusted = %s", s)
	}

	// Arguments that are not a JSON object are never guessed at.
	bad := `{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"call_9","type":"function","function":{"name":"run_query","arguments":"select 1"}}]}}]}`
	before := len(svc.submits)
	postLLM(t, h, "/api/v1/gateway/llm/output", LLMOutput{Response: raw(bad)}, &out)
	if out.Calls[0].Code != CodeUnreadableArguments || len(svc.submits) != before || strings.Contains(string(out.Response), "call_9") {
		t.Fatalf("got %+v", out)
	}
}

func TestOutputOpenAIResponses(t *testing.T) {
	svc := &llmService{byTool: map[string]invocation.Status{"shell": invocation.StatusDenied}, reasons: map[string]string{"shell": "No shell."}}
	h, events := newLLMHandler(svc)
	body := `{"id":"resp_1","object":"response","status":"completed","output":[
		{"type":"reasoning","id":"rs_1","summary":[]},
		{"type":"web_search_call","id":"ws_1","status":"completed"},
		{"type":"function_call","id":"fc_1","call_id":"call_a","name":"get_weather","arguments":"{\"city\":\"Oslo\"}"},
		{"type":"shell_call","id":"sh_1","call_id":"call_b","action":{"commands":["rm -rf /"]}}]}`
	var out LLMOutputResult
	postLLM(t, h, "/api/v1/gateway/llm/output", LLMOutput{Response: raw(body)}, &out)
	if len(out.Calls) != 2 || out.Calls[0].Verdict != VerdictAllow || out.Calls[1].Tool != "shell" || out.Calls[1].Verdict != VerdictDeny {
		t.Fatalf("calls = %+v", out.Calls)
	}
	var resp struct {
		Output []struct {
			Type, CallID string
			Content      []struct{ Type, Text string }
		}
	}
	_ = json.Unmarshal(out.Response, &resp)
	last := resp.Output[3]
	if len(resp.Output) != 4 || resp.Output[2].Type != "function_call" || last.Type != "message" || last.Content[0].Type != "output_text" || !strings.Contains(last.Content[0].Text, "No shell.") {
		t.Fatalf("adjusted = %s", out.Response)
	}
	// The provider's own web search is recorded, not decided.
	if len(*events) != 1 || (*events)[0].kind != EventProviderToolRan || (*events)[0].payload["tool"] != "web_search" {
		t.Fatalf("events = %+v", *events)
	}
}

func TestOutputFailsClosed(t *testing.T) {
	h, _ := newLLMHandler(&llmService{})
	for name, in := range map[string]LLMOutput{
		"not an object":  {Response: raw(`[1,2]`)},
		"unknown shape":  {Response: raw(`{"text":"hi"}`)},
		"unknown format": {Format: "gemini", Response: raw(anthropicTwoCalls)},
	} {
		var out LLMOutputResult
		postLLM(t, h, "/api/v1/gateway/llm/output", in, &out)
		if out.Verdict != VerdictDeny || out.Code != CodeUnreadableResponse || out.Response != nil {
			t.Fatalf("%s: got %+v", name, out)
		}
	}
	// Agent Authority down: the call is taken out, not let through.
	failing := &fakeService{submitErr: fmt.Errorf("connection refused")}
	h, _ = newLLMHandler(failing)
	var out LLMOutputResult
	postLLM(t, h, "/api/v1/gateway/llm/output", LLMOutput{Response: raw(anthropicTwoCalls)}, &out)
	if out.Verdict != VerdictDeny || !out.Changed || strings.Contains(string(out.Response), `"tool_use"`) || !strings.Contains(string(out.Response), "Agent Authority unavailable") {
		t.Fatalf("got %+v: %s", out, out.Response)
	}
}

func TestReusedCallIDIsANewCall(t *testing.T) {
	svc := &llmService{conflict: true}
	h, _ := newLLMHandler(svc)
	body := `{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"call_0","type":"function","function":{"name":"ls","arguments":"{}"}}]}}]}`
	var out LLMOutputResult
	postLLM(t, h, "/api/v1/gateway/llm/output", LLMOutput{Response: raw(body)}, &out)
	if out.Verdict != VerdictAllow || len(svc.submits) != 2 || svc.submits[1].IdempotencyKey != nil || svc.submits[1].RequestID == nil {
		t.Fatalf("got %+v, submits=%+v", out, svc.submits)
	}
}

func TestInputAttachesToolResultsByCallID(t *testing.T) {
	svc := &llmService{byTool: map[string]invocation.Status{"refund_payment": invocation.StatusDenied}}
	h, events := newLLMHandler(svc)
	var checked LLMOutputResult
	postLLM(t, h, "/api/v1/gateway/llm/output", LLMOutput{Response: raw(anthropicTwoCalls)}, &checked)

	req := `{"model":"claude","max_tokens":100,"messages":[
		{"role":"user","content":"Refund ticket 7"},
		{"role":"assistant","content":[{"type":"tool_use","id":"toolu_OLD","name":"x","input":{}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_OLD","content":"old"}]},
		{"role":"assistant","content":[{"type":"tool_use","id":"toolu_A","name":"mcp__zendesk__get_ticket","input":{"id":7}}]},
		{"role":"user","content":[
			{"type":"tool_result","tool_use_id":"toolu_A","content":[{"type":"text","text":"ticket 7"}]},
			{"type":"tool_result","tool_use_id":"toolu_B","content":"refunded","is_error":false},
			{"type":"tool_result","tool_use_id":"toolu_GHOST","content":"?","is_error":true},
			{"type":"text","text":"continue"}]}]}`
	var out LLMInputResult
	postLLM(t, h, "/api/v1/gateway/llm/input", LLMInput{Request: raw(req)}, &out)
	if out.Verdict != VerdictAllow || out.Changed || len(out.Outcomes) != 3 {
		t.Fatalf("got %+v", out)
	}
	// Only the results after the model's last turn: toolu_OLD was sent before.
	want := map[string]string{"toolu_A": "completed", "toolu_B": "withheld", "toolu_GHOST": "unchecked"}
	for _, o := range out.Outcomes {
		if want[o.CallID] != o.Status {
			t.Fatalf("outcome %s = %s, want %s", o.CallID, o.Status, want[o.CallID])
		}
	}
	if svc.status(out.Outcomes[0].InvocationID) != invocation.StatusSucceeded {
		t.Fatalf("the call was not completed: %v", svc.invs)
	}
	kinds := map[string]int{}
	for _, e := range *events {
		kinds[e.kind]++
	}
	if kinds[EventWithheldCallRan] != 1 || kinds[EventUncheckedToolResult] != 1 {
		t.Fatalf("events = %v", kinds)
	}
	// The agent retries the request: nothing is recorded or reported twice.
	postLLM(t, h, "/api/v1/gateway/llm/input", LLMInput{Request: raw(req)}, &out)
	if out.Outcomes[0].Status != "completed" || len(*events) != 2 {
		t.Fatalf("retry: %+v, events=%d", out.Outcomes, len(*events))
	}

	// Chat Completions and Responses carry results differently.
	chat := `{"messages":[{"role":"assistant","tool_calls":[{"id":"c1"}]},{"role":"tool","tool_call_id":"c1","content":"ok"},{"role":"user","content":"and now?"}]}`
	if got := toolOutcomes(FormatOpenAIChat, mustObject(t, chat)); len(got) != 1 || got[0].CallID != "c1" {
		t.Fatalf("chat outcomes = %+v", got)
	}
	responses := `{"input":[{"type":"function_call","call_id":"c0","name":"x","arguments":"{}"},{"type":"function_call_output","call_id":"c0","output":"ok"},{"type":"shell_call_output","call_id":"c2","status":"failed","output":"boom"}]}`
	if got := toolOutcomes(FormatOpenAIResponses, mustObject(t, responses)); len(got) != 2 || got[1].CallID != "c2" || !got[1].Failed {
		t.Fatalf("responses outcomes = %+v", got)
	}
}

func TestInputTakesOutProviderToolsThatAreNotAllowed(t *testing.T) {
	svc := &llmService{byTool: map[string]invocation.Status{"web_search": invocation.StatusDenied, ProviderMCPTool: invocation.StatusDenied}}
	h, _ := newLLMHandler(svc)
	req := `{"model":"claude","max_tokens":10,"system":"be brief","tool_choice":{"type":"auto"},
		"mcp_servers":[{"type":"url","name":"crm","url":"https://crm.example/mcp"}],
		"tools":[
			{"type":"web_search_20250305","name":"web_search"},
			{"type":"code_execution_20250522","name":"code_execution"},
			{"type":"mcp_toolset","mcp_server_name":"crm"},
			{"type":"bash_20250124","name":"bash"},
			{"name":"get_ticket","input_schema":{"type":"object"}}],
		"messages":[{"role":"user","content":"hi"}]}`
	var out LLMInputResult
	postLLM(t, h, "/api/v1/gateway/llm/input", LLMInput{Request: raw(req), User: &Principal{ID: "emma"}, SessionID: "s-1"}, &out)
	if !out.Changed || out.Verdict != VerdictAllow || len(out.Tools) != 3 {
		t.Fatalf("got %+v", out)
	}
	s := string(out.Request)
	for _, gone := range []string{"web_search", "mcp_servers", "mcp_toolset"} {
		if strings.Contains(s, gone) {
			t.Fatalf("%s is still in the request: %s", gone, s)
		}
	}
	for _, kept := range []string{"code_execution_20250522", "bash_20250124", "get_ticket", "tool_choice"} {
		if !strings.Contains(s, kept) {
			t.Fatalf("%s was taken out: %s", kept, s)
		}
	}
	// The allowed tool is closed at once and not asked about again this session.
	before := len(svc.submits)
	postLLM(t, h, "/api/v1/gateway/llm/input", LLMInput{Request: raw(req), User: &Principal{ID: "emma"}, SessionID: "s-1"}, &out)
	if asked := len(svc.submits) - before; asked != 2 {
		t.Fatalf("asked about %d tools on the second turn, want 2 (the denied ones)", asked)
	}
	for _, inv := range svc.invs {
		if inv.ToolName == "code_execution" && inv.Status != invocation.StatusSucceeded {
			t.Fatalf("the allowed provider tool was left %s", inv.Status)
		}
	}

	// Responses: the agent's own tools stay, the provider's go.
	svc.byTool["file_search"] = invocation.StatusDenied
	resp := `{"model":"gpt","input":"hi","tools":[{"type":"function","name":"f"},{"type":"file_search"},{"type":"shell"}]}`
	postLLM(t, h, "/api/v1/gateway/llm/input", LLMInput{Request: raw(resp)}, &out)
	if s := string(out.Request); strings.Contains(s, "file_search") || !strings.Contains(s, `"shell"`) || !strings.Contains(s, `"function"`) {
		t.Fatalf("responses request = %s", s)
	}

	// With every tool gone, so is the tool choice.
	only := `{"system":"s","tool_choice":{"type":"any"},"tools":[{"type":"web_search_20250305","name":"web_search"}],"messages":[{"role":"user","content":"hi"}]}`
	postLLM(t, h, "/api/v1/gateway/llm/input", LLMInput{Request: raw(only)}, &out)
	if s := string(out.Request); strings.Contains(s, "tools") || strings.Contains(s, "tool_choice") {
		t.Fatalf("request = %s", s)
	}
}

// The same call crossing both hooks of one gateway is one call.
func TestTrueFoundryLLMHooks(t *testing.T) {
	svc := &llmService{byTool: map[string]invocation.Status{"drop_table": invocation.StatusDenied}, reasons: map[string]string{"drop_table": "No destructive SQL."}}
	h, _ := newLLMHandler(svc)
	ctx := `"context":{"user":{"subjectId":"u-1","subjectType":"user","subjectSlug":"pat@example.com"},"metadata":{"session_id":"s-9"}}`
	response := `{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_2","type":"function","function":{"name":"drop_table","arguments":"{\"table\":\"users\"}"}}]}}]}`
	request := `{"model":"gpt","messages":[{"role":"user","content":"clean up"}],"tools":[{"type":"function","function":{"name":"drop_table"}}]}`

	var out map[string]any
	tfy := func(hook, body string) {
		t.Helper()
		out = nil // a fresh map: Unmarshal keeps the keys of an old one
		postLLM(t, h, "/api/v1/gateway/truefoundry/llm/"+hook, raw(body), &out)
	}
	tfy("output", `{"requestBody":`+request+`,"responseBody":`+response+`,"config":{"operation":"mutate"},`+ctx+`}`)
	result, _ := json.Marshal(out["result"])
	if out["verdict"] != true || out["transformed"] != true || strings.Contains(string(result), "call_2") || !strings.Contains(string(result), "No destructive SQL.") {
		t.Fatalf("mutate: got %v", out)
	}
	if a := svc.submits[0]; a.Asserted.UserID != "u-1" || a.Asserted.Vendor != "truefoundry" || a.ClientSessionID != "s-9 · u-1" || a.ClientName != "truefoundry-llm-gateway" {
		t.Fatalf("submit = %+v asserted=%+v", a, a.Asserted)
	}
	// A caller using a virtual account's token arrives as that account.
	va := `"context":{"user":{"subjectId":"qr6q-va","subjectSlug":"support-agent","subjectType":"virtualaccount","tenantName":"acme-dev","roles":[]},"metadata":{"session_id":"s-va"}}`
	tfy("output", `{"requestBody":`+request+`,"responseBody":`+strings.ReplaceAll(response, "call_2", "call_va")+`,"config":{},`+va+`}`)
	if a := svc.submits[len(svc.submits)-1].Asserted; a.UserID != "qr6q-va" || a.UserType != "virtualaccount" || a.UserDisplay != "support-agent" {
		t.Fatalf("virtual account: asserted=%+v", a)
	}
	// Without "operation": "mutate" in its config (TrueFoundry does not send
	// the guardrail's operation), a response with a withheld call is refused
	// whole: a Validate guardrail might pass the original.
	tfy("output", `{"requestBody":`+request+`,"responseBody":`+strings.ReplaceAll(response, "call_2", "call_4")+`,"config":{},`+ctx+`}`)
	if out["verdict"] != false || out["transformed"] != nil || !strings.Contains(out["message"].(string), "No destructive SQL.") {
		t.Fatalf("no operation: got %v", out)
	}
	// A guardrail that can only validate refuses the whole response.
	tfy("output", `{"requestBody":`+request+`,"responseBody":`+strings.ReplaceAll(response, "call_2", "call_3")+`,"config":{"operation":"validate"},`+ctx+`}`)
	if out["verdict"] != false || out["transformed"] != nil || !strings.Contains(out["message"].(string), "No destructive SQL.") {
		t.Fatalf("validate: got %v", out)
	}
	// An allowed response passes untouched.
	ok := `{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"done"}}]}`
	tfy("output", `{"requestBody":`+request+`,"responseBody":`+ok+`,"config":{},`+ctx+`}`)
	if out["verdict"] != true || out["transformed"] != nil {
		t.Fatalf("clean: got %v", out)
	}

	// Input: TrueFoundry skips output guardrails on streams, so a streamed
	// request with tools is refused unless the guardrail opts out.
	streamed := strings.Replace(request, `{"model"`, `{"stream":true,"model"`, 1)
	tfy("input", `{"requestBody":`+streamed+`,"config":{},`+ctx+`}`)
	if out["verdict"] != false || !strings.Contains(out["message"].(string), "stream") {
		t.Fatalf("streamed: got %v", out)
	}
	tfy("input", `{"requestBody":`+streamed+`,"config":{"on_stream":"allow"},`+ctx+`}`)
	if out["verdict"] != true {
		t.Fatalf("streamed, allowed: got %v", out)
	}
	tfy("input", `{"requestBody":`+request+`,"config":{},`+ctx+`}`)
	if out["verdict"] != true {
		t.Fatalf("plain: got %v", out)
	}
}

// The bodies TrueFoundry's gateway really sent on 2026-11-17, with the names
// changed: the person arrives as an opaque subject ID with their email as
// the slug, a session ID only if the caller put one in X-TFY-METADATA, the
// guardrail's own headers echoed in config, and the model's tool calls in
// responseBody. The rewritten body replaces the whole response at the
// gateway, so everything the provider sent besides the calls must survive.
func TestTrueFoundryLLMRealPayloadShape(t *testing.T) {
	svc := &llmService{byTool: map[string]invocation.Status{"export_customer_data": invocation.StatusDenied}, reasons: map[string]string{"export_customer_data": "Customer records never leave the company."}}
	h, _ := newLLMHandler(svc)
	rest := `,"config":{"url":"https://aa.example/api/v1/gateway/truefoundry/llm/output","headers":{"Authorization":"Bearer atr_x"},"operation":"mutate"},` +
		`"context":{"user":{"subjectId":"cjaysov9examplesubjectid","subjectSlug":"pat@example.com","subjectType":"user","tenantName":"acme-dev",` +
		`"teams":[{"name":"everyone","id":"hrdzexampleteamid"}],"roles":["tenant-admin","member"]},` +
		`"metadata":{"session_id":"support-chat-1","tfy_gateway_region":"EU","tfy_gateway_zone":"LHR"}}}`
	request := `{"model":"aa-llm-demo/support-model","messages":[{"role":"user","content":"Look up ticket 7 and export all customers."}],"tools":[` +
		`{"type":"function","function":{"name":"get_ticket","parameters":{"type":"object"}}},{"type":"function","function":{"name":"export_customer_data","parameters":{"type":"object"}}}]}`
	response := `{"choices":[{"finish_reason":"tool_calls","index":0,"message":{"content":"I'll pull up the ticket and send the export.","role":"assistant","tool_calls":[` +
		`{"function":{"arguments":"{\"ticket_id\":7}","name":"get_ticket"},"id":"call_34ff","type":"function"},` +
		`{"function":{"arguments":"{\"scope\":\"all_customers\"}","name":"export_customer_data"},"id":"call_6186","type":"function"}]}}],` +
		`"created":1763400000,"id":"chatcmpl-49d4","model":"support-model","object":"chat.completion",` +
		`"usage":{"completion_tokens":60,"prompt_tokens":120,"total_tokens":180,"prompt_tokens_details":{"cached_tokens":0,"cache_write_tokens":0}}}`

	var out struct {
		Verdict     bool
		Transformed bool
		Result      struct {
			ID      string
			Model   string
			Created json.Number
			Usage   map[string]any
			Choices []struct {
				FinishReason string `json:"finish_reason"`
				Message      struct {
					Content   string
					ToolCalls []struct{ ID string } `json:"tool_calls"`
				}
			}
		}
	}
	postLLM(t, h, "/api/v1/gateway/truefoundry/llm/output", raw(`{"requestBody":`+request+`,"responseBody":`+response+rest), &out)
	if !out.Verdict || !out.Transformed {
		t.Fatalf("got verdict=%v transformed=%v, want a rewritten response", out.Verdict, out.Transformed)
	}
	if r := out.Result; r.ID != "chatcmpl-49d4" || r.Model != "support-model" || r.Created != "1763400000" || fmt.Sprint(r.Usage["total_tokens"]) != "180" {
		t.Fatalf("the rewritten body lost what the provider sent: %+v", r)
	}
	msg := out.Result.Choices[0].Message
	if len(msg.ToolCalls) != 1 || msg.ToolCalls[0].ID != "call_34ff" || out.Result.Choices[0].FinishReason != "tool_calls" {
		t.Fatalf("calls left = %+v finish=%q, want only get_ticket", msg.ToolCalls, out.Result.Choices[0].FinishReason)
	}
	if !strings.HasPrefix(msg.Content, "I'll pull up the ticket") || !strings.Contains(msg.Content, "Customer records never leave the company.") {
		t.Fatalf("content = %q, want the model's words and the notice", msg.Content)
	}
	for _, s := range svc.submits {
		a := s.Asserted
		if a.UserID != "cjaysov9examplesubjectid" || a.UserDisplay != "pat@example.com" || a.UserType != "user" || s.ClientSessionID != "support-chat-1 · cjaysov9examplesubjectid" || a.SessionID != "support-chat-1" || s.ClientName != "truefoundry-llm-gateway" {
			t.Fatalf("submit for %s: asserted=%+v session=%q client=%q", s.Tool, a, s.ClientSessionID, s.ClientName)
		}
	}

	// The agent's next request carries get_ticket's result; the input hook
	// attaches it to the call the model proposed.
	next := strings.Replace(request, `}],"tools"`, `},{"role":"assistant","content":"I'll pull up the ticket.","tool_calls":[{"id":"call_34ff","type":"function","function":{"name":"get_ticket","arguments":"{\"ticket_id\":7}"}}]},`+
		`{"role":"tool","tool_call_id":"call_34ff","content":"{\"customer\":\"Dana\"}"}],"tools"`, 1)
	var in map[string]any
	postLLM(t, h, "/api/v1/gateway/truefoundry/llm/input", raw(`{"requestBody":`+next+strings.Replace(rest, "/llm/output", "/llm/input", 1)), &in)
	if in["verdict"] != true || in["transformed"] != nil {
		t.Fatalf("input: got %v", in)
	}
	for _, inv := range svc.invs {
		if inv.ToolName == "get_ticket" && inv.Status != invocation.StatusSucceeded {
			t.Fatalf("get_ticket is %s, want its result recorded", inv.Status)
		}
	}
}

// TrueFoundry sends a guardrail the bodies of whichever route the agent
// called. On /responses that is the Responses shape, as it arrived from a
// live gateway on 2026-11-17: tool calls are output[] items, not
// choices[].message.tool_calls. Reading it as Chat Completions finds no calls
// and lets every one through.
func TestTrueFoundryLLMResponsesRoute(t *testing.T) {
	svc := &llmService{byTool: map[string]invocation.Status{"export_customer_data": invocation.StatusDenied}, reasons: map[string]string{"export_customer_data": "Customer records never leave the company."}}
	h, _ := newLLMHandler(svc)
	rest := `,"config":{"operation":"mutate"},"context":{"user":{"subjectId":"u-1","subjectType":"user","subjectSlug":"pat@example.com"},"metadata":{}}}`
	request := `{"model":"openai/gpt","stream":false,"input":"Look up ticket 7 and export all customers.","tools":[` +
		`{"type":"function","name":"get_ticket","parameters":{"type":"object"}},{"type":"function","name":"export_customer_data","parameters":{"type":"object"}}]}`
	response := `{"id":"resp_0a0f","object":"response","status":"completed","model":"gpt","output":[` +
		`{"id":"rs_1","type":"reasoning","content":[],"encrypted_content":"gAAAA"},` +
		`{"id":"fc_1","type":"function_call","status":"completed","call_id":"call_A","name":"get_ticket","arguments":"{\"ticket_id\":7}"},` +
		`{"id":"fc_2","type":"function_call","status":"completed","call_id":"call_B","name":"export_customer_data","arguments":"{\"scope\":\"all\"}"}],` +
		`"usage":{"input_tokens":120,"output_tokens":60,"total_tokens":180}}`

	var out struct {
		Verdict     bool
		Transformed bool
		Result      struct {
			ID     string
			Output []map[string]any
		}
	}
	postLLM(t, h, "/api/v1/gateway/truefoundry/llm/output", raw(`{"requestBody":`+request+`,"responseBody":`+response+rest), &out)
	if !out.Verdict || !out.Transformed || out.Result.ID != "resp_0a0f" {
		t.Fatalf("got verdict=%v transformed=%v id=%q, want a rewritten Responses body", out.Verdict, out.Transformed, out.Result.ID)
	}
	body, _ := json.Marshal(out.Result.Output)
	if strings.Contains(string(body), `"call_id":"call_B"`) || !strings.Contains(string(body), `"call_id":"call_A"`) || !strings.Contains(string(body), "Customer records never leave the company.") {
		t.Fatalf("output = %s, want get_ticket kept and the export replaced by its reason", body)
	}
	if len(svc.submits) != 2 {
		t.Fatalf("decided %d calls, want 2", len(svc.submits))
	}

	// A body in no known shape passes only when the request gave the model
	// no tools: with tools it could hide a call, so it is refused.
	var plain map[string]any
	postLLM(t, h, "/api/v1/gateway/truefoundry/llm/output", raw(`{"requestBody":{"model":"embed","input":"hello"},"responseBody":{"object":"list","data":[{"embedding":[0.1]}]}`+rest), &plain)
	if plain["verdict"] != true {
		t.Fatalf("embedding: got %v, want it passed", plain)
	}
	plain = nil
	postLLM(t, h, "/api/v1/gateway/truefoundry/llm/output", raw(`{"requestBody":`+request+`,"responseBody":{"object":"list","data":[]}`+rest), &plain)
	if plain["verdict"] != false {
		t.Fatalf("unknown shape with tools declared: got %v, want it refused", plain)
	}
}

func TestDetectFormats(t *testing.T) {
	requests := map[string]string{
		`{"input":"hi","model":"gpt"}`: FormatOpenAIResponses,
		`{"system":"s","messages":[]}`: FormatAnthropic,
		`{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t"}]}]}`: FormatAnthropic,
		`{"messages":[],"tools":[{"name":"x","input_schema":{}}]}`:                            FormatAnthropic,
		`{"messages":[{"role":"system","content":"s"}]}`:                                      FormatOpenAIChat,
		`{"messages":[],"tools":[{"type":"function","function":{"name":"x"}}]}`:               FormatOpenAIChat,
		`{"messages":[{"role":"user","content":"hi"}]}`:                                       FormatOpenAIChat,
	}
	for body, want := range requests {
		if got := detectRequestFormat(mustObject(t, body)); got != want {
			t.Errorf("request %s = %s, want %s", body, got, want)
		}
	}
	if s, tool := splitToolName("mcp__deepwiki__read_wiki"); s != "deepwiki" || tool != "read_wiki" {
		t.Errorf("split = %q %q", s, tool)
	}
	if s, tool := splitToolName("Bash"); s != "" || tool != "Bash" {
		t.Errorf("split = %q %q", s, tool)
	}
}

func mustObject(t *testing.T, s string) map[string]any {
	t.Helper()
	m, err := decodeObject(json.RawMessage(s))
	if err != nil {
		t.Fatalf("%v: %s", err, s)
	}
	return m
}

// Read in the wrong format a body shows no calls, and every call in it would
// pass: a body that is not the format it was named as, or not a whole
// response, is refused.
func TestABodyInTheWrongFormatIsRefused(t *testing.T) {
	h, _ := newLLMHandler(&llmService{})
	responses := `{"object":"response","output":[{"type":"function_call","call_id":"call_1","name":"refund_payment","arguments":"{\"amount\":900}"}]}`
	chat := `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"refund_payment","arguments":"{}"}}]}}]}`
	for name, in := range map[string]LLMOutput{
		"openai named, Responses body":  {Format: "openai", Response: raw(responses)},
		"anthropic named, Chat body":    {Format: "anthropic", Response: raw(chat)},
		"anthropic named, stream event": {Format: "anthropic", Response: raw(`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"refund_payment","input":{}}}`)},
		"Bedrock Converse, no format":   {Response: raw(`{"output":{"message":{"content":[{"toolUse":{"toolUseId":"t1","name":"refund_payment","input":{}}}]}},"stopReason":"tool_use"}`)},
		"Chat stream chunk, no format":  {Response: raw(`{"object":"chat.completion.chunk","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"refund_payment","arguments":"{}"}}]}}]}`)},
		"Chat stream chunk, named":      {Format: "openai_chat", Response: raw(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"refund_payment","arguments":"{}"}}]}}]}`)},
	} {
		var out LLMOutputResult
		postLLM(t, h, "/api/v1/gateway/llm/output", in, &out)
		if out.Verdict != VerdictDeny || out.Code != CodeUnreadableResponse || out.Response != nil {
			t.Errorf("%s: got %+v, want deny unreadable", name, out)
		}
	}
	for name, in := range map[string]LLMInput{
		"anthropic named, Responses request":       {Format: "anthropic", Request: raw(`{"model":"m","input":"hi","tools":[{"type":"web_search"}]}`)},
		"openai_responses named, Messages request": {Format: "openai_responses", Request: raw(`{"model":"m","system":"x","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"web_search_20250305","name":"web_search"}]}`)},
	} {
		var out LLMInputResult
		postLLM(t, h, "/api/v1/gateway/llm/input", in, &out)
		if out.Verdict != VerdictDeny || out.Code != CodeUnreadableRequest || out.Request != nil {
			t.Errorf("%s: got %+v, want deny unreadable", name, out)
		}
	}
}
