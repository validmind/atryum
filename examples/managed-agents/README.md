# Atryum + Claude Managed Agents

[Claude Managed Agents](https://platform.claude.com/docs/en/managed-agents/overview)
is Anthropic's *hosted* agent harness: Anthropic runs the agent loop and
executes tools on its own infrastructure. Unlike Claude Code, it exposes no
`PreToolUse` / `PostToolUse` hook, so the command-hook adapter in
`examples/claude-code-hook` does not apply.

Atryum integrates two ways:

| | Events bridge (recommended) | MCP proxy |
| --- | --- | --- |
| What it gates | **Any** blocking tool call — built-in (`bash`, `read`, `write`, …) **and** MCP tools | only MCP tool calls routed through Atryum |
| How | Atryum streams the session's events and answers the harness's confirmation prompts | the agent's `mcp_servers[].url` points at Atryum's `/mcp/{server}` |
| Tool config | `permission_policy: always_ask` (or custom tools) | `permission_policy: always_allow` (Atryum is the gate) |
| Credentials | Anthropic holds them | Atryum holds the upstream credentials |
| Who calls whom | Atryum → Anthropic (outbound) | Anthropic → Atryum (inbound) |

This page documents the **events bridge**. For the MCP proxy path, see the
[MCP connector](#appendix-mcp-proxy-path) appendix at the bottom.

## Events bridge: how it works

```
 ┌────────────────────────────┐   GET  /v1/sessions/{id}/events/stream   ┌────────────────────────┐
 │ Anthropic cloud            │◀─────────────────────────────────────────│ Atryum events bridge   │
 │ Managed Agent + sandbox    │   POST /v1/sessions/{id}/events           │ (watcher per session)  │
 │ runs tools, blocks on      │──────────── tool_confirmation ──────────▶ │  • records invocations │
 │ always_ask confirmations   │                                           │  • runs approval rules │
 └────────────────────────────┘                                          └────────────────────────┘
```

When a session is registered, Atryum opens an outbound connection to its event
stream and:

1. **Records every session event** into the invocations table. Each watched
   session gets one synthetic audit invocation (tool `_managed_agent_session`)
   whose `invocation_events` are the raw Anthropic events.
2. **Turns each tool call into an invocation.** `agent.tool_use`,
   `agent.mcp_tool_use`, and `agent.custom_tool_use` events are submitted to the
   normal Atryum approval engine (same path as the external/hook integration).
3. **Answers blocking tool calls.** When the session emits
   `session.status_idle` with `stop_reason: requires_action`, Atryum waits for
   the rule decision (`auto_approve` / `auto_deny` / human approval) and replies
   to Claude:
   - hosted/MCP tools → `user.tool_confirmation` with `result: "allow"|"deny"`
     (and `deny_message` on denial).
   - custom tools → `user.custom_tool_result` carrying an Atryum decision
     envelope (see [Custom tools](#custom-tools)).
4. **Records the outcome.** The later tool-result event is recorded on the
   invocation as `completed` / `failed`.

Because Atryum answers the harness's own confirmation prompts, it can gate the
**built-in agent toolset** (`bash`, file ops, web) — not just MCP tools.

## Prerequisites

1. **An Anthropic API key** for the workspace the sessions run in.
2. **Tools configured to ask.** Set the toolset `permission_policy` to
   `always_ask` so the harness actually blocks and asks Atryum before running a
   tool. Tools left at `always_allow` run without ever pausing, so Atryum can
   only record them after the fact — it cannot gate them.

## Setup

### 1. Enable the bridge in `atryum.toml`

Declare one `[[managed_agents]]` table per Anthropic account/workspace API key.
The `name` is a unique label the session-registration API uses to target a
specific account.

```toml
[[managed_agents]]
name    = "default"   # unique label; targeted by the registration "account" field
workspace = "anthropic-workspace-name-or-id" # display/metadata label
# Anthropic API key created in that workspace. Env overrides (single account only):
# ATRYUM_MANAGED_AGENTS_API_KEY, then ANTHROPIC_API_KEY.
# If using env for the key, set ATRYUM_MANAGED_AGENTS_WORKSPACE too.
api_key = "sk-ant-..."
# Optional tuning (defaults shown):
# base_url                  = "https://api.anthropic.com"
# poll_interval_millis      = 1000   # how often a pending approval is re-checked
# reconnect_backoff_seconds = 2      # SSE reconnect backoff
# recent_chat_messages_limit = 100   # chat messages sent as LLM-as-judge context
# client_name               = "claude-managed-agents"  # shown in the Agent column
# client_version            = ""

# Watch a second account by repeating the table:
# [[managed_agents]]
# name    = "staging"
# workspace = "staging-workspace"
# api_key = "sk-ant-..."
```

Entries with an empty `api_key` are skipped; when no account has a usable key
the bridge is disabled and the operator endpoint returns `501`. The
`ATRYUM_MANAGED_AGENTS_API_KEY` / `ANTHROPIC_API_KEY` env overrides apply only
when zero or one `[[managed_agents]]` entry is configured. `workspace` is
required whenever `api_key` is set, but it is not sent as an Anthropic request
selector: Anthropic API keys are already workspace-scoped, so use an API key
created in the workspace whose Claude agents you want to list.

### 2. Create an agent whose tools ask for confirmation

```bash
curl -sS https://api.anthropic.com/v1/agents \
  -H "x-api-key: $ANTHROPIC_API_KEY" \
  -H "anthropic-version: 2023-06-01" \
  -H "anthropic-beta: managed-agents-2026-04-01" \
  -H "content-type: application/json" \
  -d '{
    "name": "Atryum-gated agent",
    "model": "claude-opus-4-8",
    "system": "You are a careful coding agent.",
    "tools": [
      { "type": "agent_toolset_20260401",
        "default_config": { "permission_policy": { "type": "always_ask" } } }
    ]
  }'
```

Create an environment and sessions as usual (see the
[quickstart](https://platform.claude.com/docs/en/managed-agents/quickstart)).

### 3. Link the Claude agent in Atryum

Open the Agents page, edit the Atryum agent you want rules to apply to, and
select the Claude Managed Agent. Atryum writes ownership metadata to the Claude
agent and discovers its sessions automatically.

Manual session registration still exists as an escape hatch:

```bash
curl -sS -X POST http://localhost:8080/api/v1/managed-agents/sessions \
  -H "content-type: application/json" \
  -d '{
    "session_id": "sess_...",
    "account": "default",          // optional when only one [[managed_agents]] is configured; required to disambiguate when several are
    "agent_id": "my-agent",        // optional: tags invocations & enables agent-scoped rules
    "description": "prod coding agent"
  }'
```

Atryum starts watching linked sessions as it discovers them and resumes watched
sessions on restart (the cursor is persisted, so it replays anything missed).
Send the session a user message; blocking tool calls now flow through your
Atryum rules and appear live in the invocations UI.

### Approval rules

Rules match on `(server, tool, agent)` exactly as for any other invocation:

- **Built-in tools** match with server = the configured `client_name`
  (default `claude-managed-agents`) and tool = `bash` / `read` / `write` / …
- **MCP tools** match with server = the MCP server name reported by Anthropic
  (falling back to `client_name` when none is reported).

With the default `manual_approval` policy, anything without a matching rule
parks for human approval (fails closed).

### Custom tools

Custom tools have no native allow/deny envelope, so on a decision Atryum sends a
`user.custom_tool_result` whose content is a structured block your tool can read:

```json
{ "atryum": { "decision": "allow", "invocation_id": "inv_..." } }
```
```json
{ "atryum": { "decision": "deny", "invocation_id": "inv_...", "deny_message": "..." } }
```

## What shows up in the invocations table

- One **audit invocation** per watched session (`_managed_agent_session`) with
  every raw session event as an `invocation_event`.
- One **invocation per tool call**, carrying the approval decision, the matched
  rule, the tool input, and the execution outcome.

## Notes & limits

- **Reachability is reversed** vs. the MCP path: Atryum dials out to Anthropic,
  so Atryum does *not* need a public ingress. It only needs outbound HTTPS to
  `api.anthropic.com`.
- **Long human approvals are fine.** The session sits idle (durable, checkpointed
  by Anthropic) while Atryum holds the confirmation; there is no synchronous
  HTTP timeout as there is on the MCP proxy path.
- **Tool-use creation is idempotent** (keyed on the Anthropic event ID), so
  stream reconnects and restarts don't double-submit. Raw event audit is
  at-least-once.

---

## Appendix: MCP proxy path

If you'd rather have Atryum hold the upstream credentials and proxy MCP traffic,
declare Atryum's `/mcp/{server}` as the agent's MCP server and authenticate with
a vault credential whose `access_token` is an OIDC JWT Atryum accepts (a
configured `[[auth]]` issuer). In this mode Anthropic calls **into** Atryum, so
Atryum must be reachable from Anthropic's cloud (public ingress or an MCP
tunnel), and only MCP tool calls are gated.

```json
{
  "mcp_servers": [
    { "type": "url", "name": "atryum-shortcut", "url": "https://atryum.example.com/mcp/shortcut" }
  ],
  "tools": [
    { "type": "mcp_toolset", "mcp_server_name": "atryum-shortcut",
      "default_config": { "permission_policy": { "type": "always_allow" } } }
  ]
}
```

Provide the JWT via a session `vault_ids` credential (`type: mcp_oauth`,
`mcp_server_url` matching the agent's `url`). Because the call is a synchronous
MCP `tools/call`, human approvals must complete within the connector timeout —
prefer auto-approve/auto-deny rules here, and use the events bridge above for
long human approvals.
