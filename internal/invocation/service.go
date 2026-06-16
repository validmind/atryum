package invocation

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"atryum/internal/auth"
	"atryum/internal/invocation/policy"
	"atryum/internal/mcp"
)

// dispositionContinue is an internal sentinel returned by runAIEvaluation when the
// LLM verdict is "next_rule". It is never persisted or returned to clients — the
// Invoke/Submit rule-iteration loop uses it to advance to the next matching approval
// rule. If all matching rules defer, the call falls back to human_approval.
const dispositionContinue policy.Disposition = "continue"

// dispositionAIEscalated is an internal sentinel returned by runAIEvaluation when
// the LLM verdict is "human_approval". It routes to the same human-approval queue as
// DispositionHuman but marks the invocation's approval record with status
// "ai_escalated" so the UI can distinguish a deliberate LLM escalation from a
// direct human_approval rule or an error fallback.
const dispositionAIEscalated policy.Disposition = "ai_escalated"

// humanDecisionStatus returns the approval status string to write when a human
// approves or denies an invocation. If the invocation was AI-escalated the composite
// statuses ("ai_escalated_approved" / "ai_escalated_denied") preserve the origin so
// the UI can show both badges.
func humanDecisionStatus(current *Approval, approved bool) string {
	if current != nil && current.Status == "ai_escalated" {
		if approved {
			return "ai_escalated_approved"
		}
		return "ai_escalated_denied"
	}
	if approved {
		return "approved"
	}
	return "denied"
}

func newApproval(status, reason string, confidence *float64) *Approval {
	return &Approval{
		Status:          status,
		Reason:          stringPtr(reason),
		ConfidenceScore: confidence,
	}
}

// AgentLookup is the minimal interface required by the invocation service to
// resolve an agent's VM CUID from its runtime agent ID.
type AgentLookup interface {
	GetByAgentID(ctx context.Context, agentID string) (AgentRecord, error)
	GetByVMCUID(ctx context.Context, vmCUID string) (AgentRecord, error)
}

// AgentRecord is a lightweight copy of store.AgentRecord used within the
// invocation package to avoid a circular import.
type AgentRecord struct {
	ID                 string // local Atryum agent UUID (used for rule matching)
	VMCUID             string // VM inventory model CUID (used for charter lookup)
	VMOrganizationCUID string // VM organization CUID (used for cross-tenant validation)
	Charter            string // governing text for local LLM-as-judge evaluation
}

// EvaluatorClient is the minimal interface required by the invocation service
// to call the VM backend for LLM evaluation.
type EvaluatorClient interface {
	EvaluateToolCall(ctx context.Context, req EvaluateRequest) (EvaluateResponse, error)
}

// SummaryClient is the minimal interface required by the invocation service to
// call the VM backend for an invocation summary.
type SummaryClient interface {
	SummarizeInvocation(ctx context.Context, req SummaryRequest) (SummaryResponse, error)
}

// SyncSettingsProvider lets the invocation service read the current agent sync
// configuration on demand without importing the store package. The service
// calls CharterFieldKey on every AI evaluation so that changes saved via
// the Settings UI are picked up immediately without a restart.
type SyncSettingsProvider interface {
	CharterFieldKey(ctx context.Context) string
	DefaultAgentVMCUID(ctx context.Context) string
	SummarySettings(ctx context.Context) (orgCUID string, modelConfigCUID string)
}

// EvaluateRequest mirrors backend.EvaluateRequest so the service package does
// not import the backend package directly.
type EvaluateRequest struct {
	ModelConfigCUID string `json:"model_config_cuid"`
	OrgCUID         string `json:"org_cuid,omitempty"`
	AgentVMCUID     string `json:"agent_vm_cuid,omitempty"`
	CharterFieldKey string `json:"charter_field_key,omitempty"`
	// AtryumLLMConfigID references a local LLM config for native evaluation.
	// When set, the local evaluator is used instead of the VM backend.
	AtryumLLMConfigID string `json:"atryum_llm_config_id,omitempty"`
	// Charter is the agent's governing text sent to the local LLM judge.
	Charter    string         `json:"charter,omitempty"`
	ServerName string         `json:"server_name"`
	ToolName   string         `json:"tool_name"`
	ToolArgs   map[string]any `json:"tool_args,omitempty"`
	Context    string         `json:"context,omitempty"`
}

// EvaluateResponse mirrors backend.EvaluateResponse.
// Verdict is one of: "approved", "denied", "human_approval", "next_rule".
type EvaluateResponse struct {
	Verdict    string   `json:"verdict"`
	Reason     string   `json:"reason"`
	Confidence *float64 `json:"confidence,omitempty"`
}

// SummaryRequest mirrors backend.SummarizeInvocationRequest so the service
// package does not import the backend package directly.
type SummaryRequest struct {
	ModelConfigCUID string         `json:"model_config_cuid"`
	OrgCUID         string         `json:"org_cuid,omitempty"`
	Invocation      map[string]any `json:"invocation"`
}

// SummaryResponse mirrors backend.SummarizeInvocationResponse.
type SummaryResponse struct {
	Summary string `json:"summary"`
}

type approvalDecision struct {
	approved bool
	message  string
}

type invocationRepo interface {
	Create(ctx context.Context, inv Invocation) error
	UpdateResult(ctx context.Context, inv Invocation) error
	UpdateSummary(ctx context.Context, id string, summary string) error
	Get(ctx context.Context, id string) (Invocation, error)
	GetByIdempotencyKey(ctx context.Context, key string) (Invocation, error)
	List(ctx context.Context, filter InvocationListFilter) ([]Invocation, int, error)
	ListAgentIDs(ctx context.Context) ([]string, error)
}

type eventRepo interface {
	Create(ctx context.Context, evt Event) error
	ListByInvocation(ctx context.Context, invocationID string, filter EventListFilter) ([]Event, int, error)
}

type resolver interface {
	ResolveContext(ctx context.Context, name string) (mcp.Upstream, error)
	ListAll(ctx context.Context) ([]mcp.Upstream, error)
}

type upstreamClient interface {
	Invoke(ctx context.Context, upstream mcp.Upstream, tool string, input map[string]any, requestID *string) (mcp.InvokeResult, error)
	ListTools(ctx context.Context, upstream mcp.Upstream) ([]mcp.Tool, error)
	ForwardEnvelope(ctx context.Context, upstream mcp.Upstream, envelope mcp.Envelope, protocolVersion string) (mcp.ForwardResult, error)
}

const toolCatalogTTL = 5 * time.Minute

type toolCatalogEntry struct {
	tools     map[string]mcp.Tool
	fetchedAt time.Time
}

