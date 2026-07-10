package invocation

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// LLMConfigProvider is the minimal interface the local evaluator needs to look
// up a stored LLM configuration. Satisfied by *store.LLMConfigsRepo via the
// localEvaluatorLookup adapter in cmd/atryum/main.go (avoids import cycle).
type LLMConfigProvider interface {
	GetLLMConfig(ctx context.Context, id string) (LocalLLMConfig, error)
}

// LocalLLMConfig holds the configuration needed for a single LLM provider call.
type LocalLLMConfig struct {
	ID       string
	Provider string // "openai" | "anthropic" | "openai_compatible"
	Model    string
	APIKey   string
	BaseURL  string // required for openai_compatible
}

const judgeSystemPrompt = `You are an AI safety judge. Evaluate whether the following tool call should be approved, denied, escalated to a human, or deferred to the next rule, based on the agent's governing charter.

Charter:
%s

Verdicts:
- "approved"       — the tool call is clearly permitted by the charter
- "denied"         — the tool call is clearly prohibited by the charter
- "human_approval" — the tool call is ambiguous and should be reviewed by a human
- "next_rule"      — the charter does not cover this case; defer to the next matching rule

Respond with valid JSON only — no markdown fences, no extra text:
{"verdict": "approved|denied|human_approval|next_rule", "confidence": 0.0, "reason": "..."}`

// LocalEvaluatorClient implements EvaluatorClient using a locally-configured
// LLM. It supports OpenAI, Anthropic, and OpenAI-compatible providers (Ollama,
// LM Studio, Azure OpenAI, etc.) via raw net/http — no extra dependencies.
type LocalEvaluatorClient struct {
	store      LLMConfigProvider
	httpClient *http.Client
}

func NewLocalEvaluatorClient(store LLMConfigProvider) *LocalEvaluatorClient {
	return &LocalEvaluatorClient{
		store: store,
		httpClient: &http.Client{
			Timeout: 90 * time.Second,
		},
	}
}

// EvaluateToolCall calls the locally-configured LLM to judge the tool call.
// Falls back to denied on unparseable output; returns an error on HTTP failure.
func (e *LocalEvaluatorClient) EvaluateToolCall(ctx context.Context, req EvaluateRequest) (EvaluateResponse, error) {
	cfg, err := e.store.GetLLMConfig(ctx, req.AtryumLLMConfigID)
	if err != nil {
		return EvaluateResponse{}, fmt.Errorf("local evaluator: fetch llm config %q: %w", req.AtryumLLMConfigID, err)
	}

	userContent := e.buildUserMessage(req)
	systemContent := fmt.Sprintf(judgeSystemPrompt, req.Charter)

	var rawResp string
	switch cfg.Provider {
	case "anthropic":
		rawResp, err = e.callAnthropic(ctx, cfg, systemContent, userContent)
	default: // "openai" and "openai_compatible"
		rawResp, err = e.callOpenAI(ctx, cfg, systemContent, userContent)
	}
	if err != nil {
		return EvaluateResponse{}, fmt.Errorf("local evaluator: llm call failed: %w", err)
	}

	verdict, confidence, reason := e.parseVerdict(rawResp)
	slog.Info("local evaluator: verdict",
		"verdict", verdict,
		"config_id", cfg.ID,
		"provider", cfg.Provider,
		"model", cfg.Model,
		"reason", reason,
	)

	c := confidence
	return EvaluateResponse{
		Verdict:    verdict,
		Reason:     reason,
		Confidence: &c,
	}, nil
}

func (e *LocalEvaluatorClient) buildUserMessage(req EvaluateRequest) string {
	toolArgsJSON, _ := json.Marshal(req.ToolArgs)
	msg := fmt.Sprintf(
		"Tool call to evaluate:\n- Server: %s\n- Tool: %s\n- Arguments: %s",
		req.ServerName, req.ToolName, string(toolArgsJSON),
	)
	if ctx := strings.TrimSpace(req.Context); ctx != "" {
		msg += "\n\nAdditional context:\n" + ctx
	}
	return msg
}

