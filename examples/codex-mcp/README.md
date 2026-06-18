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
| `ATRYUM_STATE_DIR` | `~/.atryum/agent-hook-state` | tool-use to invocation-id state |
| `ATRYUM_CHAT_MESSAGES_LIMIT` | `100` | recent Codex chat messages sent as LLM-as-judge context when available |
| `ATRYUM_MAX_MESSAGE_CHARS` | `2000` | maximum characters included from any one chat message |
| `ATRYUM_CODEX_TRANSCRIPT_PATH` | hook transcript/conversation path | override Codex transcript file path |
| `ATRYUM_CHAT_HISTORY_PATH` | _(empty)_ | generic override for a chat history JSON/JSONL file |

## LLM-as-judge chat context

Before each Codex `PreToolUse` decision, the hook looks for recent chat
messages in the hook payload. If Codex does not include chat messages there, the
hook reads Codex's local session JSONL under `~/.codex/sessions` using the hook
`session_id` when available, or the latest `~/.codex/history.jsonl` session as a
fallback. It sends extracted user/assistant/system messages to Atryum as
`chat_context` and `chat_context_messages`.

Set `ATRYUM_CHAT_MESSAGES_LIMIT` to change how many recent messages are sent.
Set it to `0` to disable Codex chat context.

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
server registry.

## MCP coverage

- Codex calls to MCP tools routed through Atryum
- Atryum approval rules, invocation logging, and admin UI for those MCP calls