type Service struct {
	invocations      invocationRepo
	events           eventRepo
	resolver         resolver
	client           upstreamClient
	policy           policy.Provider
	rules            rulesStore // nil = no rule evaluation
	agents           AgentLookup
	evaluator        EvaluatorClient
	summarizer       SummaryClient
	syncSettings     SyncSettingsProvider // nil = no charter lookup
	defaultTimeout   time.Duration
	mu               sync.Mutex
	pendingApprovals map[string]chan approvalDecision

	toolCatalogMu sync.Mutex
	toolCatalog   map[string]toolCatalogEntry
}

func NewService(
	inv invocationRepo,
	evt eventRepo,
	resolver resolver,
	client upstreamClient,
	policyProvider policy.Provider,
	defaultTimeout time.Duration,
	rules rulesStore,
	agents AgentLookup,
	evaluator EvaluatorClient,
	syncSettings SyncSettingsProvider,
) *Service {
	return &Service{
		invocations:      inv,
		events:           evt,
		resolver:         resolver,
		client:           client,
		policy:           policyProvider,
		rules:            rules,
		agents:           agents,
		evaluator:        evaluator,
		syncSettings:     syncSettings,
		defaultTimeout:   defaultTimeout,
		pendingApprovals: make(map[string]chan approvalDecision),
		toolCatalog:      make(map[string]toolCatalogEntry),
	}
}

// SetInvocationSummarizer installs the optional backend summarizer used to
// summarize invocations automatically when they enter human approval.
func (s *Service) SetInvocationSummarizer(client SummaryClient) {
	s.summarizer = client
}

func (s *Service) Invoke(ctx context.Context, req CreateInvocationRequest) (InvocationResponse, error) {
	if req.Server == "" {
		return InvocationResponse{}, fmt.Errorf("server is required")
	}
	if req.Tool == "" {
		return InvocationResponse{}, fmt.Errorf("tool is required")
	}
	if req.IdempotencyKey != nil && *req.IdempotencyKey != "" {
		existing, err := s.invocations.GetByIdempotencyKey(ctx, *req.IdempotencyKey)
		if err == nil {
			return s.toResponse(existing), nil
		}
		if err != nil && err != sql.ErrNoRows {
			return InvocationResponse{}, err
		}
	}
	upstream, err := s.resolver.ResolveContext(ctx, req.Server)
	if err != nil {
		return InvocationResponse{}, err
	}
	inputJSON, err := json.Marshal(req.Input)
	if err != nil {
		return InvocationResponse{}, err
	}

	// agentID is the authenticated agent identity from middleware. When auth
	// is disabled the field is empty and we fall back to request_id for rule
	// matching to preserve pre-auth behavior.
	agentID := auth.AgentIDFromContext(ctx)
	agentRec := s.resolveAgentRecord(ctx, agentID)

	now := time.Now().UTC()
	inv := Invocation{
		InvocationID:   "inv_" + uuid.NewString(),
		RequestID:      req.RequestID,
		IdempotencyKey: req.IdempotencyKey,
		Tool:           req.Tool,
		Upstream:       upstream.Name,
		Status:         StatusReceived,
		Input:          inputJSON,
		SubmittedAt:    now,
	}
	if agentID != "" {
		inv.AgentID = &agentID
	}
	if req.ClientName != "" {
		v := req.ClientName
		inv.ClientName = &v
	}
	if req.ClientVersion != "" {
		v := req.ClientVersion
		inv.ClientVersion = &v
	}
	if err := s.invocations.Create(ctx, inv); err != nil {
		return InvocationResponse{}, err
	}

	// Determine disposition: check rules first (fine-grained), then fall back to policy (global).
	// Both resolve to a policy.Decision so the rest of the flow is uniform.
	var decision policy.Decision
	var matchedRuleID *string
	var aiConfidence *float64
	ruleMatched := false
	if s.rules != nil {
		if approvalRules, err := s.rules.ListApprovalRules(ctx); err == nil {
			for _, rule := range matchRules(approvalRules, upstream.Name, req.Tool, agentRec.ID) {
				r := rule
				ruleMatched = true
				if r.ID != "" {
					id := r.ID
					matchedRuleID = &id
				}
				switch r.Action {
				case RuleActionAutoDeny:
					decision = policy.Decision{Disposition: policy.DispositionNever, Reason: "matched approval rule (auto_deny)"}
				case RuleActionAutoApprove:
					decision = policy.Decision{Disposition: policy.DispositionAuto, Reason: "matched approval rule (auto_approve)"}
				case RuleActionAIEvaluation:
					var conf *float64
					decision, conf = s.runAIEvaluation(ctx, &r, upstream.Name, req.Tool, req.Input, agentID, agentRec, "", 0)
					if decision.Disposition != dispositionContinue {
						aiConfidence = conf
					}
				default:
					decision = policy.Decision{Disposition: policy.DispositionHuman, Reason: "matched approval rule (human_approval)"}
				}
				if decision.Disposition != dispositionContinue {
					break
				}
				slog.Info("ai_evaluation: LLM deferred to next rule; continuing rule iteration",
					"rule_id", r.ID, "server", upstream.Name, "tool", req.Tool)
			}
			// If every matching ai_evaluation rule deferred, treat as human approval.
			if ruleMatched && decision.Disposition == dispositionContinue {
				decision = policy.Decision{Disposition: policy.DispositionHuman, Reason: "ai_evaluation: all matching rules deferred; falling back to human_approval"}
			}
		}
	}
	if !ruleMatched && s.policy != nil {
		callCtx := policy.CallContext{AgentID: agentID, Server: upstream.Name, Tool: req.Tool, Input: req.Input}
		var policyErr error
		decision, policyErr = s.policy.Evaluate(ctx, callCtx)
		if policyErr != nil {
			decision = policy.Decision{Disposition: policy.DispositionHuman, Reason: "policy error: " + policyErr.Error()}
		}
	}
	// Persist matched_rule_id for human-approval invocations so approve/deny handlers can reference it.
	// Also tag AI-escalated invocations so the UI can distinguish them from direct human_approval rules.
	if decision.Disposition == policy.DispositionHuman || decision.Disposition == policy.DispositionWorkflow || decision.Disposition == dispositionAIEscalated {
		inv.MatchedRuleID = matchedRuleID
		if decision.Disposition == dispositionAIEscalated {
			inv.Approval = newApproval("ai_escalated", decision.Reason, aiConfidence)
		}
		_ = s.invocations.UpdateResult(ctx, inv)
	}

	receivedPayload := map[string]any{
		"tool": req.Tool, "upstream": upstream.Name,
		"request_id": req.RequestID,
		"input":      json.RawMessage(inv.Input), "arguments": json.RawMessage(inv.Input),
		"disposition": string(decision.Disposition), "disposition_reason": decision.Reason,
	}
	if agentID != "" {
		receivedPayload["agent_id"] = agentID
	}
	_ = s.events.Create(ctx, Event{
		InvocationID: inv.InvocationID,
		EventType:    "invocation.received",
		Payload:      mustJSON(receivedPayload),
		CreatedAt:    now,
	})

	switch decision.Disposition {
	case policy.DispositionNever:
		return s.denyByPolicy(ctx, inv, decision.Reason, aiConfidence)
	case policy.DispositionAuto:
		return s.executeNow(ctx, inv, upstream, req, decision.Reason, aiConfidence)
	default:
		// DispositionHuman, DispositionWorkflow, and dispositionAIEscalated all gate
		// on a human decision. AI-escalated invocations are already tagged on inv.Approval
		// above; waitForHumanApproval will persist the pending_approval status.
		return s.waitForHumanApproval(ctx, inv, upstream, req)
	}
}