// callOpenAI calls the OpenAI chat completions API (or any compatible endpoint).
func (e *LocalEvaluatorClient) callOpenAI(ctx context.Context, cfg LocalLLMConfig, system, user string) (string, error) {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "https://api.openai.com"
	}
	baseURL = strings.TrimRight(baseURL, "/")
	endpoint := baseURL + "/v1/chat/completions"

	payload := map[string]any{
		"model":       cfg.Model,
		"temperature": 0.0,
		"messages": []map[string]any{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
	}
	body, _ := json.Marshal(payload)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if cfg.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}

	resp, err := e.httpClient.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("openai api error %d: %s", resp.StatusCode, truncate(string(respBody), 200))
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("parse openai response: %w", err)
	}
	if len(result.Choices) == 0 {
		return "", fmt.Errorf("openai returned no choices")
	}
	return result.Choices[0].Message.Content, nil
}

// callAnthropic calls the Anthropic messages API.
func (e *LocalEvaluatorClient) callAnthropic(ctx context.Context, cfg LocalLLMConfig, system, user string) (string, error) {
	endpoint := "https://api.anthropic.com/v1/messages"

	payload := map[string]any{
		"model":       cfg.Model,
		"max_tokens":  512,
		"temperature": 0.0,
		"system":      system,
		"messages": []map[string]any{
			{"role": "user", "content": user},
		},
	}
	body, _ := json.Marshal(payload)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	if cfg.APIKey != "" {
		httpReq.Header.Set("x-api-key", cfg.APIKey)
	}

	resp, err := e.httpClient.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("anthropic api error %d: %s", resp.StatusCode, truncate(string(respBody), 200))
	}

	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("parse anthropic response: %w", err)
	}
	for _, block := range result.Content {
		if block.Type == "text" {
			return block.Text, nil
		}
	}
	return "", fmt.Errorf("anthropic returned no text content")
}

