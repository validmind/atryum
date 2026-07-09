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

| var                               | default                           | meaning                                                                                                                    |
| --------------------------------- | --------------------------------- | -------------------------------------------------------------------------------------------------------------------------- |
| `ATRYUM_URL`                      | `http://localhost:8080`           | base URL of the Atryum server                                                                                              |
| `ATRYUM_SOURCE`                   | `cursor` in the example           | source label in Atryum                                                                                                     |
| `ATRYUM_POLL_MS`                  | `2000`                            | approval polling interval                                                                                                  |
| `ATRYUM_AGENT_ID`                 | _(empty)_                         | self-declared agent identifier; matched against Agent Record `agent_ids`                                                   |
| `ATRYUM_ACCESS_TOKEN`             | _(empty)_                         | optional OAuth bearer token for Atryum agent runtime APIs                                                                  |
| `ATRYUM_TOKEN_COMMAND`            | _(empty)_                         | optional command run to mint each new token; prints a raw token with no whitespace or OAuth token JSON with `access_token` |
| `ATRYUM_TOKEN_REFRESH_SKEW_MS`    | `60000`                           | refresh command cache skew before token expiry                                                                             |
| `ATRYUM_TOKEN_COMMAND_TIMEOUT_MS` | `10000`                           | timeout for the token command subprocess                                                                                   |
| `ATRYUM_STATE_DIR`                | `~/.atryum/agent-hook-state`      | tool-use to invocation-id state and the on-disk token cache (`token-cache.json`, mode 0600)                                |
| `ATRYUM_CHAT_MESSAGES_LIMIT`      | `100`                             | recent Cursor chat messages sent as LLM-as-judge context when available                                                    |
| `ATRYUM_MAX_MESSAGE_CHARS`        | `2000`                            | maximum characters included from any one chat message                                                                      |
| `ATRYUM_CURSOR_TRANSCRIPT_PATH`   | hook transcript/conversation path | override Cursor transcript file path                                                                                       |
| `ATRYUM_CHAT_HISTORY_PATH`        | _(empty)_                         | generic override for a chat history JSON/JSONL file                                                                        |

## Authentication

In no-auth mode (no `[[auth]]` blocks configured in Atryum), optionally set
`ATRYUM_AGENT_ID` to tag invocations with a self-declared agent identity.

When Atryum runs behind OAuth, export an access token before starting Cursor:

```sh
export ATRYUM_ACCESS_TOKEN=<oauth-access-token>
```

The hook sends it as `Authorization: Bearer ...` on every agent runtime call.
In auth mode Atryum derives the agent id from the token and ignores
`ATRYUM_AGENT_ID`. The token is used as-is and never refreshed — if it
expires, requests fail with `401`.

For short-lived tokens, set `ATRYUM_TOKEN_COMMAND` instead (if both are set,
`ATRYUM_TOKEN_COMMAND` wins and `ATRYUM_ACCESS_TOKEN` is ignored). This is a
shell command the hook runs whenever it needs to mint a new token — typically
a client credentials request against your identity provider's token endpoint,
or a CLI that prints one:

```sh
export ATRYUM_TOKEN_COMMAND='curl -fsS -X POST "$OIDC_TOKEN_URL" \
  -d grant_type=client_credentials \
  -d client_id="$CLIENT_ID" \
  -d client_secret="$CLIENT_SECRET" \
  -d scope=atryum:mcp'
```

(`$OIDC_TOKEN_URL`, `$CLIENT_ID`, and `$CLIENT_SECRET` are placeholders for
your identity provider's values — export them alongside the command.)

The command may print a raw token (a single string with no whitespace) or JSON such as
`{"access_token":"...","expires_in":3600}`. The `expires_in` field is relative
seconds; `expires_at` (absolute Unix timestamp in seconds or milliseconds) is
also accepted, and either may be a JSON number or a numeric string. A raw
token, or JSON without a usable (positive, numeric) expiry field, is assumed
valid for 55 minutes.

Token lifecycle: each hook invocation is a fresh process, so the on-disk cache
at `$ATRYUM_STATE_DIR/token-cache.json` (mode 0600) is what keeps the command
from running on every tool call — the hook reuses the cached token until
`ATRYUM_TOKEN_REFRESH_SKEW_MS` (default 60s) before expiry, then runs the
command again to mint a replacement. If a request still gets a `401`, the hook
bypasses the cache, mints a fresh token, and retries the request once. The
cache is keyed to `ATRYUM_TOKEN_COMMAND` and `ATRYUM_URL` (trailing slashes
ignored), so changing either invalidates it; environment variables referenced
by the command are not part of the cache key. If the command fails (non-zero exit, timeout after
`ATRYUM_TOKEN_COMMAND_TIMEOUT_MS`, empty output, or invalid token output), the
runtime call fails — run the command by hand in a shell to debug it.

Export these variables in the terminal session where you launch Cursor (or
prefix them in the hook `command` strings in `hooks.json`, as with
`ATRYUM_HOOK_HOST`).

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