// resolveAgentRecord looks up the Atryum agent record for the given runtime
// agentID. Returns a zero-value AgentRecord (with empty ID) when the agent
// cannot be found or when agents lookup is not configured — callers treat an
// empty ID as "match all agents" in rule filtering.
func (s *Service) resolveAgentRecord(ctx context.Context, agentID string) AgentRecord {
	if s.agents == nil {
		return AgentRecord{}
	}
	if agentID != "" {
		rec, err := s.agents.GetByAgentID(ctx, agentID)
		if err == nil {
			return rec
		}
		slog.Warn("could not resolve agent record for runtime agent id; falling back to default agent record if configured",
			"agent_id", agentID, "error", err)
	}
	defaultVMCUID := ""
	if s.syncSettings != nil {
		defaultVMCUID = strings.TrimSpace(s.syncSettings.DefaultAgentVMCUID(ctx))
	}
	if defaultVMCUID == "" {
		return AgentRecord{}
	}
	rec, err := s.agents.GetByVMCUID(ctx, defaultVMCUID)
	if err != nil {
		slog.Warn("could not resolve default agent record",
			"default_agent_vm_cuid", defaultVMCUID, "error", err)
		return AgentRecord{}
	}
	return rec
}

func (s *Service) lookupToolInfo(ctx context.Context, serverName, toolName string) (mcp.Tool, bool) {
	if serverName == "" || toolName == "" || s.resolver == nil || s.client == nil {
		return mcp.Tool{}, false
	}

	s.toolCatalogMu.Lock()
	entry, ok := s.toolCatalog[serverName]
	fresh := ok && time.Since(entry.fetchedAt) < toolCatalogTTL
	s.toolCatalogMu.Unlock()

	if fresh {
		tool, found := entry.tools[toolName]
		return tool, found
	}

	upstream, err := s.resolver.ResolveContext(ctx, serverName)
	if err != nil {
		return mcp.Tool{}, false
	}

	listCtx, cancel := context.WithTimeout(ctx, s.defaultTimeout)
	defer cancel()
	tools, err := s.client.ListTools(listCtx, upstream)
	if err != nil {
		slog.Warn("ai_evaluation: tools/list failed; judge will run without tool description",
			"server", serverName, "tool", toolName, "error", err)
		return mcp.Tool{}, false
	}

	byName := make(map[string]mcp.Tool, len(tools))
	for _, t := range tools {
		byName[t.Name] = t
	}
	s.toolCatalogMu.Lock()
	s.toolCatalog[serverName] = toolCatalogEntry{tools: byName, fetchedAt: time.Now()}
	s.toolCatalogMu.Unlock()

	tool, found := byName[toolName]
	return tool, found
}

func buildEvaluationContext(tool mcp.Tool) string {
	var sb strings.Builder
	desc := strings.TrimSpace(tool.Description)
	if desc != "" {
		sb.WriteString("Tool description: ")
		sb.WriteString(desc)
	}
	if len(tool.InputSchema) > 0 {
		if sb.Len() > 0 {
			sb.WriteString("\n\n")
		}
		sb.WriteString("Tool input schema (JSON Schema):\n")
		sb.Write(tool.InputSchema)
	}
	return sb.String()
}

func combineEvaluationContext(parts ...string) string {
	nonEmpty := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			nonEmpty = append(nonEmpty, trimmed)
		}
	}
	return strings.Join(nonEmpty, "\n\n")
}