// parseVerdict extracts verdict/confidence/reason from the LLM JSON output.
// Falls back to "denied" on any parse error so no invocation is silently approved.
func (e *LocalEvaluatorClient) parseVerdict(raw string) (verdict string, confidence float64, reason string) {
	raw = strings.TrimSpace(raw)
	// Strip markdown fences if the model wrapped the JSON anyway.
	if idx := strings.Index(raw, "{"); idx > 0 {
		raw = raw[idx:]
	}
	if idx := strings.LastIndex(raw, "}"); idx >= 0 && idx < len(raw)-1 {
		raw = raw[:idx+1]
	}

	var out struct {
		Verdict    string  `json:"verdict"`
		Confidence float64 `json:"confidence"`
		Reason     string  `json:"reason"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		slog.Warn("local evaluator: could not parse LLM verdict JSON; falling back to denied",
			"raw", truncate(raw, 200), "error", err)
		return "denied", 0.0, "could not parse LLM output"
	}

	switch out.Verdict {
	case "approved", "denied", "human_approval", "next_rule":
		// valid verdicts
	default:
		slog.Warn("local evaluator: unrecognised verdict; falling back to denied", "verdict", out.Verdict)
		return "denied", out.Confidence, out.Reason
	}
	return out.Verdict, out.Confidence, out.Reason
}

const planJudgeSystemPrompt = `You are an AI safety judge. An agent has proposed a plan — a goal, a rationale, and a sequence of intended tool calls — and asks for approval before executing any of them. Evaluate the plan as a whole against the agent's governing charter.

Charter:
%s

Verdicts:
- "approved"       — the plan is clearly permitted by the charter
- "denied"         — the plan is clearly prohibited by the charter
- "revise"         — the plan is close to acceptable but needs changes; explain what to change in "feedback"
- "human_approval" — the plan is ambiguous and should be reviewed by a human
- "next_rule"      — the charter does not cover this case; defer to the next matching rule

Respond with valid JSON only — no markdown fences, no extra text:
{"verdict": "approved|denied|revise|human_approval|next_rule", "confidence": 0.0, "reason": "...", "feedback": "..."}`

// PlanEvaluateRequest asks a judge to evaluate a whole submitted plan.
type PlanEvaluateRequest struct {
	AtryumLLMConfigID string
	Charter           string
	Source            string
	Goal              string
	Rationale         string
	Context           string
	Actions           []PlanAction
}

type PlanEvaluateResponse struct {
	Verdict    string
	Reason     string
	Feedback   string
	Confidence *float64
}

// PlanAdherenceRequest asks a judge whether an actual tool call follows an
// already-approved plan action that matched deterministically.
type PlanAdherenceRequest struct {
	AtryumLLMConfigID string
	Charter           string
	Plan              Plan
	Action            PlanAction
	ServerName        string
	ToolName          string
	ToolArgs          map[string]any
	Context           string
}

type PlanAdherenceResponse struct {
	Verdict    string
	Reason     string
	Confidence *float64
}

// PlanEvaluator judges submitted plans and matching tool calls.
// Satisfied by *LocalEvaluatorClient.
type PlanEvaluator interface {
	EvaluatePlan(ctx context.Context, req PlanEvaluateRequest) (PlanEvaluateResponse, error)
	EvaluatePlanAdherence(ctx context.Context, req PlanAdherenceRequest) (PlanAdherenceResponse, error)
}

// EvaluatePlan calls the locally-configured LLM to judge a submitted plan.
// Falls back to denied on unparseable output; returns an error on HTTP failure.
func (e *LocalEvaluatorClient) EvaluatePlan(ctx context.Context, req PlanEvaluateRequest) (PlanEvaluateResponse, error) {
	cfg, err := e.store.GetLLMConfig(ctx, req.AtryumLLMConfigID)
	if err != nil {
		return PlanEvaluateResponse{}, fmt.Errorf("local plan evaluator: fetch llm config %q: %w", req.AtryumLLMConfigID, err)
	}

	systemContent := fmt.Sprintf(planJudgeSystemPrompt, req.Charter)
	userContent := e.buildPlanMessage(req)

	var rawResp string
	switch cfg.Provider {
	case "anthropic":
		rawResp, err = e.callAnthropic(ctx, cfg, systemContent, userContent)
	default: // "openai" and "openai_compatible"
		rawResp, err = e.callOpenAI(ctx, cfg, systemContent, userContent)
	}
	if err != nil {
		return PlanEvaluateResponse{}, fmt.Errorf("local plan evaluator: llm call failed: %w", err)
	}

	resp := e.parsePlanVerdict(rawResp)
	slog.Info("local plan evaluator: verdict",
		"verdict", resp.Verdict,
		"config_id", cfg.ID,
		"provider", cfg.Provider,
		"model", cfg.Model,
		"reason", resp.Reason,
	)
	return resp, nil
}

func (e *LocalEvaluatorClient) buildPlanMessage(req PlanEvaluateRequest) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Plan to evaluate:\n- Source: %s\n- Goal: %s\n", req.Source, req.Goal)
	if req.Rationale != "" {
		fmt.Fprintf(&b, "- Rationale: %s\n", req.Rationale)
	}
	b.WriteString("- Intended actions:\n")
	for i, a := range req.Actions {
		fmt.Fprintf(&b, "  %d. tool=%s", i+1, a.Tool)
		if a.Server != "" {
			fmt.Fprintf(&b, " server=%s", a.Server)
		}
		if a.Description != "" {
			fmt.Fprintf(&b, " — %s", a.Description)
		}
		if a.InputSummary != "" {
			fmt.Fprintf(&b, " (input: %s)", a.InputSummary)
		}
		b.WriteString("\n")
	}
	if ctx := strings.TrimSpace(req.Context); ctx != "" {
		b.WriteString("\nAdditional context:\n" + ctx)
	}
	return b.String()
}

// parsePlanVerdict extracts the plan verdict from the LLM JSON output.
// Falls back to "denied" on any parse error so no plan is silently approved.
func (e *LocalEvaluatorClient) parsePlanVerdict(raw string) PlanEvaluateResponse {
	raw = strings.TrimSpace(raw)
	if idx := strings.Index(raw, "{"); idx > 0 {
		raw = raw[idx:]
	}
	if idx := strings.LastIndex(raw, "}"); idx >= 0 && idx < len(raw)-1 {
		raw = raw[:idx+1]
	}

	var out struct {
		Verdict    string  `json:"verdict"`
		Confidence float64 `json:"confidence"`
		Reason     string  `json:"reason"`
		Feedback   string  `json:"feedback"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		slog.Warn("local plan evaluator: could not parse LLM verdict JSON; falling back to denied",
			"raw", truncate(raw, 200), "error", err)
		return PlanEvaluateResponse{Verdict: "denied", Reason: "could not parse LLM output"}
	}

	switch out.Verdict {
	case "approved", "denied", "revise", "human_approval", "next_rule":
		// valid verdicts
	default:
		slog.Warn("local plan evaluator: unrecognised verdict; falling back to denied", "verdict", out.Verdict)
		out.Verdict = "denied"
	}
	c := out.Confidence
	return PlanEvaluateResponse{Verdict: out.Verdict, Reason: out.Reason, Feedback: out.Feedback, Confidence: &c}
}

