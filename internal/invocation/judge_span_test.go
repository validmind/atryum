package invocation_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"atryum/internal/invocation"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// sharedSpanRecorder is installed at most once per test binary: the OTEL global
// only wires its delegate on the first SetTracerProvider call, so a second
// provider would silently record nothing. Every span test therefore reads from
// this one recorder and filters for the spans it caused.
var (
	sharedSpanRecorder *tracetest.SpanRecorder
	spanRecorderOnce   sync.Once
)

func spanRecorder(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	spanRecorderOnce.Do(func() {
		sharedSpanRecorder = tracetest.NewSpanRecorder()
		otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sharedSpanRecorder)))
	})
	return sharedSpanRecorder
}

// lastSpanNamed returns the most recently ended span with the given name, or nil.
func lastSpanNamed(sr *tracetest.SpanRecorder, name string) sdktrace.ReadOnlySpan {
	var found sdktrace.ReadOnlySpan
	for _, s := range sr.Ended() {
		if s.Name() == name {
			found = s
		}
	}
	return found
}

// spanAttrs flattens a span's attributes into string and int64 lookups.
func spanAttrs(span sdktrace.ReadOnlySpan) (map[string]string, map[string]int64) {
	strs := map[string]string{}
	ints := map[string]int64{}
	for _, kv := range span.Attributes() {
		if kv.Value.Type().String() == "INT64" {
			ints[string(kv.Key)] = kv.Value.AsInt64()
			continue
		}
		strs[string(kv.Key)] = kv.Value.Emit()
	}
	return strs, ints
}

type stubLLMStore struct{ cfg invocation.LocalLLMConfig }

func (s stubLLMStore) GetLLMConfig(context.Context, string) (invocation.LocalLLMConfig, error) {
	return s.cfg, nil
}

// TestEvaluateToolCallEmitsJudgeSpan verifies the local judge call produces a
// gen_ai "chat {model}" span carrying the model, token usage, and verdict —
// the shape Langfuse renders as a generation.
func TestEvaluateToolCallEmitsJudgeSpan(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"{\"verdict\":\"approved\",\"confidence\":0.9,\"reason\":\"ok\"}"}}],"usage":{"prompt_tokens":11,"completion_tokens":7}}`)
	}))
	defer srv.Close()

	sr := spanRecorder(t)

	e := invocation.NewLocalEvaluatorClient(stubLLMStore{cfg: invocation.LocalLLMConfig{
		ID: "cfg1", Provider: "openai", Model: "gpt-4o-mini", BaseURL: srv.URL, APIKey: "x",
	}})
	resp, err := e.EvaluateToolCall(context.Background(), invocation.EvaluateRequest{
		AtryumLLMConfigID: "cfg1", Charter: "be safe", ServerName: "srv", ToolName: "tool",
	})
	if err != nil {
		t.Fatalf("EvaluateToolCall: %v", err)
	}
	if resp.Verdict != "approved" {
		t.Fatalf("verdict = %q, want approved", resp.Verdict)
	}

	chat := lastSpanNamed(sr, "chat gpt-4o-mini")
	if chat == nil {
		t.Fatal("no 'chat gpt-4o-mini' span recorded")
	}
	got, gotInt := spanAttrs(chat)

	if got["gen_ai.request.model"] != "gpt-4o-mini" {
		t.Errorf("gen_ai.request.model = %q", got["gen_ai.request.model"])
	}
	if got["gen_ai.system"] != "openai" {
		t.Errorf("gen_ai.system = %q", got["gen_ai.system"])
	}
	if got["atryum.judge.verdict"] != "approved" {
		t.Errorf("atryum.judge.verdict = %q", got["atryum.judge.verdict"])
	}
	if gotInt["gen_ai.usage.input_tokens"] != 11 {
		t.Errorf("input_tokens = %d, want 11", gotInt["gen_ai.usage.input_tokens"])
	}
	if gotInt["gen_ai.usage.output_tokens"] != 7 {
		t.Errorf("output_tokens = %d, want 7", gotInt["gen_ai.usage.output_tokens"])
	}
}