// runAIEvaluation dispatches to either the VM backend evaluator (when
// rule.ModelConfigCUID is set) or the local LLM evaluator (when
// rule.AtryumLLMConfigID is set). Falls back to DispositionHuman on any error
// so no invocation is silently lost.
func (s *Service) runAIEvaluation(ctx context.Context, rule *ApprovalRule, serverName, toolName string, toolArgs map[string]any, agentID string, agentRec AgentRecord, chatContext string, chatContextMessages int) (policy.Decision, *float64) {
	if s.evaluator == nil {
		slog.Warn("ai_evaluation rule matched but no evaluator configured; falling back to human_approval",
			"rule_id", rule.ID, "tool", toolName, "server", serverName)
		return policy.Decision{Disposition: policy.DispositionHuman, Reason: "ai_evaluation: evaluator not configured (falling back to human_approval)"}, nil
	}
	debugf("ai_evaluation judge context rule_id=%s server=%s tool=%s agent_id=%s chat_messages=%d has_chat_context=%t",
		rule.ID, serverName, toolName, agentID, chatContextMessages, strings.TrimSpace(chatContext) != "")

	evalContext := ""
	if tool, ok := s.lookupToolInfo(ctx, serverName, toolName); ok {
		evalContext = buildEvaluationContext(tool)
	}
	evalContext = combineEvaluationContext(evalContext, chatContext)

	// --- Local LLM path ---
	if rule.AtryumLLMConfigID != "" {
		if agentRec.Charter == "" {
			slog.Error("ai_evaluation (local): agent has no charter configured; denying tool call",
				"rule_id", rule.ID, "server", serverName, "tool", toolName, "agent_id", agentID)
			return policy.Decision{
				Disposition: policy.DispositionNever,
				Reason:      "ai_evaluation denied: no charter configured for this agent",
			}, nil
		}

		slog.Info("ai_evaluation: calling local LLM",
			"rule_id", rule.ID,
			"server", serverName,
			"tool", toolName,
			"agent_id", agentID,
			"atryum_llm_config_id", rule.AtryumLLMConfigID,
		)

		evalCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		defer cancel()

		resp, err := s.evaluator.EvaluateToolCall(evalCtx, EvaluateRequest{
			AtryumLLMConfigID: rule.AtryumLLMConfigID,
			Charter:           agentRec.Charter,
			ServerName:        serverName,
			ToolName:          toolName,
			ToolArgs:          toolArgs,
			Context:           evalContext,
		})
		if err != nil {
			slog.Error("ai_evaluation (local): LLM call failed; falling back to human_approval",
				"rule_id", rule.ID, "tool", toolName, "error", err)
			return policy.Decision{Disposition: policy.DispositionHuman, Reason: "ai_evaluation: local LLM call failed (falling back to human_approval)"}, nil
		}

		slog.Info("ai_evaluation (local): result",
			"verdict", resp.Verdict,
			"rule_id", rule.ID,
			"server", serverName,
			"tool", toolName,
			"agent_id", agentID,
			"reason", resp.Reason,
			"confidence", resp.Confidence,
		)

		return toDecision(resp), resp.Confidence
	}

	// --- VM backend path ---
	agentVMCUID := agentRec.VMCUID
	orgCUID := agentRec.VMOrganizationCUID

	charterFieldKey := ""
	if s.syncSettings != nil {
		charterFieldKey = s.syncSettings.CharterFieldKey(ctx)
	}

	if agentVMCUID == "" || charterFieldKey == "" {
		slog.Error("ai_evaluation: missing agent or charter context; denying tool call",
			"rule_id", rule.ID,
			"server", serverName,
			"tool", toolName,
			"agent_id", agentID,
			"agent_vm_cuid", agentVMCUID,
			"charter_field_key", charterFieldKey,
		)
		return policy.Decision{
			Disposition: policy.DispositionNever,
			Reason:      "ai_evaluation denied: no charter available for this agent",
		}, nil
	}

	slog.Info("ai_evaluation: calling VM LLM",
		"rule_id", rule.ID,
		"server", serverName,
		"tool", toolName,
		"agent_id", agentID,
		"agent_vm_cuid", agentVMCUID,
		"org_cuid", orgCUID,
		"model_config_cuid", rule.ModelConfigCUID,
		"charter_field_key", charterFieldKey,
	)

	evalCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	resp, err := s.evaluator.EvaluateToolCall(evalCtx, EvaluateRequest{
		ModelConfigCUID: rule.ModelConfigCUID,
		OrgCUID:         orgCUID,
		AgentVMCUID:     agentVMCUID,
		CharterFieldKey: charterFieldKey,
		ServerName:      serverName,
		ToolName:        toolName,
		ToolArgs:        toolArgs,
		Context:         evalContext,
	})
	if err != nil {
		slog.Error("ai_evaluation: LLM evaluation failed; falling back to human_approval",
			"rule_id", rule.ID, "tool", toolName, "error", err)
		return policy.Decision{Disposition: policy.DispositionHuman, Reason: "ai_evaluation: LLM call failed (falling back to human_approval)"}, nil
	}

	slog.Info("ai_evaluation (vm): result",
		"verdict", resp.Verdict,
		"rule_id", rule.ID,
		"server", serverName,
		"tool", toolName,
		"agent_id", agentID,
		"reason", resp.Reason,
		"confidence", resp.Confidence,
	)

	return toDecision(resp), resp.Confidence
}

// toDecision converts an EvaluateResponse verdict into a policy.Decision.
func toDecision(resp EvaluateResponse) policy.Decision {
	switch resp.Verdict {
	case "approved":
		return policy.Decision{Disposition: policy.DispositionAuto, Reason: "ai_evaluation approved: " + resp.Reason}
	case "denied":
		return policy.Decision{Disposition: policy.DispositionNever, Reason: "ai_evaluation denied: " + resp.Reason}
	case "human_approval":
		return policy.Decision{Disposition: dispositionAIEscalated, Reason: "ai_evaluation requires human approval: " + resp.Reason}
	default: // "next_rule" or any unrecognised value
		return policy.Decision{Disposition: dispositionContinue, Reason: "ai_evaluation deferred to next rule: " + resp.Reason}
	}
}

func debugf(format string, args ...any) {
	if !debugEnabled() {
		return
	}
	log.Printf("[mcp] "+format, args...)
}

func debugEnabled() bool {
	value := os.Getenv("ATRYUM_MCP_DEBUG")
	return strings.EqualFold(value, "1") || strings.EqualFold(value, "true")
}

func countChatContextMessages(chatContext string) int {
	count := 0
	for _, line := range strings.Split(chatContext, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "- ") {
			count++
		}
	}
	return count
}

// denyByPolicy hard-denies the call without execution.
func (s *Service) denyByPolicy(ctx context.Context, inv Invocation, reason string, confidence *float64) (InvocationResponse, error) {
	completed := time.Now().UTC()
	inv.Status = StatusDenied
	inv.CompletedAt = &completed
	inv.Approval = newApproval("auto_denied", reason, confidence)
	msg := "Tool call hard-denied by policy."
	if reason != "" {
		msg += " Reason: " + reason
	}
	inv.Error = mustJSON(map[string]any{"content": []map[string]any{{"type": "text", "text": msg}}, "isError": true})
	_ = s.invocations.UpdateResult(context.Background(), inv)
	_ = s.events.Create(context.Background(), Event{
		InvocationID: inv.InvocationID,
		EventType:    "invocation.denied",
		Payload:      mustJSON(map[string]any{"reason": reason, "disposition": "never"}),
		CreatedAt:    completed,
	})
	return s.toResponse(inv), nil
}

// executeNow runs the tool call immediately without waiting for human approval.
func (s *Service) executeNow(ctx context.Context, inv Invocation, upstream mcp.Upstream, req CreateInvocationRequest, reason string, confidence *float64) (InvocationResponse, error) {
	inv.Status = StatusExecuting
	inv.Approval = newApproval("auto_approved", reason, confidence)
	if err := s.invocations.UpdateResult(ctx, inv); err != nil {
		return InvocationResponse{}, err
	}
	_ = s.events.Create(ctx, Event{
		InvocationID: inv.InvocationID,
		EventType:    "invocation.executing",
		Payload: mustJSON(map[string]any{
			"upstream": upstream.Name, "request_id": req.RequestID,
			"input": json.RawMessage(inv.Input), "arguments": json.RawMessage(inv.Input),
			"auto_approved": true, "auto_reason": reason,
		}),
		CreatedAt: time.Now().UTC(),
	})
	return s.finishExecution(ctx, inv, upstream, req)
}

