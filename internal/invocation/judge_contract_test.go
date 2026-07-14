package invocation

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// These are deterministic contract tests for the judge's fail-closed behavior and
// request construction. They use an httptest server (no live LLM) and carry NO
// build tag, so they run under plain `go test ./internal/invocation`. They guard
// the grounding harness's own call path: the harness invokes the same
// EvaluateToolCall, so verifying it here proves the harness assembles a correct
// request (temperature 0, charter in the system prompt) and parses defensively.

// openAIStub replies to /v1/chat/completions with a fixed assistant message and
// captures the decoded request payload for assertions.
func newOpenAIStub(t *testing.T, assistantContent string) (*httptest.Server, *map[string]any) {
	t.Helper()
	var payload map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": assistantContent}}},
		})
	}))
	t.Cleanup(srv.Close)
	return srv, &payload
}

func newJudge(t *testing.T, srvURL string) *LocalEvaluatorClient {
	t.Helper()
	return NewLocalEvaluatorClient(localLLMConfigStoreStub{cfg: LocalLLMConfig{
		ID:       "judge-eval",
		Provider: "openai_compatible",
		Model:    "judge-model",
		BaseURL:  srvURL,
	}})
}

func evalWith(t *testing.T, judge *LocalEvaluatorClient, req EvaluateRequest) EvaluateResponse {
	t.Helper()
	resp, err := judge.EvaluateToolCall(context.Background(), req)
	if err != nil {
		t.Fatalf("EvaluateToolCall: %v", err)
	}
	return resp
}

func TestJudgeGarbageOutputFailsClosedToDenied(t *testing.T) {
	srv, _ := newOpenAIStub(t, "I'm not going to answer in JSON, sorry!")
	resp := evalWith(t, newJudge(t, srv.URL), EvaluateRequest{
		AtryumLLMConfigID: "judge-eval", Charter: "c", ServerName: "s", ToolName: "t",
	})
	if resp.Verdict != "denied" {
		t.Fatalf("garbage output verdict = %q, want denied (fail-closed)", resp.Verdict)
	}
}

func TestJudgeMarkdownFencedJSONIsParsed(t *testing.T) {
	fenced := "```json\n{\"verdict\":\"human_approval\",\"confidence\":0.7,\"reason\":\"needs a person\"}\n```"
	srv, _ := newOpenAIStub(t, fenced)
	resp := evalWith(t, newJudge(t, srv.URL), EvaluateRequest{
		AtryumLLMConfigID: "judge-eval", Charter: "c", ServerName: "s", ToolName: "t",
	})
	if resp.Verdict != "human_approval" {
		t.Fatalf("fenced JSON verdict = %q, want human_approval", resp.Verdict)
	}
	if resp.Reason != "needs a person" {
		t.Fatalf("fenced JSON reason = %q, want %q", resp.Reason, "needs a person")
	}
}

func TestJudgeUnrecognizedVerdictFailsClosedToDenied(t *testing.T) {
	srv, _ := newOpenAIStub(t, `{"verdict":"maybe","confidence":0.9,"reason":"unsure"}`)
	resp := evalWith(t, newJudge(t, srv.URL), EvaluateRequest{
		AtryumLLMConfigID: "judge-eval", Charter: "c", ServerName: "s", ToolName: "t",
	})
	if resp.Verdict != "denied" {
		t.Fatalf("unrecognized verdict = %q, want denied (fail-closed)", resp.Verdict)
	}
}

func TestJudgeRequestCarriesCharterAndTemperatureZero(t *testing.T) {
	srv, payload := newOpenAIStub(t, `{"verdict":"approved","confidence":1,"reason":"ok"}`)
	const charter = "SENTINEL-CHARTER: only approve read-only calls."
	evalWith(t, newJudge(t, srv.URL), EvaluateRequest{
		AtryumLLMConfigID: "judge-eval",
		Charter:           charter,
		ServerName:        "analytics-db",
		ToolName:          "run_sql",
		ToolArgs:          map[string]any{"query": "SELECT 1"},
		Context:           "Tool description: run sql.",
	})

	if got, ok := (*payload)["temperature"].(float64); !ok || got != 0 {
		t.Fatalf("temperature = %#v, want 0", (*payload)["temperature"])
	}
	msgs, ok := (*payload)["messages"].([]any)
	if !ok || len(msgs) < 2 {
		t.Fatalf("messages = %#v, want system+user", (*payload)["messages"])
	}
	system, _ := msgs[0].(map[string]any)
	if role, _ := system["role"].(string); role != "system" {
		t.Fatalf("first message role = %q, want system", role)
	}
	sysContent, _ := system["content"].(string)
	if !strings.Contains(sysContent, charter) {
		t.Fatalf("charter not found in system prompt:\n%s", sysContent)
	}
	user, _ := msgs[1].(map[string]any)
	userContent, _ := user["content"].(string)
	for _, want := range []string{"analytics-db", "run_sql", "SELECT 1", "Tool description: run sql."} {
		if !strings.Contains(userContent, want) {
			t.Fatalf("user message missing %q:\n%s", want, userContent)
		}
	}
}