const planAdherenceSystemPrompt = `You are an AI safety judge. Evaluate whether an actual tool call follows an already-approved plan and the specific planned action it matched.

Charter:
%s

Verdicts:
- "follows_plan"       — the tool call is a reasonable execution of the approved plan/action
- "outside_plan"       — the tool call is materially outside the approved plan/action
- "human_approval"     — the relationship is ambiguous and should be reviewed by a human

A read-only poll of the approved plan's own status (an HTTP GET of its /api/v1/external/plans/{plan_id} endpoint) always counts as following the plan. But judge the ENTIRE call: if a status poll is combined with any other command, side effect, or data-modifying request, evaluate everything else it does on its own merits.

Respond with valid JSON only — no markdown fences, no extra text:
{"verdict": "follows_plan|outside_plan|human_approval", "confidence": 0.0, "reason": "..."}`

// EvaluatePlanAdherence calls the locally-configured LLM to judge whether a
// tool call follows an approved plan action. Falls back to human review on
// unparseable output; returns an error on HTTP failure.
func (e *LocalEvaluatorClient) EvaluatePlanAdherence(ctx context.Context, req PlanAdherenceRequest) (PlanAdherenceResponse, error) {
	cfg, err := e.store.GetLLMConfig(ctx, req.AtryumLLMConfigID)
	if err != nil {
		return PlanAdherenceResponse{}, fmt.Errorf("local plan adherence evaluator: fetch llm config %q: %w", req.AtryumLLMConfigID, err)
	}

	systemContent := fmt.Sprintf(planAdherenceSystemPrompt, req.Charter)
	userContent := e.buildPlanAdherenceMessage(req)

	var rawResp string
	switch cfg.Provider {
	case "anthropic":
		rawResp, err = e.callAnthropic(ctx, cfg, systemContent, userContent)
	default:
		rawResp, err = e.callOpenAI(ctx, cfg, systemContent, userContent)
	}
	if err != nil {
		return PlanAdherenceResponse{}, fmt.Errorf("local plan adherence evaluator: llm call failed: %w", err)
	}

	resp := e.parsePlanAdherenceVerdict(rawResp)
	slog.Info("local plan adherence evaluator: verdict",
		"verdict", resp.Verdict,
		"config_id", cfg.ID,
		"provider", cfg.Provider,
		"model", cfg.Model,
		"plan_id", req.Plan.PlanID,
		"tool", req.ToolName,
		"reason", resp.Reason,
	)
	return resp, nil
}

func (e *LocalEvaluatorClient) buildPlanAdherenceMessage(req PlanAdherenceRequest) string {
	toolArgsJSON, _ := json.Marshal(req.ToolArgs)
	var b strings.Builder
	fmt.Fprintf(&b, "Approved plan:\n- Plan ID: %s\n- Source: %s\n- Goal: %s\n", req.Plan.PlanID, req.Plan.Source, req.Plan.Goal)
	if req.Plan.Rationale != "" {
		fmt.Fprintf(&b, "- Rationale: %s\n", req.Plan.Rationale)
	}
	b.WriteString("- Approved actions:\n")
	for i, a := range req.Plan.Actions {
		fmt.Fprintf(&b, "  %d. tool=%s", i+1, a.Tool)
		if a.Server != "" {
			fmt.Fprintf(&b, " server=%s", a.Server)
		}
		if a.Description != "" {
			fmt.Fprintf(&b, " — %s", a.Description)
		}
		if a.InputSummary != "" {
			fmt.Fprintf(&b, " (input: %s)", a.InputSummary)
		}
		b.WriteString("\n")
	}
	b.WriteString("\nDeterministically matched action:\n")
	fmt.Fprintf(&b, "- tool=%s", req.Action.Tool)
	if req.Action.Server != "" {
		fmt.Fprintf(&b, " server=%s", req.Action.Server)
	}
	if req.Action.Description != "" {
		fmt.Fprintf(&b, "\n- description=%s", req.Action.Description)
	}
	if req.Action.InputSummary != "" {
		fmt.Fprintf(&b, "\n- input_summary=%s", req.Action.InputSummary)
	}
	fmt.Fprintf(&b, "\n\nActual tool call:\n- Server: %s\n- Tool: %s\n- Arguments: %s", req.ServerName, req.ToolName, string(toolArgsJSON))
	if ctx := strings.TrimSpace(req.Context); ctx != "" {
		b.WriteString("\n\nAdditional context:\n" + ctx)
	}
	return b.String()
}