// waitForHumanApproval blocks until an operator approves or denies, or the context is cancelled.
func (s *Service) waitForHumanApproval(ctx context.Context, inv Invocation, upstream mcp.Upstream, req CreateInvocationRequest) (InvocationResponse, error) {
	ch := make(chan approvalDecision, 1)
	s.mu.Lock()
	s.pendingApprovals[inv.InvocationID] = ch
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.pendingApprovals, inv.InvocationID)
		s.mu.Unlock()
	}()

	inv.Status = StatusPendingApproval
	if err := s.invocations.UpdateResult(ctx, inv); err != nil {
		return InvocationResponse{}, err
	}
	_ = s.events.Create(ctx, Event{
		InvocationID: inv.InvocationID,
		EventType:    "invocation.pending_approval",
		Payload: mustJSON(map[string]any{
			"tool": req.Tool, "upstream": upstream.Name,
			"request_id": req.RequestID,
			"input":      json.RawMessage(inv.Input), "arguments": json.RawMessage(inv.Input),
		}),
		CreatedAt: time.Now().UTC(),
	})
	s.summarizePendingApproval(inv.InvocationID)

	select {
	case d := <-ch:
		if !d.approved {
			completed := time.Now().UTC()
			inv.Status = StatusDenied
			inv.CompletedAt = &completed
			inv.Approval = &Approval{Status: humanDecisionStatus(inv.Approval, false), Reason: stringPtr("human")}
			msg := "Tool call denied by the MCP permissions system."
			if d.message != "" {
				msg += "\n\nReason: " + d.message
			}
			inv.Error = mustJSON(map[string]any{"content": []map[string]any{{"type": "text", "text": msg}}, "isError": true})
			_ = s.invocations.UpdateResult(context.Background(), inv)
			_ = s.events.Create(context.Background(), Event{
				InvocationID: inv.InvocationID,
				EventType:    "invocation.denied",
				Payload:      mustJSON(map[string]any{"message": d.message, "input": json.RawMessage(inv.Input), "arguments": json.RawMessage(inv.Input)}),
				CreatedAt:    completed,
			})
			return s.toResponse(inv), nil
		}
	case <-ctx.Done():
		completed := time.Now().UTC()
		inv.Status = StatusFailed
		inv.CompletedAt = &completed
		inv.Error = mustJSON(map[string]any{"content": []map[string]any{{"type": "text", "text": "Tool call cancelled: approval timed out or connection closed."}}, "isError": true})
		_ = s.invocations.UpdateResult(context.Background(), inv)
		_ = s.events.Create(context.Background(), Event{InvocationID: inv.InvocationID, EventType: "invocation.failed", Payload: inv.Error, CreatedAt: completed})
		return s.toResponse(inv), nil
	}

	inv.Status = StatusExecuting
	inv.Approval = &Approval{Status: humanDecisionStatus(inv.Approval, true), Reason: stringPtr("human")}
	if err := s.invocations.UpdateResult(ctx, inv); err != nil {
		return InvocationResponse{}, err
	}
	_ = s.events.Create(ctx, Event{
		InvocationID: inv.InvocationID,
		EventType:    "invocation.executing",
		Payload: mustJSON(map[string]any{
			"upstream": upstream.Name, "request_id": req.RequestID,
			"input": json.RawMessage(inv.Input), "arguments": json.RawMessage(inv.Input),
		}),
		CreatedAt: time.Now().UTC(),
	})
	return s.finishExecution(ctx, inv, upstream, req)
}

// finishExecution calls the upstream client and persists the outcome.
func (s *Service) finishExecution(ctx context.Context, inv Invocation, upstream mcp.Upstream, req CreateInvocationRequest) (InvocationResponse, error) {
	execCtx, cancel := context.WithTimeout(ctx, s.defaultTimeout)
	defer cancel()
	result, err := s.client.Invoke(execCtx, upstream, req.Tool, req.Input, req.RequestID)
	completed := time.Now().UTC()
	inv.CompletedAt = &completed
	if err != nil {
		inv.Status = StatusFailed
		inv.Error = mustJSON(map[string]any{"message": err.Error()})
		_ = s.invocations.UpdateResult(ctx, inv)
		_ = s.events.Create(ctx, Event{InvocationID: inv.InvocationID, EventType: "invocation.failed", Payload: inv.Error, CreatedAt: completed})
		return s.toResponse(inv), nil
	}
	if result.Failed {
		inv.Status = StatusFailed
		inv.Error = result.Body
		_ = s.events.Create(ctx, Event{
			InvocationID: inv.InvocationID, EventType: "invocation.failed",
			Payload:   mustJSON(map[string]any{"request_id": req.RequestID, "input": json.RawMessage(inv.Input), "arguments": json.RawMessage(inv.Input), "body": json.RawMessage(result.Body)}),
			CreatedAt: completed,
		})
	} else {
		inv.Status = StatusSucceeded
		inv.Response = result.Body
		_ = s.events.Create(ctx, Event{
			InvocationID: inv.InvocationID, EventType: "invocation.succeeded",
			Payload:   mustJSON(map[string]any{"request_id": req.RequestID, "input": json.RawMessage(inv.Input), "arguments": json.RawMessage(inv.Input), "body": json.RawMessage(result.Body)}),
			CreatedAt: completed,
		})
	}
	if err := s.invocations.UpdateResult(ctx, inv); err != nil {
		return InvocationResponse{}, err
	}
	return s.toResponse(inv), nil
}

func (s *Service) Approve(ctx context.Context, invocationID string) error {
	s.mu.Lock()
	ch, ok := s.pendingApprovals[invocationID]
	s.mu.Unlock()
	if ok {
		select {
		case ch <- approvalDecision{approved: true}:
			return nil
		default:
			return fmt.Errorf("invocation %s approval already decided", invocationID)
		}
	}
	// External (mediated) invocation: no in-memory waiter. Update DB directly
	// so the polling external executor can pick up the decision.
	return s.recordExternalDecision(ctx, invocationID, true, "")
}

func (s *Service) Deny(ctx context.Context, invocationID string, message string) error {
	s.mu.Lock()
	ch, ok := s.pendingApprovals[invocationID]
	s.mu.Unlock()
	if ok {
		select {
		case ch <- approvalDecision{approved: false, message: message}:
			return nil
		default:
			return fmt.Errorf("invocation %s approval already decided", invocationID)
		}
	}
	return s.recordExternalDecision(ctx, invocationID, false, message)
}

func (s *Service) recordExternalDecision(ctx context.Context, invocationID string, approved bool, message string) error {
	inv, err := s.invocations.Get(ctx, invocationID)
	if err != nil {
		return err
	}
	if inv.Status != StatusPendingApproval {
		return fmt.Errorf("invocation %s is not pending approval (status=%s)", invocationID, inv.Status)
	}
	now := time.Now().UTC()
	if approved {
		inv.Status = StatusApproved
		inv.Approval = &Approval{Status: humanDecisionStatus(inv.Approval, true), Reason: stringPtr("human")}
		if err := s.invocations.UpdateResult(ctx, inv); err != nil {
			return err
		}
		_ = s.events.Create(ctx, Event{InvocationID: inv.InvocationID, EventType: "invocation.approved", Payload: mustJSON(map[string]any{"input": json.RawMessage(inv.Input), "arguments": json.RawMessage(inv.Input)}), CreatedAt: now})
		return nil
	}
	inv.Status = StatusDenied
	inv.CompletedAt = &now
	inv.Approval = &Approval{Status: humanDecisionStatus(inv.Approval, false), Reason: stringPtr("human")}
	msg := "Tool call denied by the MCP permissions system."
	if message != "" {
		msg += "\n\nReason: " + message
	}
	inv.Error = mustJSON(map[string]any{"content": []map[string]any{{"type": "text", "text": msg}}, "isError": true})
	if err := s.invocations.UpdateResult(ctx, inv); err != nil {
		return err
	}
	_ = s.events.Create(ctx, Event{InvocationID: inv.InvocationID, EventType: "invocation.denied", Payload: mustJSON(map[string]any{"message": message, "input": json.RawMessage(inv.Input), "arguments": json.RawMessage(inv.Input)}), CreatedAt: now})
	return nil
}

