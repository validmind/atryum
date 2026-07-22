package invocation_test

import (
	"context"
	"testing"
	"time"

	"atryum/internal/invocation"
)

// TestVMJudgeGenerationSpan verifies the VM path synthesizes a gen_ai
// "chat {model}" generation span from the backend-returned facts — model,
// tokens, verdict — backdated so its duration is the reported LLM latency.
// (Cost is derived downstream by the OTEL backend from model + tokens.)
func TestVMJudgeGenerationSpan(t *testing.T) {
	sr := spanRecorder(t)

	conf := 0.83
	invocation.RecordVMJudgeGeneration(context.Background(), invocation.EvaluateResponse{
		Verdict:    "approved",
		Confidence: &conf,
		Model:      "gpt-5.4",
		Usage:      &invocation.TokenUsage{InputTokens: 351, OutputTokens: 42},
		LatencyMS:  250,
		Prompt:     []byte(`[{"role":"system","content":"charter"}]`),
		Completion: `{"verdict":"approved"}`,
	})

	chat := lastSpanNamed(sr, "chat gpt-5.4")
	if chat == nil {
		t.Fatal("no 'chat gpt-5.4' span recorded")
	}
	got, gotInt := spanAttrs(chat)

	if got["gen_ai.request.model"] != "gpt-5.4" {
		t.Errorf("gen_ai.request.model = %q, want gpt-5.4", got["gen_ai.request.model"])
	}
	if got["gen_ai.system"] != "openai" {
		t.Errorf("gen_ai.system = %q, want openai", got["gen_ai.system"])
	}
	if got["atryum.judge.verdict"] != "approved" {
		t.Errorf("atryum.judge.verdict = %q, want approved", got["atryum.judge.verdict"])
	}
	if gotInt["gen_ai.usage.input_tokens"] != 351 {
		t.Errorf("input_tokens = %d, want 351", gotInt["gen_ai.usage.input_tokens"])
	}
	if gotInt["gen_ai.usage.output_tokens"] != 42 {
		t.Errorf("output_tokens = %d, want 42", gotInt["gen_ai.usage.output_tokens"])
	}
	if d := chat.EndTime().Sub(chat.StartTime()); d != 250*time.Millisecond {
		t.Errorf("span duration = %v, want 250ms (backdated by latency)", d)
	}
	if got["gen_ai.prompt"] == "" {
		t.Error("gen_ai.prompt missing (content capture on by default)")
	}
	if got["gen_ai.completion"] == "" {
		t.Error("gen_ai.completion missing (content capture on by default)")
	}
}

// TestVMJudgeContentCaptureDisabled verifies the capture_content=false gate keeps
// prompt/completion text off the span while still emitting model/tokens/verdict.
func TestVMJudgeContentCaptureDisabled(t *testing.T) {
	sr := spanRecorder(t)

	invocation.SetContentCapture(false)
	defer invocation.SetContentCapture(true)

	invocation.RecordVMJudgeGeneration(context.Background(), invocation.EvaluateResponse{
		Verdict:    "denied",
		Model:      "nocap-judge",
		Usage:      &invocation.TokenUsage{InputTokens: 10, OutputTokens: 2},
		Prompt:     []byte(`[{"role":"system","content":"secret charter"}]`),
		Completion: `{"verdict":"denied"}`,
	})

	chat := lastSpanNamed(sr, "chat nocap-judge")
	if chat == nil {
		t.Fatal("no 'chat nocap-judge' span recorded")
	}
	got, gotInt := spanAttrs(chat)
	if got["gen_ai.prompt"] != "" || got["gen_ai.completion"] != "" {
		t.Error("content captured despite capture_content=false")
	}
	if gotInt["gen_ai.usage.input_tokens"] != 10 {
		t.Error("token counts should still be emitted when content capture is off")
	}
}

// TestVMJudgeGenerationSpanNoModel verifies a degraded backend response (no
// model — e.g. an older backend that returns only a verdict) produces no
// generation span rather than an empty "chat " span.
func TestVMJudgeGenerationSpanNoModel(t *testing.T) {
	sr := spanRecorder(t)

	invocation.RecordVMJudgeGeneration(context.Background(), invocation.EvaluateResponse{
		Verdict: "denied",
	})

	if s := lastSpanNamed(sr, "chat "); s != nil {
		t.Error("expected no generation span when model is empty")
	}
}
