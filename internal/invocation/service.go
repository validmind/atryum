package invocation

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

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

	// Determine disposition: check rules first (fine-grained), then fall back to policy (global).
	// Both resolve to a policy.Decision so the rest of the flow is uniform.
	var decision policy.Decision
	ruleMatched := false
	if s.rules != nil {
		if approvalRules, err := s.rules.ListApprovalRules(ctx); err == nil {
			user := ""
			if req.RequestID != nil {
				user = *req.RequestID
			}
			if matched := matchRule(approvalRules, upstream.Name, req.Tool, user); matched != nil {
				ruleMatched = true
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
		callCtx := policy.CallContext{Server: upstream.Name, Tool: req.Tool, Input: req.Input}
		var policyErr error
		decision, policyErr = s.policy.Evaluate(ctx, callCtx)
		if policyErr != nil {
			decision = policy.Decision{Disposition: policy.DispositionHuman, Reason: "policy error: " + policyErr.Error()}
		}
	}

	_ = s.events.Create(ctx, Event{
		InvocationID: inv.InvocationID,
		EventType:    "invocation.received",
		Payload: mustJSON(map[string]any{
			"tool": req.Tool, "upstream": upstream.Name,
			"request_id": req.RequestID,
			"input": json.RawMessage(inv.Input), "arguments": json.RawMessage(inv.Input),
			"disposition": string(decision.Disposition), "disposition_reason": decision.Reason,
		}),
		CreatedAt: now,
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
	if !ok {
		return fmt.Errorf("invocation %s is not pending approval", invocationID)
	}
	select {
	case ch <- approvalDecision{approved: true}:
		return nil
	default:
		return fmt.Errorf("invocation %s approval already decided", invocationID)
	}
}

func (s *Service) Deny(ctx context.Context, invocationID string, message string) error {
	s.mu.Lock()
	ch, ok := s.pendingApprovals[invocationID]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("invocation %s is not pending approval", invocationID)
	}
	select {
	case ch <- approvalDecision{approved: false, message: message}:
		return nil
	default:
		return fmt.Errorf("invocation %s approval already decided", invocationID)
	}
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
	resp := InvocationResponse{InvocationID: inv.InvocationID, ServerName: inv.Upstream, ToolName: inv.Tool, Status: inv.Status, Approval: inv.Approval, RequestID: inv.RequestID, SubmittedAt: inv.SubmittedAt, CompletedAt: inv.CompletedAt}
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

func normalizedLimit(limit uint64, fallback uint64) uint64 {
	if limit == 0 {
		return fallback
	}
	return limit
}