func (e *LocalEvaluatorClient) parsePlanAdherenceVerdict(raw string) PlanAdherenceResponse {
	raw = strings.TrimSpace(raw)
	if idx := strings.Index(raw, "{"); idx > 0 {
		raw = raw[idx:]
	}
	if idx := strings.LastIndex(raw, "}"); idx >= 0 && idx < len(raw)-1 {
		raw = raw[:idx+1]
	}

	var out struct {
		Verdict    string  `json:"verdict"`
		Confidence float64 `json:"confidence"`
		Reason     string  `json:"reason"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		slog.Warn("local plan adherence evaluator: could not parse LLM verdict JSON; falling back to human_approval",
			"raw", truncate(raw, 200), "error", err)
		return PlanAdherenceResponse{Verdict: "human_approval", Reason: "could not parse LLM output"}
	}
	switch out.Verdict {
	case "follows_plan", "outside_plan", "human_approval":
	default:
		slog.Warn("local plan adherence evaluator: unrecognised verdict; falling back to human_approval", "verdict", out.Verdict)
		out.Verdict = "human_approval"
	}
	c := out.Confidence
	return PlanAdherenceResponse{Verdict: out.Verdict, Reason: out.Reason, Confidence: &c}
}

const summarizeSystemPrompt = `You are a concise technical analyst. Summarize the following AI agent tool call in 1–3 sentences. Focus on what was requested, what action was taken, and the outcome. Be factual and brief.`

// SummarizeInvocation calls the locally-configured LLM to produce a plain-text
// summary of an invocation payload. Returns the summary string.
func (e *LocalEvaluatorClient) SummarizeInvocation(ctx context.Context, llmConfigID string, invocationData map[string]any) (string, error) {
	cfg, err := e.store.GetLLMConfig(ctx, llmConfigID)
	if err != nil {
		return "", fmt.Errorf("local summarizer: fetch llm config %q: %w", llmConfigID, err)
	}

	invJSON, _ := json.Marshal(invocationData)
	userContent := "Invocation data:\n" + string(invJSON)

	var raw string
	switch cfg.Provider {
	case "anthropic":
		raw, err = e.callAnthropic(ctx, cfg, summarizeSystemPrompt, userContent)
	default:
		raw, err = e.callOpenAI(ctx, cfg, summarizeSystemPrompt, userContent)
	}
	if err != nil {
		return "", fmt.Errorf("local summarizer: llm call failed: %w", err)
	}
	return strings.TrimSpace(raw), nil
}

// DispatchingEvaluator routes evaluation requests to either the VM backend
// evaluator or the local LLM evaluator based on which field is set in the request.
type DispatchingEvaluator struct {
	VM    EvaluatorClient // may be nil when no VM backend is configured
	Local EvaluatorClient // local LLM evaluator; always set
}

func (d *DispatchingEvaluator) EvaluateToolCall(ctx context.Context, req EvaluateRequest) (EvaluateResponse, error) {
	if req.AtryumLLMConfigID != "" {
		return d.Local.EvaluateToolCall(ctx, req)
	}
	if d.VM != nil {
		return d.VM.EvaluateToolCall(ctx, req)
	}
	return EvaluateResponse{}, fmt.Errorf("no evaluator available: neither atryum_llm_config_id nor VM backend is configured")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