// Submit creates an invocation on behalf of an external executor (e.g. an amp
// coding harness plugin). It does NOT execute the tool — the caller is expected
// to poll Get() until status is approved or denied, run the tool itself, and
// then RecordExecution() with the outcome.
//
// Rules are evaluated against the source (as the "server") and tool name.
// If a rule matches auto_approve, the invocation is immediately approved.
// If a rule matches auto_deny, it is immediately denied.
// Otherwise, the invocation enters pending_approval for human review.
func (s *Service) Submit(ctx context.Context, req ExternalSubmitRequest) (InvocationResponse, error) {
	if req.Tool == "" {
		return InvocationResponse{}, fmt.Errorf("tool is required")
	}
	chatContext := req.ChatContext
	if chatContext == "" {
		chatContext = req.Context
	}
	chatContextMessages := req.ChatContextMessages
	if chatContextMessages <= 0 && chatContext != "" {
		chatContextMessages = countChatContextMessages(chatContext)
	}
	source := req.Source
	if source == "" {
		source = "external"
	}
	if req.IdempotencyKey != nil && *req.IdempotencyKey != "" {
		existing, err := s.invocations.GetByIdempotencyKey(ctx, *req.IdempotencyKey)
		if err == nil {
			return s.toResponse(existing), nil
		}
		if err != nil && err != sql.ErrNoRows {
			return InvocationResponse{}, err
		}
	}
	inputJSON, err := json.Marshal(req.Input)
	if err != nil {
		return InvocationResponse{}, err
	}
	// Evaluate rules against (source, tool, user) — same logic as Invoke.
	// Verified OAuth identity (when the external route runs behind auth
	// middleware) wins. Otherwise fall back to the self-declared agent_id
	// in the body — useful for plugin-style callers that aren't doing
	// OAuth but still want their invocations tagged & matched to an
	// Agent Record via agents.agent_ids.
	agentID := auth.AgentIDFromContext(ctx)
	if agentID == "" {
		agentID = strings.TrimSpace(req.AgentID)
	}
	agentRec := s.resolveAgentRecord(ctx, agentID)

	now := time.Now().UTC()
	inv := Invocation{
		InvocationID:   "inv_" + uuid.NewString(),
		RequestID:      req.RequestID,
		IdempotencyKey: req.IdempotencyKey,
		Tool:           req.Tool,
		// Upstream is intentionally left empty for external/non-MCP
		// invocations — the harness ran the tool itself, there is no
		// MCP server in the loop. The UI renders an empty server as
		// "none". The harness identity is recorded as ClientName below
		// so it shows up in the Agent column instead.
		Upstream:    "",
		Status:      StatusReceived,
		Input:       inputJSON,
		SubmittedAt: now,
	}
	// Prefer explicit ClientName/ClientVersion from the request; fall back
	// to Source when the caller didn't tell us anything more specific.
	if cn := req.ClientName; cn != "" {
		v := cn
		inv.ClientName = &v
	} else if source != "" && source != "external" {
		s := source
		inv.ClientName = &s
	}
	if cv := req.ClientVersion; cv != "" {
		v := cv
		inv.ClientVersion = &v
	}
	if agentID != "" {
		inv.AgentID = &agentID
	}
	if err := s.invocations.Create(ctx, inv); err != nil {
		return InvocationResponse{}, err
	}
	ruleAction := ""
	var matchedRuleID *string
	var resolvedAIDecision *policy.Decision
	var resolvedAIConfidence *float64
	if s.rules != nil {
		if approvalRules, err := s.rules.ListApprovalRules(ctx); err == nil {
			for _, rule := range matchRules(approvalRules, source, req.Tool, agentRec.ID) {
				r := rule
				if r.ID != "" {
					id := r.ID
					matchedRuleID = &id
				}
				if r.Action == RuleActionAIEvaluation {
					d, conf := s.runAIEvaluation(ctx, &r, source, req.Tool, req.Input, agentID, agentRec, chatContext, chatContextMessages)
					if d.Disposition == dispositionContinue {
						matchedRuleID = nil
						slog.Info("ai_evaluation: LLM deferred to next rule; continuing rule iteration",
							"rule_id", r.ID, "server", source, "tool", req.Tool)
						continue
					}
					ruleAction = r.Action
					resolvedAIDecision = &d
					resolvedAIConfidence = conf
					break
				}
				ruleAction = r.Action
				break
			}
		}
	}
	inv.MatchedRuleID = matchedRuleID

	receivedPayload := map[string]any{"tool": req.Tool, "upstream": source, "request_id": req.RequestID, "input": json.RawMessage(inv.Input), "arguments": json.RawMessage(inv.Input), "external": true}
	if agentID != "" {
		receivedPayload["agent_id"] = agentID
	}
	if req.Description != "" {
		receivedPayload["description"] = req.Description
	}
	if req.ThreadID != "" {
		receivedPayload["thread_id"] = req.ThreadID
	}
	if ruleAction != "" {
		receivedPayload["disposition"] = ruleAction
	}
	_ = s.events.Create(ctx, Event{InvocationID: inv.InvocationID, EventType: "invocation.received", Payload: mustJSON(receivedPayload), CreatedAt: now})

	switch ruleAction {
	case RuleActionAutoDeny:
		completed := time.Now().UTC()
		inv.Status = StatusDenied
		inv.CompletedAt = &completed
		inv.Approval = &Approval{Status: "auto_denied", Reason: stringPtr("matched approval rule (auto_deny)")}
		inv.Error = mustJSON(map[string]any{"content": []map[string]any{{"type": "text", "text": "Tool call denied by approval rule (auto_deny)."}}, "isError": true})
		_ = s.invocations.UpdateResult(context.Background(), inv)
		_ = s.events.Create(context.Background(), Event{InvocationID: inv.InvocationID, EventType: "invocation.denied", Payload: mustJSON(map[string]any{"reason": "matched approval rule (auto_deny)", "disposition": "never"}), CreatedAt: completed})
		return s.toResponse(inv), nil

	case RuleActionAutoApprove:
		inv.Status = StatusApproved
		inv.Approval = &Approval{Status: "auto_approved", Reason: stringPtr("matched approval rule (auto_approve)")}
		if err := s.invocations.UpdateResult(ctx, inv); err != nil {
			return InvocationResponse{}, err
		}
		_ = s.events.Create(ctx, Event{InvocationID: inv.InvocationID, EventType: "invocation.approved", Payload: mustJSON(map[string]any{"auto_approved": true, "auto_reason": "matched approval rule (auto_approve)"}), CreatedAt: time.Now().UTC()})
		return s.toResponse(inv), nil

	case RuleActionAIEvaluation:
		// resolvedAIDecision is always populated when ruleAction == RuleActionAIEvaluation
		// because runAIEvaluation is called during rule iteration above (never twice).
		var aiDecision policy.Decision
		if resolvedAIDecision != nil {
			aiDecision = *resolvedAIDecision
		} else {
			aiDecision = policy.Decision{Disposition: policy.DispositionHuman, Reason: "ai_evaluation: decision unavailable; falling back to human_approval"}
		}
		if aiDecision.Disposition == policy.DispositionAuto {
			inv.Status = StatusApproved
			inv.Approval = newApproval("auto_approved", aiDecision.Reason, resolvedAIConfidence)
			if err := s.invocations.UpdateResult(ctx, inv); err != nil {
				return InvocationResponse{}, err
			}
			_ = s.events.Create(ctx, Event{InvocationID: inv.InvocationID, EventType: "invocation.approved", Payload: mustJSON(map[string]any{"auto_approved": true, "auto_reason": aiDecision.Reason}), CreatedAt: time.Now().UTC()})
			return s.toResponse(inv), nil
		}
		if aiDecision.Disposition == policy.DispositionNever {
			completed := time.Now().UTC()
			inv.Status = StatusDenied
			inv.CompletedAt = &completed
			inv.Approval = newApproval("auto_denied", aiDecision.Reason, resolvedAIConfidence)
			inv.Error = mustJSON(map[string]any{"content": []map[string]any{{"type": "text", "text": "Tool call denied by AI evaluation. Reason: " + aiDecision.Reason}}, "isError": true})
			_ = s.invocations.UpdateResult(context.Background(), inv)
			_ = s.events.Create(context.Background(), Event{InvocationID: inv.InvocationID, EventType: "invocation.denied", Payload: mustJSON(map[string]any{"reason": aiDecision.Reason, "disposition": "never"}), CreatedAt: completed})
			return s.toResponse(inv), nil
		}
		// Tag AI-escalated invocations before falling through to human approval so the
		// UI can distinguish them from direct human_approval rules.
		if aiDecision.Disposition == dispositionAIEscalated {
			inv.Approval = newApproval("ai_escalated", aiDecision.Reason, resolvedAIConfidence)
		}
		// Fall through to human approval for human_approval verdict or unexpected fallback.
	}

	// Default: human approval required.
	inv.Status = StatusPendingApproval
	if err := s.invocations.UpdateResult(ctx, inv); err != nil {
		return InvocationResponse{}, err
	}
	_ = s.events.Create(ctx, Event{InvocationID: inv.InvocationID, EventType: "invocation.pending_approval", Payload: mustJSON(receivedPayload), CreatedAt: time.Now().UTC()})
	s.summarizePendingApproval(inv.InvocationID)
	return s.toResponse(inv), nil
}

