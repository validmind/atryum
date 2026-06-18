# Atryum Claude Code hook

A Claude Code command-hook adapter that gates native Claude Code tools through
Atryum before execution, then reports successful results back for audit.

This is the Claude Code equivalent of `examples/amp-plugin`: Claude Code runs
the tool, while Atryum mediates approval and stores the audit trail.

## Install

Copy the shared hook somewhere stable:

```sh
mkdir -p ~/.atryum/hooks
cp examples/shared-agent-hook/atryum-hook.mjs ~/.atryum/hooks/atryum-hook.mjs
chmod +x ~/.atryum/hooks/atryum-hook.mjs
```

Then add this to `~/.claude/settings.json` for all projects, or to
`.claude/settings.json` for one project:

```json
{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "*",
        "hooks": [
          {
            "type": "command",
            "command": "ATRYUM_HOOK_HOST=claude ATRYUM_HOOK_EVENT=PreToolUse ATRYUM_SOURCE=claude-code node ~/.atryum/hooks/atryum-hook.mjs"
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
            "command": "ATRYUM_HOOK_HOST=claude ATRYUM_HOOK_EVENT=PostToolUse ATRYUM_SOURCE=claude-code node ~/.atryum/hooks/atryum-hook.mjs"
          }
        ]
      }
    ]
  }
}
```

Restart Claude Code after changing settings.

## Configure

| var | default | meaning |
| --- | --- | --- |
| `ATRYUM_URL` | `http://localhost:8080` | base URL of the Atryum server |
| `ATRYUM_SOURCE` | `claude-code` in the example | source label in Atryum |
| `ATRYUM_POLL_MS` | `2000` | approval polling interval |
| `ATRYUM_STATE_DIR` | `~/.atryum/agent-hook-state` | tool-use to invocation-id state |
| `ATRYUM_CHAT_MESSAGES_LIMIT` | `100` | recent Claude Code chat messages sent as LLM-as-judge context |
| `ATRYUM_MAX_MESSAGE_CHARS` | `2000` | maximum characters included from any one chat message |
| `ATRYUM_CLAUDE_TRANSCRIPT_PATH` | hook `transcript_path` | override Claude Code transcript file path |

## LLM-as-judge chat context

Before each Claude Code `PreToolUse` decision, the hook reads Claude's
`transcript_path` from the hook input when available, extracts recent
user/assistant/system messages, and sends them to Atryum as `chat_context` and
`chat_context_messages`. Atryum appends that context to local and backend
LLM-as-judge evaluations, alongside any tool description/schema context.

Set `ATRYUM_CHAT_MESSAGES_LIMIT` to change how many recent messages are sent.
Set it to `0` to disable Claude Code chat context.

## Notes

Claude Code hook support is documented around `PreToolUse` and `PostToolUse`.
`PreToolUse` receives `tool_name`, `tool_input`, `tool_use_id`, and, in current
Claude Code builds, `transcript_path`; the hook returns a permission decision
before the tool executes.
