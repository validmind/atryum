# Atryum Cursor hook

A Cursor command-hook adapter that gates Cursor agent tool calls through Atryum
before execution, then reports successful results back for audit.

Cursor's hook surface is still evolving. This adapter targets the current
`preToolUse` / `postToolUse` command hook shape used by Cursor plugins and
`.cursor/hooks.json`.

## Install

Copy the shared hook somewhere stable:

```sh
mkdir -p ~/.atryum/hooks
cp examples/shared-agent-hook/atryum-hook.mjs ~/.atryum/hooks/atryum-hook.mjs
chmod +x ~/.atryum/hooks/atryum-hook.mjs
```

Then add this to `~/.cursor/hooks.json` for all projects, or to
`.cursor/hooks.json` for one project:

```json
{
  "hooks": {
    "preToolUse": [
      {
        "type": "command",
        "command": "ATRYUM_HOOK_HOST=cursor ATRYUM_HOOK_EVENT=preToolUse ATRYUM_SOURCE=cursor node ~/.atryum/hooks/atryum-hook.mjs"
      }
    ],
    "postToolUse": [
      {
        "type": "command",
        "command": "ATRYUM_HOOK_HOST=cursor ATRYUM_HOOK_EVENT=postToolUse ATRYUM_SOURCE=cursor node ~/.atryum/hooks/atryum-hook.mjs"
      }
    ]
  }
}
```

Restart Cursor after changing hooks.

## Configure

| var | default | meaning |
| --- | --- | --- |
| `ATRYUM_URL` | `http://localhost:8080` | base URL of the Atryum server |
| `ATRYUM_SOURCE` | `cursor` in the example | source label in Atryum |
| `ATRYUM_POLL_MS` | `2000` | approval polling interval |
| `ATRYUM_STATE_DIR` | `~/.atryum/agent-hook-state` | tool-use to invocation-id state |
| `ATRYUM_CHAT_MESSAGES_LIMIT` | `100` | recent Cursor chat messages sent as LLM-as-judge context when available |
| `ATRYUM_MAX_MESSAGE_CHARS` | `2000` | maximum characters included from any one chat message |
| `ATRYUM_CURSOR_TRANSCRIPT_PATH` | hook transcript/conversation path | override Cursor transcript file path |
| `ATRYUM_CHAT_HISTORY_PATH` | _(empty)_ | generic override for a chat history JSON/JSONL file |

## LLM-as-judge chat context

Before each Cursor `preToolUse` decision, the hook looks for recent chat
messages in the hook payload (`messages`, `conversation`, `transcript`, or
`chat_history`) and, when those are absent, reads a hook-provided
`transcript_path` / `conversation_path` file if available. It sends extracted
user/assistant/system messages to Atryum as `chat_context` and
`chat_context_messages`.

Set `ATRYUM_CHAT_MESSAGES_LIMIT` to change how many recent messages are sent.
Set it to `0` to disable Cursor chat context.

## Plugin packaging

For a Cursor plugin, point the plugin manifest at a hooks file that contains
the same `preToolUse` and `postToolUse` entries. Cursor plugin manifests support
a `hooks` path or inline hooks object.