func (s *Service) summarizePendingApproval(invocationID string) {
	if s.summarizer == nil || invocationID == "" {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()

		orgCUID := ""
		modelConfigCUID := ""
		if s.syncSettings != nil {
			orgCUID, modelConfigCUID = s.syncSettings.SummarySettings(ctx)
			orgCUID = strings.TrimSpace(orgCUID)
			modelConfigCUID = strings.TrimSpace(modelConfigCUID)
		}
		if modelConfigCUID == "" {
			slog.Debug("invocation summary skipped: summary model config is not set", "invocation_id", invocationID)
			return
		}

		inv, err := s.invocations.Get(ctx, invocationID)
		if err != nil {
			slog.Warn("invocation summary skipped: load invocation failed", "invocation_id", invocationID, "error", err)
			return
		}
		if inv.Summary != nil && *inv.Summary != "" {
			return
		}

		raw, err := json.Marshal(s.toResponse(inv))
		if err != nil {
			slog.Warn("invocation summary skipped: encode invocation failed", "invocation_id", invocationID, "error", err)
			return
		}
		var invMap map[string]any
		if err := json.Unmarshal(raw, &invMap); err != nil {
			slog.Warn("invocation summary skipped: normalize invocation failed", "invocation_id", invocationID, "error", err)
			return
		}

		resp, err := s.summarizer.SummarizeInvocation(ctx, SummaryRequest{
			ModelConfigCUID: modelConfigCUID,
			OrgCUID:         orgCUID,
			Invocation:      invMap,
		})
		if err != nil {
			slog.Warn("invocation summary failed", "invocation_id", invocationID, "model_config_cuid", modelConfigCUID, "error", err)
			return
		}
		if _, err := s.SetSummary(ctx, invocationID, resp.Summary); err != nil {
			slog.Warn("invocation summary persist failed", "invocation_id", invocationID, "error", err)
		}
	}()
}

