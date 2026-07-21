package googlegateway

import (
	"context"
	"log/slog"
	"time"

	"atryum/internal/invocation"
)

// Service turns a normalized ToolCall into an Atryum invocation, runs the
// approval engine, and blocks until a terminal decision (or the decision
// timeout, which fails closed). It is transport-agnostic; the ext_authz gRPC
// server is a thin adapter over Decide.
type Service struct {
	inv InvocationGateway
	cfg Config
}

// NewService builds a callout decision service over the reused invocation
// gateway. *invocation.Service satisfies InvocationGateway.
func NewService(inv InvocationGateway, cfg Config) *Service {
	return &Service{inv: inv, cfg: cfg.withDefaults()}
}

func (s *Service) log() *slog.Logger { return slog.With("component", "google_agent_gateway") }

// Decide submits the tool call to the approval engine and waits for the verdict.
// It fails CLOSED (deny) on any error or on decision timeout, matching the
// gateway's fail-closed default. The tool is never executed here — on an allow,
// Agent Gateway forwards the call to its destination.
func (s *Service) Decide(ctx context.Context, tc ToolCall) Decision {
	source := s.cfg.Source
	if tc.ServerName != "" {
		source = tc.ServerName // MCP tools match rules on their server name
	}
	var idem *string
	if tc.CallID != "" {
		id := tc.CallID
		idem = &id
	}
	resp, err := s.inv.Submit(ctx, invocation.ExternalSubmitRequest{
		Source:         source,
		Tool:           tc.Tool,
		Description:    "Google Agent Gateway tool call: " + tc.Tool,
		Input:          tc.Input,
		RequestID:      idem,
		IdempotencyKey: idem, // dedupe across gateway retries / deny-and-resubmit
		ClientName:     s.cfg.ClientName,
		ClientVersion:  s.cfg.ClientVersion,
		AgentID:        tc.AgentID,
	})
	if err != nil {
		s.log().Warn("submit failed; failing closed", "tool", tc.Tool, "error", err)
		return Decision{Allow: false, Message: "atryum: approval submission failed"}
	}
	return s.await(ctx, resp)
}

// await polls the invocation until a terminal approval decision, the decision
// timeout, or context cancellation. Non-terminal timeout fails closed.
func (s *Service) await(ctx context.Context, resp invocation.InvocationResponse) Decision {
	id := resp.InvocationID
	deadline := time.Now().Add(s.cfg.DecisionTimeout)
	for {
		switch resp.Status {
		case invocation.StatusApproved, invocation.StatusExecuting, invocation.StatusSucceeded:
			return Decision{Allow: true}
		case invocation.StatusDenied, invocation.StatusExpired, invocation.StatusCancelled, invocation.StatusFailed:
			return Decision{Allow: false, Message: denyReason(resp)}
		default: // received, pending_approval
			if time.Now().After(deadline) {
				// The gateway can't park for a human; return deny now. The human
				// approves out-of-band and the agent's retry (same idempotency
				// key) resolves to allow.
				return Decision{Allow: false, Message: "atryum: pending human approval — retry to resume"}
			}
			select {
			case <-ctx.Done():
				return Decision{Allow: false, Message: "atryum: request cancelled"}
			case <-time.After(s.cfg.PollInterval):
			}
			next, err := s.inv.Get(ctx, id)
			if err != nil {
				s.log().Warn("get invocation failed; failing closed", "invocation_id", id, "error", err)
				return Decision{Allow: false, Message: "atryum: approval lookup failed"}
			}
			resp = next
		}
	}
}

func denyReason(resp invocation.InvocationResponse) string {
	if resp.Approval != nil && resp.Approval.Reason != nil && *resp.Approval.Reason != "" {
		return *resp.Approval.Reason
	}
	return "denied by Atryum policy"
}
