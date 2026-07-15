package invocation

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

type localLLMConfigStoreStub struct {
	cfg LocalLLMConfig
}

func (s localLLMConfigStoreStub) GetLLMConfig(_ context.Context, _ string) (LocalLLMConfig, error) {
	return s.cfg, nil
}

func (s localLLMConfigStoreStub) DefaultLLMConfig(_ context.Context) (LocalLLMConfig, error) {
	return s.cfg, nil
}

func TestLocalEvaluatorJudgeOpenAISendsTemperatureZero(t *testing.T) {
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{"content": `{"verdict":"approved","confidence":1,"reason":"ok"}`},
			}},
		})
	}))
	defer server.Close()

	evaluator := NewLocalEvaluatorClient(localLLMConfigStoreStub{cfg: LocalLLMConfig{
		ID:       "llm-openai",
		Provider: "openai_compatible",
		Model:    "judge-model",
		BaseURL:  server.URL,
	}})

	_, err := evaluator.EvaluateToolCall(context.Background(), EvaluateRequest{
		AtryumLLMConfigID: "llm-openai",
		Charter:           "Approve harmless calls.",
		ServerName:        "github",
		ToolName:          "search",
		ToolArgs:          map[string]any{"q": "x"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if got, ok := payload["temperature"].(float64); !ok || got != 0 {
		t.Fatalf("temperature = %#v, want 0", payload["temperature"])
	}
}

// A plan adherence request without a rule-specific config ID (human- or
// auto-approved plans) must resolve the default enabled config, not fail the
// empty-ID lookup.
func TestLocalEvaluatorPlanAdherenceUsesDefaultConfigWhenIDEmpty(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{"content": `{"verdict":"follows_plan","confidence":1,"reason":"ok"}`},
			}},
		})
	}))
	defer server.Close()

	evaluator := NewLocalEvaluatorClient(defaultOnlyLLMConfigStoreStub{cfg: LocalLLMConfig{
		ID:       "llm-default",
		Provider: "openai_compatible",
		Model:    "judge-model",
		BaseURL:  server.URL,
	}})

	resp, err := evaluator.EvaluatePlanAdherence(context.Background(), PlanAdherenceRequest{
		AtryumLLMConfigID: "",
		Charter:           "Only run planned safe commands.",
		Plan:              Plan{PlanID: "plan_x", Goal: "test"},
		Action:            PlanAction{Tool: "Bash"},
		ToolName:          "Bash",
		ToolArgs:          map[string]any{"cmd": "echo ok"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Verdict != "follows_plan" {
		t.Fatalf("verdict = %q, want follows_plan", resp.Verdict)
	}
}

func TestParsePlanAdherenceVerdictAcceptsCharterViolation(t *testing.T) {
	evaluator := NewLocalEvaluatorClient(localLLMConfigStoreStub{})
	resp := evaluator.parsePlanAdherenceVerdict(`{"verdict":"violates_charter","confidence":0.98,"reason":"deletion is forbidden"}`)
	if resp.Verdict != "violates_charter" {
		t.Fatalf("verdict = %q, want violates_charter", resp.Verdict)
	}
	if resp.Confidence == nil || *resp.Confidence != 0.98 {
		t.Fatalf("confidence = %v, want 0.98", resp.Confidence)
	}
}

// defaultOnlyLLMConfigStoreStub fails GetLLMConfig so the test proves the
// empty-ID path goes through DefaultLLMConfig.
type defaultOnlyLLMConfigStoreStub struct {
	cfg LocalLLMConfig
}

func (s defaultOnlyLLMConfigStoreStub) GetLLMConfig(_ context.Context, id string) (LocalLLMConfig, error) {
	return LocalLLMConfig{}, fmt.Errorf("unexpected GetLLMConfig(%q); want DefaultLLMConfig", id)
}

func (s defaultOnlyLLMConfigStoreStub) DefaultLLMConfig(_ context.Context) (LocalLLMConfig, error) {
	return s.cfg, nil
}

func TestLocalEvaluatorJudgeAnthropicSendsTemperatureZero(t *testing.T) {
	transport := &captureTransport{
		responseBody: `{"content":[{"type":"text","text":"{\"verdict\":\"approved\",\"confidence\":1,\"reason\":\"ok\"}"}]}`,
	}
	evaluator := NewLocalEvaluatorClient(localLLMConfigStoreStub{cfg: LocalLLMConfig{
		ID:       "llm-anthropic",
		Provider: "anthropic",
		Model:    "judge-model",
		APIKey:   "key",
	}})
	evaluator.httpClient = &http.Client{Transport: transport}

	_, err := evaluator.EvaluateToolCall(context.Background(), EvaluateRequest{
		AtryumLLMConfigID: "llm-anthropic",
		Charter:           "Approve harmless calls.",
		ServerName:        "github",
		ToolName:          "search",
		ToolArgs:          map[string]any{"q": "x"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if got, ok := transport.payload["temperature"].(float64); !ok || got != 0 {
		t.Fatalf("temperature = %#v, want 0", transport.payload["temperature"])
	}
}

func TestLocalSummarizerSendsTemperatureZero(t *testing.T) {
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{"content": "summary"},
			}},
		})
	}))
	defer server.Close()

	evaluator := NewLocalEvaluatorClient(localLLMConfigStoreStub{cfg: LocalLLMConfig{
		ID:       "llm-openai",
		Provider: "openai_compatible",
		Model:    "summary-model",
		BaseURL:  server.URL,
	}})

	_, err := evaluator.SummarizeInvocation(context.Background(), "llm-openai", map[string]any{"status": "ok"})
	if err != nil {
		t.Fatal(err)
	}

	if got, ok := payload["temperature"].(float64); !ok || got != 0 {
		t.Fatalf("temperature = %#v, want 0", payload["temperature"])
	}
}

type captureTransport struct {
	payload      map[string]any
	responseBody string
}

func (t *captureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	defer req.Body.Close()
	if err := json.NewDecoder(req.Body).Decode(&t.payload); err != nil {
		return nil, err
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewBufferString(t.responseBody)),
		Request:    req,
	}, nil
}
