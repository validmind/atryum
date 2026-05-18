# Claude Managed Agents — End-to-End Example

This walks through wiring atryum as the human-in-the-loop approval gateway for an Anthropic Managed Agent named **"PR Reviewer"**.

## Prerequisites

- Atryum running (see top-level [README.md](../README.md))
- An Anthropic API key with access to the Managed Agents beta
- An agent created on Anthropic's side (you supply its `agent_id`)
- A session id for that agent that you want atryum to manage

## 1. Configure atryum

Add the `[claude_agents]` block to `atryum.toml`:

```toml
[claude_agents]
api_key               = "sk-ant-..."
base_url              = "https://api.anthropic.com"
poll_interval_seconds = 5
```

Restart atryum. You should see:

```
claude agents watcher started
```

in the logs.

## 2. Register the agent

Either via the UI (`/ui/claude-agents` → **New Agent**) or via the API:

```bash
curl -X POST http://localhost:8080/api/v1/admin/claude-agents \
  -H 'Content-Type: application/json' \
  -d '{
        "name": "PR Reviewer",
        "agent_id": "agent_abc123",
        "description": "Reviews pull requests"
      }'
```

Response includes the atryum-side `id` (e.g. `cagent_…`). Save it as `$AGENT_ID`.

## 3. Register a session to watch

```bash
curl -X POST "http://localhost:8080/api/v1/admin/claude-agents/$AGENT_ID/sessions" \
  -H 'Content-Type: application/json' \
  -d '{ "session_id": "sess_xyz789" }'
```

The watcher picks the new session up on its next tick (≤ `poll_interval_seconds`).

## 4. (Optional) Add a rule scoped to this agent

Auto-approve any `read` tool call from PR Reviewer (still ask for everything else):

```bash
curl -X POST http://localhost:8080/api/v1/admin/rules \
  -H 'Content-Type: application/json' \
  -d '{
        "action": "auto_approve",
        "server_patterns": ["PR Reviewer"],
        "tool_patterns": ["read"],
        "user_pattern": "*",
        "enabled": true
      }'
```

The `server_patterns` field matches against the **agent name**, so this rule applies only to invocations sourced from "PR Reviewer".

## 5. Trigger something on the Claude side

When the managed agent attempts a tool call configured with `permission_policy: { type: "always_ask" }`, the session emits `agent.tool_use` (or `agent.mcp_tool_use`) followed by `session.status_idle` with `stop_reason.type == requires_action`.

The atryum watcher will:

1. See the `requires_action` idle.
2. Correlate it with the referenced `tool_use` event.
3. Submit an invocation tagged with `Source = "PR Reviewer"`, `ClaudeSessionID = sess_xyz789`, `ClaudeToolUseEventID = toolu_…`.
4. Evaluate rules:
   - If a rule matches `auto_approve` → invocation auto-approved → dispatcher posts `user.tool_confirmation { result: "allow" }`.
   - If a rule matches `auto_deny` → invocation auto-denied → dispatcher posts `{ result: "deny", deny_message: "<rule reason>" }`.
   - Otherwise the invocation lands in `pending_approval`. Visit `/ui/invocations` to approve/deny manually; on resolution the dispatcher posts the corresponding confirmation back.

## Sequence

```diagram
╭───────────╮      ╭─────────╮      ╭─────────╮      ╭─────────────╮
│ Anthropic │      │ atryum  │      │ rules / │      │  atryum     │
│  agent    │      │ watcher │      │ human   │      │ dispatcher  │
╰─────┬─────╯      ╰────┬────╯      ╰────┬────╯      ╰──────┬──────╯
      │ tool_use         │                │                  │
      │ status_idle      │                │                  │
      │ (requires_action)│                │                  │
      │─────────────────▶│                │                  │
      │                  │ Submit         │                  │
      │                  │───────────────▶│                  │
      │                  │                │ allow / deny     │
      │                  │                │─────────────────▶│
      │ user.tool_confirmation                              │
      │◀─────────────────────────────────────────────────────│
      │                  │                │                  │
```

## Cleanup

Remove a session (atryum stops watching it):

```bash
curl -X DELETE \
  "http://localhost:8080/api/v1/admin/claude-agents/$AGENT_ID/sessions/$SESSION_ROW_ID"
```

Disable an agent (sessions stay registered but are skipped by the watcher):

```bash
curl -X PUT "http://localhost:8080/api/v1/admin/claude-agents/$AGENT_ID" \
  -H 'Content-Type: application/json' \
  -d '{ "name": "PR Reviewer", "agent_id": "agent_abc123", "enabled": false }'
```

Delete an agent entirely (cascades to its sessions):

```bash
curl -X DELETE "http://localhost:8080/api/v1/admin/claude-agents/$AGENT_ID"
```
