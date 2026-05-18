package claudeagents

import (
	"context"
	"fmt"
	"strings"

	"atryum/internal/invocation"
)

// Dispatcher implements invocation.ResolutionListener. When an invocation
// that originated from a Claude managed agent (i.e. has ClaudeSessionID and
// ClaudeToolUseEventID populated) reaches a final approval state, the
// dispatcher posts the corresponding `user.tool_confirmation` event back to
// Anthropic.
//
// Invocations without Claude linkage are silently ignored so the dispatcher
// can be safely registered alongside MCP and harness flows.
type Dispatcher struct {
	client *Client
}

// NewDispatcher builds a Dispatcher backed by the given client.
func NewDispatcher(client *Client) *Dispatcher {
	return &Dispatcher{client: client}
}

// OnResolution satisfies invocation.ResolutionListener.
func (d *Dispatcher) OnResolution(ctx context.Context, inv invocation.Invocation) error {
	if inv.ClaudeSessionID == nil || inv.ClaudeToolUseEventID == nil {
		return nil
	}
	sessionID := strings.TrimSpace(*inv.ClaudeSessionID)
	toolUseID := strings.TrimSpace(*inv.ClaudeToolUseEventID)
	if sessionID == "" || toolUseID == "" {
		return nil
	}

	result, denyMessage := decisionFromInvocation(inv)
	if result == "" {
		return nil
	}

	if err := d.client.SendToolConfirmation(ctx, sessionID, toolUseID, result, denyMessage); err != nil {
		return fmt.Errorf("post tool confirmation: %w", err)
	}
	return nil
}

// decisionFromInvocation maps an invocation's terminal status to the
// allow/deny shape Anthropic expects. Returns ("", "") when the invocation
// is not in a final state we should report.
func decisionFromInvocation(inv invocation.Invocation) (ConfirmationResult, string) {
	switch inv.Status {
	case invocation.StatusApproved:
		return ResultAllow, ""
	case invocation.StatusDenied:
		return ResultDeny, denyMessageFromApproval(inv.Approval)
	}
	return "", ""
}

func denyMessageFromApproval(a *invocation.Approval) string {
	if a == nil || a.Reason == nil {
		return ""
	}
	r := strings.TrimSpace(*a.Reason)
	// "human" is a sentinel meaning the human denied without supplying a
	// reason; surface a generic message rather than the raw sentinel.
	if r == "" || r == "human" {
		return ""
	}
	return r
}
