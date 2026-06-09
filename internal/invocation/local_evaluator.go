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

const judgeSystemPrompt = `You are an AI safety judge. Evaluate whether the following tool call should be approved, denied, escalated to a human, or deferred to the next rule, based on the agent's governing constitution.

Constitution:
%s

Verdicts:
- "approved"       — the tool call is clearly permitted by the constitution
- "denied"         — the tool call is clearly prohibited by the constitution
- "human_approval" — the tool call is ambiguous and should be reviewed by a human
- "next_rule"      — the constitution does not cover this case; defer to the next matching rule

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
// Falls back to next_rule on unparseable output; returns an error on HTTP failure.
func (e *LocalEvaluatorClient) EvaluateToolCall(ctx context.Context, req EvaluateRequest) (EvaluateResponse, error) {
	cfg, err := e.store.GetLLMConfig(ctx, req.AtryumLLMConfigID)
	if err != nil {
		return EvaluateResponse{}, fmt.Errorf("local evaluator: fetch llm config %q: %w", req.AtryumLLMConfigID, err)
	}

	userContent := e.buildUserMessage(req)
	systemContent := fmt.Sprintf(judgeSystemPrompt, req.Constitution)

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
	return fmt.Sprintf(
		"Tool call to evaluate:\n- Server: %s\n- Tool: %s\n- Arguments: %s",
		req.ServerName, req.ToolName, string(toolArgsJSON),
	)
}

// callOpenAI calls the OpenAI chat completions API (or any compatible endpoint).
func (e *LocalEvaluatorClient) callOpenAI(ctx context.Context, cfg LocalLLMConfig, system, user string) (string, error) {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "https://api.openai.com"
	}
	baseURL = strings.TrimRight(baseURL, "/")
	endpoint := baseURL + "/v1/chat/completions"

	body, _ := json.Marshal(map[string]any{
		"model": cfg.Model,
		"messages": []map[string]any{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
		"temperature": 0.0,
	})

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

	body, _ := json.Marshal(map[string]any{
		"model":      cfg.Model,
		"max_tokens": 512,
		"system":     system,
		"messages": []map[string]any{
			{"role": "user", "content": user},
		},
	})

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
// Falls back to "next_rule" on any parse error so no invocation is silently lost.
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
