# Atryum Codex setup

Codex supports lifecycle hooks through `~/.codex/hooks.json`. Atryum can use
those hooks to submit Codex tool events to `/api/v1/external/invocations` before
execution and report results after execution.

Install with:

```sh
atryum hooks install codex
```

Or copy the shared hook manually:

```sh
mkdir -p ~/.atryum/hooks
cp examples/shared-agent-hook/atryum-hook.mjs ~/.atryum/hooks/atryum-hook.mjs
chmod +x ~/.atryum/hooks/atryum-hook.mjs
```

Then add this to `~/.codex/hooks.json`:

```json
{
  "hooks": {
    "SessionStart": [
      {
        "matcher": "*",
        "hooks": [
          {
            "type": "command",
            "command": "ATRYUM_HOOK_HOST=codex ATRYUM_HOOK_EVENT=SessionStart ATRYUM_SOURCE=codex node ~/.atryum/hooks/atryum-hook.mjs"
          }
        ]
      }
    ],
    "PreToolUse": [
      {
        "matcher": "*",
        "hooks": [
          {
            "type": "command",
            "command": "ATRYUM_HOOK_HOST=codex ATRYUM_HOOK_EVENT=PreToolUse ATRYUM_SOURCE=codex node ~/.atryum/hooks/atryum-hook.mjs"
          }
        ]
      }
    ],
    "PostToolUse": [
      {
        "matcher": "*",
        "hooks": [
          {
            "type": "command",
            "command": "ATRYUM_HOOK_HOST=codex ATRYUM_HOOK_EVENT=PostToolUse ATRYUM_SOURCE=codex node ~/.atryum/hooks/atryum-hook.mjs"
          }
        ]
      }
    ]
  }
}
```

Run `/hooks` in Codex to review and trust the hook. Codex records trust against
the hook definition, so changing the command requires re-review.

## Configure

| var | default | meaning |
| --- | --- | --- |
| `ATRYUM_URL` | `http://localhost:8080` | base URL of the Atryum server |
| `ATRYUM_SOURCE` | `codex` in the example | source label in Atryum |
| `ATRYUM_POLL_MS` | `2000` | approval polling interval |
| `ATRYUM_AGENT_ID` | _(empty)_ | self-declared agent identifier; matched against Agent Record `agent_ids` |
| `ATRYUM_ACCESS_TOKEN` | _(empty)_ | optional OAuth bearer token for Atryum agent runtime APIs |
| `ATRYUM_STATE_DIR` | `~/.atryum/agent-hook-state` | tool-use to invocation-id state |

## LLM-as-judge session context

The hook does **not** scrape Codex's chat payload, local session JSONL, or
history, or send any chat/context blob to Atryum. The harness is trusted to
report _which_ session a tool call belongs to, but a runaway agent must not be
able to hand the judge arbitrary text to poison it.

Instead, the hook sends Codex's own session id as `client_session_id` on every
`POST /api/v1/external/invocations` and lets Atryum manage the session
server-side. Atryum resolves the internal session with get-or-create keyed by
(agent binding, `client_session_id`) and reconstructs the judge's context from
the prior tool calls it recorded for that session — trusting tool outputs more
than tool inputs, and ignoring agent chat entirely. Because the server manages
the session, the hook keeps no session state on disk even though each hook
invocation is a fresh process — no mint call, no cache file, no re-mint/retry.

A session still requires an agent binding: `ATRYUM_AGENT_ID` (no-auth mode) or
the bearer token (auth mode). With neither, the caller is anonymous and Atryum
resolves no session, evaluating the call history-free (tool calls are still
gated, just without prior-call context).

## Preapproval plans

When plan-scoped rules apply to the current agent, the shared hook discovers
that via `GET /api/v1/agent/rules` and injects plan-submission guidance at
`SessionStart`. It also includes the guidance in a blocked tool message as a
fallback. The agent can submit a batch plan to
`POST /api/v1/external/plans?source=<source>` (the hint provides the exact
endpoint; the source parameter scopes the plan's actions to this harness so
later tool calls match), wait for approval, and then continue with normal
tool calls. Atryum's adherence judge checks each call matching a declared
action against the approved plan: confirmed calls are preapproved until the
plan expires, off-plan calls are denied, and polling the approved plan's
status URL is always allowed.

## MCP proxy

You can also configure Codex to use Atryum's MCP proxy instead of connecting
directly to an upstream MCP server:

```toml
# ~/.codex/config.toml

[mcp_servers.leanctx_via_atryum]
command = "npx"
args = [
  "mcp-remote",
  "http://localhost:8080/mcp/leanctx"
]
```

Replace `leanctx` with the Atryum MCP server name you configured in the Atryum
server registry. When plan-scoped rules apply, `tools/list` also exposes
`atryum.plan.submit` and `atryum.plan.get`; MCP agents can call those synthetic
tools to submit and poll a plan without using the raw HTTP endpoint directly.

## MCP coverage

- Codex calls to MCP tools routed through Atryum
- Atryum approval rules, invocation logging, and admin UI for those MCP calls