// RecordExecution updates an externally-executed invocation with the outcome
// reported by the executor. Valid execStatus values:
//
//	running | completed | failed | cancelled
func (s *Service) RecordExecution(ctx context.Context, invocationID string, update ExternalExecutionUpdate) (InvocationResponse, error) {
	inv, err := s.invocations.Get(ctx, invocationID)
	if err != nil {
		return InvocationResponse{}, err
	}
	now := time.Now().UTC()
	switch update.ExecutionStatus {
	case "running":
		if inv.Status != StatusApproved && inv.Status != StatusExecuting {
			return InvocationResponse{}, fmt.Errorf("invocation %s cannot move to running from %s", invocationID, inv.Status)
		}
		inv.Status = StatusExecuting
		if err := s.invocations.UpdateResult(ctx, inv); err != nil {
			return InvocationResponse{}, err
		}
		_ = s.events.Create(ctx, Event{InvocationID: inv.InvocationID, EventType: "invocation.executing", Payload: mustJSON(map[string]any{"upstream": inv.Upstream, "input": json.RawMessage(inv.Input), "arguments": json.RawMessage(inv.Input), "external": true}), CreatedAt: now})
	case "completed":
		inv.Status = StatusSucceeded
		inv.CompletedAt = &now
		if len(update.Result) > 0 {
			inv.Response = []byte(update.Result)
		}
		if err := s.invocations.UpdateResult(ctx, inv); err != nil {
			return InvocationResponse{}, err
		}
		_ = s.events.Create(ctx, Event{InvocationID: inv.InvocationID, EventType: "invocation.succeeded", Payload: mustJSON(map[string]any{"upstream": inv.Upstream, "input": json.RawMessage(inv.Input), "arguments": json.RawMessage(inv.Input), "body": json.RawMessage(nullableRaw(inv.Response)), "external": true}), CreatedAt: now})
	case "failed":
		inv.Status = StatusFailed
		inv.CompletedAt = &now
		if len(update.Error) > 0 {
			inv.Error = []byte(update.Error)
		} else if update.Message != "" {
			inv.Error = mustJSON(map[string]any{"message": update.Message})
		} else {
			inv.Error = mustJSON(map[string]any{"message": "tool execution failed"})
		}
		if err := s.invocations.UpdateResult(ctx, inv); err != nil {
			return InvocationResponse{}, err
		}
		_ = s.events.Create(ctx, Event{InvocationID: inv.InvocationID, EventType: "invocation.failed", Payload: mustJSON(map[string]any{"upstream": inv.Upstream, "input": json.RawMessage(inv.Input), "arguments": json.RawMessage(inv.Input), "body": json.RawMessage(inv.Error), "external": true}), CreatedAt: now})
	case "cancelled":
		inv.Status = StatusCancelled
		inv.CompletedAt = &now
		if len(update.Error) > 0 {
			inv.Error = []byte(update.Error)
		} else {
			msg := "Tool execution cancelled."
			if update.Message != "" {
				msg = update.Message
			}
			inv.Error = mustJSON(map[string]any{"content": []map[string]any{{"type": "text", "text": msg}}, "isError": true})
		}
		if err := s.invocations.UpdateResult(ctx, inv); err != nil {
			return InvocationResponse{}, err
		}
		_ = s.events.Create(ctx, Event{InvocationID: inv.InvocationID, EventType: "invocation.cancelled", Payload: mustJSON(map[string]any{"upstream": inv.Upstream, "input": json.RawMessage(inv.Input), "arguments": json.RawMessage(inv.Input), "body": json.RawMessage(inv.Error), "external": true}), CreatedAt: now})
	default:
		return InvocationResponse{}, fmt.Errorf("invalid execution_status %q (expected running|completed|failed|cancelled)", update.ExecutionStatus)
	}
	return s.toResponse(inv), nil
}

func nullableRaw(b []byte) []byte {
	if len(b) == 0 {
		return []byte("null")
	}
	return b
}

func (s *Service) ListTools(ctx context.Context, server string) ([]mcp.Tool, error) {
	if server == "" {
		return nil, fmt.Errorf("server is required")
	}
	upstream, err := s.resolver.ResolveContext(ctx, server)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, s.defaultTimeout)
	defer cancel()
	tools, err := s.client.ListTools(ctx, upstream)
	if err != nil {
		return nil, err
	}
	return tools, nil
}

func (s *Service) Get(ctx context.Context, id string) (InvocationResponse, error) {
	inv, err := s.invocations.Get(ctx, id)
	if err != nil {
		return InvocationResponse{}, err
	}
	resp := s.toResponse(inv)
	return resp, nil
}

func (s *Service) ListAgentIDs(ctx context.Context) ([]string, error) {
	return s.invocations.ListAgentIDs(ctx)
}

func (s *Service) List(ctx context.Context, filter InvocationListFilter) (InvocationListResponse, error) {
	invocations, total, err := s.invocations.List(ctx, filter)
	if err != nil {
		return InvocationListResponse{}, err
	}
	out := make([]InvocationResponse, 0, len(invocations))
	for _, inv := range invocations {
		out = append(out, s.toResponse(inv))
	}
	return InvocationListResponse{Items: out, Total: total, Offset: filter.Offset, Limit: normalizedLimit(filter.Limit, 50)}, nil
}

func (s *Service) Events(ctx context.Context, invocationID string, filter EventListFilter) (EventListResponse, error) {
	events, total, err := s.events.ListByInvocation(ctx, invocationID, filter)
	if err != nil {
		return EventListResponse{}, err
	}
	out := make([]EventResponse, 0, len(events))
	for _, evt := range events {
		out = append(out, EventResponse{Type: evt.EventType, Timestamp: evt.CreatedAt, Data: evt.Payload})
	}
	return EventListResponse{Items: out, Total: total, Offset: filter.Offset, Limit: normalizedLimit(filter.Limit, 200)}, nil
}

// SetSummary persists an LLM-generated summary for the invocation and records
// a lifecycle event. Callers (handlers) are responsible for producing the
// summary text; the service simply stores it and surfaces it via Get/List.
func (s *Service) SetSummary(ctx context.Context, invocationID string, summary string) (InvocationResponse, error) {
	if invocationID == "" {
		return InvocationResponse{}, fmt.Errorf("invocation_id is required")
	}
	if err := s.invocations.UpdateSummary(ctx, invocationID, summary); err != nil {
		return InvocationResponse{}, err
	}
	inv, err := s.invocations.Get(ctx, invocationID)
	if err != nil {
		return InvocationResponse{}, err
	}
	_ = s.events.Create(ctx, Event{
		InvocationID: invocationID,
		EventType:    "invocation.summarized",
		Payload:      mustJSON(map[string]any{"summary": summary}),
		CreatedAt:    time.Now().UTC(),
	})
	return s.toResponse(inv), nil
}

func (s *Service) toResponse(inv Invocation) InvocationResponse {
	resp := InvocationResponse{InvocationID: inv.InvocationID, ServerName: inv.Upstream, ToolName: inv.Tool, Status: inv.Status, Approval: inv.Approval, MatchedRuleID: inv.MatchedRuleID, AgentID: inv.AgentID, RequestID: inv.RequestID, SubmittedAt: inv.SubmittedAt, CompletedAt: inv.CompletedAt}
	if inv.Summary != nil {
		resp.Summary = *inv.Summary
	}
	if inv.ClientName != nil {
		resp.AgentClientName = inv.ClientName
	}
	if inv.ClientVersion != nil {
		resp.AgentClientVersion = inv.ClientVersion
	}
	if len(inv.Input) > 0 {
		resp.Input = json.RawMessage(inv.Input)
	}
	if len(inv.Response) > 0 {
		resp.Result = json.RawMessage(inv.Response)
	}
	if len(inv.Error) > 0 {
		resp.Error = json.RawMessage(inv.Error)
	}
	return resp
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func stringPtr(v string) *string { return &v }

func normalizedLimit(limit uint64, fallback uint64) uint64 {
	if limit == 0 {
		return fallback
	}
	return limit
}
