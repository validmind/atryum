package invocation

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	"atryum/internal/auth"
	"atryum/internal/invocation/policy"
	"atryum/internal/mcp"
)

type approvalDecision struct {
	approved bool
	message  string
}

type invocationRepo interface {
	Create(ctx context.Context, inv Invocation) error
	UpdateResult(ctx context.Context, inv Invocation) error
	Get(ctx context.Context, id string) (Invocation, error)
	GetByIdempotencyKey(ctx context.Context, key string) (Invocation, error)
	List(ctx context.Context, filter InvocationListFilter) ([]Invocation, int, error)
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

type Service struct {
	invocations      invocationRepo
	events           eventRepo
	resolver         resolver
	client           upstreamClient
	policy           policy.Provider
	rules            rulesStore // nil = no rule evaluation
	defaultTimeout   time.Duration
	mu               sync.Mutex
	pendingApprovals map[string]chan approvalDecision
}

func NewService(inv invocationRepo, evt eventRepo, resolver resolver, client upstreamClient, policyProvider policy.Provider, defaultTimeout time.Duration, rules rulesStore) *Service {
	return &Service{
		invocations:      inv,
		events:           evt,
		resolver:         resolver,
		client:           client,
		policy:           policyProvider,
		rules:            rules,
		defaultTimeout:   defaultTimeout,
		pendingApprovals: make(map[string]chan approvalDecision),
	}
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
	if err := s.invocations.Create(ctx, inv); err != nil {
		return InvocationResponse{}, err
	}

	// agentID is the authenticated agent identity from middleware. When auth
	// is disabled the field is empty and we fall back to request_id for rule
	// matching to preserve pre-auth behavior.
	agentID := auth.AgentIDFromContext(ctx)

	// Determine disposition: check rules first (fine-grained), then fall back to policy (global).
	// Both resolve to a policy.Decision so the rest of the flow is uniform.
	var decision policy.Decision
	var matchedRuleID *string
	ruleMatched := false
	if s.rules != nil {
		if approvalRules, err := s.rules.ListApprovalRules(ctx); err == nil {
			user := agentID
			if user == "" && req.RequestID != nil {
				user = *req.RequestID
			}
			if matched := matchRule(approvalRules, upstream.Name, req.Tool, user); matched != nil {
				ruleMatched = true
				if matched.ID != "" {
					matchedRuleID = &matched.ID
				}
				switch matched.Action {
				case RuleActionAutoDeny:
					decision = policy.Decision{Disposition: policy.DispositionNever, Reason: "matched approval rule (auto_deny)"}
				case RuleActionAutoApprove:
					decision = policy.Decision{Disposition: policy.DispositionAuto, Reason: "matched approval rule (auto_approve)"}
				default:
					decision = policy.Decision{Disposition: policy.DispositionHuman, Reason: "matched approval rule (human_approval)"}
				}
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
	if decision.Disposition == policy.DispositionHuman || decision.Disposition == policy.DispositionWorkflow {
		inv.MatchedRuleID = matchedRuleID
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
		return s.denyByPolicy(ctx, inv, decision.Reason)
	case policy.DispositionAuto:
		return s.executeNow(ctx, inv, upstream, req, decision.Reason)
	default:
		// DispositionHuman and DispositionWorkflow both gate on a human decision.
		return s.waitForHumanApproval(ctx, inv, upstream, req)
	}
}

// denyByPolicy hard-denies the call without execution.
func (s *Service) denyByPolicy(ctx context.Context, inv Invocation, reason string) (InvocationResponse, error) {
	completed := time.Now().UTC()
	inv.Status = StatusDenied
	inv.CompletedAt = &completed
	inv.Approval = &Approval{Status: "auto_denied", Reason: stringPtr(reason)}
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
func (s *Service) executeNow(ctx context.Context, inv Invocation, upstream mcp.Upstream, req CreateInvocationRequest, reason string) (InvocationResponse, error) {
	inv.Status = StatusExecuting
	inv.Approval = &Approval{Status: "auto_approved", Reason: stringPtr(reason)}
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
			"input": json.RawMessage(inv.Input), "arguments": json.RawMessage(inv.Input),
		}),
		CreatedAt: time.Now().UTC(),
	})

	select {
	case d := <-ch:
		if !d.approved {
			completed := time.Now().UTC()
			inv.Status = StatusDenied
			inv.CompletedAt = &completed
			inv.Approval = &Approval{Status: "denied", Reason: stringPtr("human")}
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
	inv.Approval = &Approval{Status: "approved", Reason: stringPtr("human")}
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
		inv.Approval = &Approval{Status: "approved", Reason: stringPtr("human")}
		if err := s.invocations.UpdateResult(ctx, inv); err != nil {
			return err
		}
		_ = s.events.Create(ctx, Event{InvocationID: inv.InvocationID, EventType: "invocation.approved", Payload: mustJSON(map[string]any{"input": json.RawMessage(inv.Input), "arguments": json.RawMessage(inv.Input)}), CreatedAt: now})
		return nil
	}
	inv.Status = StatusDenied
	inv.CompletedAt = &now
	inv.Approval = &Approval{Status: "denied", Reason: stringPtr("human")}
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
	now := time.Now().UTC()
	inv := Invocation{
		InvocationID:   "inv_" + uuid.NewString(),
		RequestID:      req.RequestID,
		IdempotencyKey: req.IdempotencyKey,
		Tool:           req.Tool,
		Upstream:       source,
		Status:         StatusReceived,
		Input:          inputJSON,
		SubmittedAt:    now,
	}
	if err := s.invocations.Create(ctx, inv); err != nil {
		return InvocationResponse{}, err
	}

	// Evaluate rules against (source, tool, user) — same logic as Invoke.
	agentID := auth.AgentIDFromContext(ctx)
	ruleAction := ""
	if s.rules != nil {
		if approvalRules, err := s.rules.ListApprovalRules(ctx); err == nil {
			user := agentID
			if user == "" && req.RequestID != nil {
				user = *req.RequestID
			}
			if matched := matchRule(approvalRules, source, req.Tool, user); matched != nil {
				ruleAction = matched.Action
			}
		}
	}

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
	}

	// Default: human approval required.
	inv.Status = StatusPendingApproval
	if err := s.invocations.UpdateResult(ctx, inv); err != nil {
		return InvocationResponse{}, err
	}
	_ = s.events.Create(ctx, Event{InvocationID: inv.InvocationID, EventType: "invocation.pending_approval", Payload: mustJSON(receivedPayload), CreatedAt: time.Now().UTC()})
	return s.toResponse(inv), nil
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

// ResolveToolServer finds which upstream server provides the named tool.
// Used in aggregate mode (no server in URL) to route tools/call correctly.
func (s *Service) ResolveToolServer(ctx context.Context, toolName string) (string, error) {
	upstreams, err := s.resolver.ListAll(ctx)
	if err != nil {
		return "", err
	}
	for _, upstream := range upstreams {
		tctx, cancel := context.WithTimeout(ctx, s.defaultTimeout)
		tools, err := s.client.ListTools(tctx, upstream)
		cancel()
		if err != nil {
			continue
		}
		for _, t := range tools {
			if t.Name == toolName {
				return upstream.Name, nil
			}
		}
	}
	return "", fmt.Errorf("no server found for tool %q", toolName)
}

// ListAllTools aggregates tools from every enabled upstream. Used when the MCP
// client connects to the root /mcp endpoint without specifying a server name.
func (s *Service) ListAllTools(ctx context.Context) ([]mcp.Tool, error) {
	upstreams, err := s.resolver.ListAll(ctx)
	if err != nil {
		return nil, err
	}
	var all []mcp.Tool
	for _, upstream := range upstreams {
		tctx, cancel := context.WithTimeout(ctx, s.defaultTimeout)
		tools, err := s.client.ListTools(tctx, upstream)
		cancel()
		if err != nil {
			continue // skip unreachable servers rather than failing the whole list
		}
		all = append(all, tools...)
	}
	return all, nil
}

func (s *Service) Get(ctx context.Context, id string) (InvocationResponse, error) {
	inv, err := s.invocations.Get(ctx, id)
	if err != nil {
		return InvocationResponse{}, err
	}
	return s.toResponse(inv), nil
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

func (s *Service) toResponse(inv Invocation) InvocationResponse {
	resp := InvocationResponse{InvocationID: inv.InvocationID, ServerName: inv.Upstream, ToolName: inv.Tool, Status: inv.Status, Approval: inv.Approval, MatchedRuleID: inv.MatchedRuleID, RequestID: inv.RequestID, SubmittedAt: inv.SubmittedAt, CompletedAt: inv.CompletedAt}
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
