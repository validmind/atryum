// Package googlegateway gates agents running on Google's Gemini Enterprise
// Agent Platform (GEAP) by acting as an Agent Gateway *authorization extension*.
//
// GEAP's Agent Gateway is a central policy enforcement point that all managed
// agent traffic routes through. It can "delegate authorization to custom
// authorization engines or third-party systems" via a Service Extensions
// authorization extension: a gRPC Envoy ext_authz callout to a service you run.
// Atryum runs that callout — bound once to the gateway resource, it gates every
// tool call from every routed agent (Agent Studio no-code, ADK, marketplace)
// with no per-agent code.
//
// This core package is transport-agnostic: it turns a normalized tool call into
// an Atryum invocation, runs the approval rules (auto-approve / auto-deny /
// LLM-as-judge / human approval) via the same external-hook gateway the Claude
// managed-agents bridge uses, and returns allow/deny. The Envoy ext_authz wire
// adapter lives in the extauthz subpackage so this package (and its tests) build
// without the gRPC/go-control-plane dependency.
package googlegateway

import (
	"context"
	"time"

	"atryum/internal/invocation"
)

// InvocationGateway is the slice of invocation.Service the callout reuses. It is
// exactly the external/hook path (POST /api/v1/external/invocations) used by the
// Claude Code hook and the managed-agents bridge — Atryum decides, the gateway
// (not Atryum) proxies the tool on an allow.
type InvocationGateway interface {
	Submit(ctx context.Context, req invocation.ExternalSubmitRequest) (invocation.InvocationResponse, error)
	Get(ctx context.Context, invocationID string) (invocation.InvocationResponse, error)
}

// Config holds one callout's runtime settings.
type Config struct {
	// ListenAddr is the gRPC address the ext_authz callout serves on (e.g.
	// ":8090"). Agent Gateway's authorization extension dials this.
	ListenAddr string
	// Source is the approval-rule "server" key recorded for callout invocations
	// whose tool is not an MCP tool carrying its own server name.
	Source string
	// PollInterval is how often a pending (human) approval is re-checked while
	// the callout is blocking.
	PollInterval time.Duration
	// DecisionTimeout bounds how long the callout blocks before failing closed
	// (deny). It MUST be set below the Agent Gateway authorization-extension
	// timeout (fail-closed there too), so auto-rules and a fast LLM-as-judge
	// resolve synchronously. Human approvals that outlast it return a deny with a
	// "retry to resume" message (deny-and-resubmit) — the gateway cannot park a
	// request awaiting a human.
	DecisionTimeout time.Duration
	// ClientName / ClientVersion identify the harness in the invocations UI.
	ClientName    string
	ClientVersion string
}

const (
	defaultSource          = "google-agent-gateway"
	defaultPollInterval    = 200 * time.Millisecond
	defaultDecisionTimeout = 30 * time.Second
)

func (c Config) withDefaults() Config {
	if c.Source == "" {
		c.Source = defaultSource
	}
	if c.PollInterval <= 0 {
		c.PollInterval = defaultPollInterval
	}
	if c.DecisionTimeout <= 0 {
		c.DecisionTimeout = defaultDecisionTimeout
	}
	if c.ClientName == "" {
		c.ClientName = defaultSource
	}
	return c
}

// ToolCall is the normalized agent tool invocation extracted from an Agent
// Gateway authorization request, decoupled from the ext_authz wire types so the
// decision logic stays unit-testable.
type ToolCall struct {
	AgentID    string         // authenticated agent identity from the gateway
	Tool       string         // tool / function name
	ServerName string         // MCP server name, when known
	Input      map[string]any // tool arguments
	CallID     string         // stable id for idempotency (dedupe retries)
}

// Decision is the callout's answer for a tool call.
type Decision struct {
	Allow   bool
	Message string // deny reason surfaced to the agent
}
