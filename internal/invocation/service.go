package invocation

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"atryum/internal/mcp"
)

type invocationRepo interface {
	Create(ctx context.Context, inv Invocation) error
	UpdateResult(ctx context.Context, inv Invocation) error
	Get(ctx context.Context, id string) (Invocation, error)
	GetByIdempotencyKey(ctx context.Context, key string) (Invocation, error)
	List(ctx context.Context, limit uint64) ([]Invocation, error)
}

type eventRepo interface {
	Create(ctx context.Context, evt Event) error
	ListByInvocation(ctx context.Context, invocationID string) ([]Event, error)
}

type resolver interface {
	Resolve(name string) (mcp.Upstream, error)
}

type upstreamClient interface {
	Invoke(ctx context.Context, upstream mcp.Upstream, tool string, input map[string]any, requestID *string) (mcp.InvokeResult, error)
}

type Service struct {
	invocations    invocationRepo
	events         eventRepo
	resolver       resolver
	client         upstreamClient
	defaultTimeout time.Duration
}

func NewService(inv invocationRepo, evt eventRepo, resolver resolver, client upstreamClient, defaultTimeout time.Duration) *Service {
	return &Service{invocations: inv, events: evt, resolver: resolver, client: client, defaultTimeout: defaultTimeout}
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
	upstream, err := s.resolver.Resolve(req.Server)
	if err != nil {
		return InvocationResponse{}, err
	}
	inputJSON, err := json.Marshal(req.Input)
	if err != nil {
		return InvocationResponse{}, err
	}
	now := time.Now().UTC()
	inv := Invocation{InvocationID: "inv_" + uuid.NewString(), RequestID: req.RequestID, IdempotencyKey: req.IdempotencyKey, Tool: req.Tool, Upstream: upstream.Name, Status: StatusReceived, Approval: nil, Input: inputJSON, SubmittedAt: now}
	if err := s.invocations.Create(ctx, inv); err != nil {
		return InvocationResponse{}, err
	}
	_ = s.events.Create(ctx, Event{InvocationID: inv.InvocationID, EventType: "invocation.received", Payload: mustJSON(map[string]any{"tool": req.Tool, "upstream": upstream.Name}), CreatedAt: now})
	inv.Status = StatusExecuting
	if err := s.invocations.UpdateResult(ctx, inv); err != nil {
		return InvocationResponse{}, err
	}
	_ = s.events.Create(ctx, Event{InvocationID: inv.InvocationID, EventType: "invocation.executing", Payload: mustJSON(map[string]any{"upstream": upstream.Name}), CreatedAt: time.Now().UTC()})
	ctx, cancel := context.WithTimeout(ctx, s.defaultTimeout)
	defer cancel()
	result, err := s.client.Invoke(ctx, upstream, req.Tool, req.Input, req.RequestID)
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
		_ = s.events.Create(ctx, Event{InvocationID: inv.InvocationID, EventType: "invocation.failed", Payload: result.Body, CreatedAt: completed})
	} else {
		inv.Status = StatusSucceeded
		inv.Response = result.Body
		_ = s.events.Create(ctx, Event{InvocationID: inv.InvocationID, EventType: "invocation.succeeded", Payload: result.Body, CreatedAt: completed})
	}
	if err := s.invocations.UpdateResult(ctx, inv); err != nil {
		return InvocationResponse{}, err
	}
	return s.toResponse(inv), nil
}

func (s *Service) Get(ctx context.Context, id string) (InvocationResponse, error) {
	inv, err := s.invocations.Get(ctx, id)
	if err != nil {
		return InvocationResponse{}, err
	}
	return s.toResponse(inv), nil
}

func (s *Service) List(ctx context.Context, limit uint64) ([]InvocationResponse, error) {
	invocations, err := s.invocations.List(ctx, limit)
	if err != nil {
		return nil, err
	}
	out := make([]InvocationResponse, 0, len(invocations))
	for _, inv := range invocations {
		out = append(out, s.toResponse(inv))
	}
	return out, nil
}

func (s *Service) Events(ctx context.Context, invocationID string) ([]EventResponse, error) {
	events, err := s.events.ListByInvocation(ctx, invocationID)
	if err != nil {
		return nil, err
	}
	out := make([]EventResponse, 0, len(events))
	for _, evt := range events {
		out = append(out, EventResponse{Type: evt.EventType, Timestamp: evt.CreatedAt, Data: evt.Payload})
	}
	return out, nil
}

func (s *Service) toResponse(inv Invocation) InvocationResponse {
	resp := InvocationResponse{InvocationID: inv.InvocationID, Status: inv.Status, Approval: inv.Approval, RequestID: inv.RequestID, SubmittedAt: inv.SubmittedAt, CompletedAt: inv.CompletedAt}
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
