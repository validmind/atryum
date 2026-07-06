# Atryum amp plugin

A minimal [amp](https://ampcode.com) plugin that routes every tool call the
amp coding harness wants to make through Atryum for human approval, and
reports execution outcome back to Atryum for audit.

This makes Atryum work for tool calls that are **not** over MCP — e.g. amp's
`Bash`, `edit_file`, `Read`, etc. — by treating amp itself as the executor
and Atryum as the approval mediator and audit log.

## How it works

```diagram
╭───────────────╮  POST /api/v1/external/invocations    ╭──────────╮
│ amp tool.call │ ─────────────────────────────────────▶│ Atryum   │
│   handler     │                                        │  pending │
╰──────┬────────╯                                        │ approval │
       │     poll GET /api/v1/external/invocations/:id   ╰────┬─────╯
       │ ◀─────────────────────────────────────────────────── │
       │            (approved | denied)              human ──▶│ /ui/
       ▼
   allow | reject
       │
       │  tool runs in amp harness
       ▼
╭──────────────────╮  PATCH /api/v1/external/invocations/:id
│ amp tool.result  │ ─────────────────────────────────▶ atryum updates
│    handler       │   {execution_status: completed|failed|cancelled}
╰──────────────────╯
```

## Install

> The Amp plugin API only works when the Amp CLI is installed via the binary
> install method, not via `npm install`.

Plugins must be enabled at runtime by setting `PLUGINS=all` in the env amp
runs in:

```sh
export PLUGINS=all
```

### System-wide (recommended)

Use the Atryum CLI to install the plugin into `~/.config/amp/plugins/`:

```sh
./atryum hooks install amp
```

Or copy it manually. Once installed there, it will gate every amp session on
the machine:

```sh
mkdir -p ~/.config/amp/plugins
cp examples/amp-plugin/atryum.ts ~/.config/amp/plugins/atryum.ts
```

### Per-project

Or scope it to a single project:

```sh
mkdir -p .amp/plugins
cp examples/amp-plugin/atryum.ts .amp/plugins/atryum.ts
```

Make sure atryum is running (default `http://localhost:8080`) and open the
admin UI at <http://localhost:8080/ui/>. Pending tool calls will appear there
with Approve / Deny buttons.

To remove the global plugin later:

```sh
./atryum hooks uninstall amp
```

## Configure (env vars)

| var                            | default                           | meaning                                                                              |
| ------------------------------ | --------------------------------- | ------------------------------------------------------------------------------------ |
| `ATRYUM_URL`                   | `http://localhost:8080`           | base URL of the atryum server                                                        |
| `ATRYUM_SOURCE`                | `amp`                             | label shown as the "upstream" column in the atryum UI                                |
| `ATRYUM_POLL_MS`               | `2000`                            | how often the plugin polls atryum while awaiting approval                            |
| `ATRYUM_CLIENT_NAME`           | `amp`                             | harness name shown in the Atryum Agent column                                        |
| `ATRYUM_CLIENT_VERSION`        | `AMP_VERSION` if set              | harness version shown in Atryum                                                      |
| `ATRYUM_AGENT_ID`              | _(empty)_                         | self-declared agent identifier; matched against Agent Record `agent_ids` (see below) |
| `ATRYUM_ACCESS_TOKEN`          | _(empty)_                         | optional OAuth bearer token for Atryum agent runtime APIs                            |
| `ATRYUM_TOKEN_COMMAND`            | _(empty)_ | optional command run to mint each new token; prints a raw token or OAuth token JSON with `access_token`   |
| `ATRYUM_TOKEN_REFRESH_SKEW_MS`   | `60000`   | refresh command cache skew before token expiry                                       |
| `ATRYUM_TOKEN_COMMAND_TIMEOUT_MS` | `10000`   | timeout for the token command subprocess                                             |
| `ATRYUM_STATE_DIR`             | `~/.atryum/amp-plugin-state`      | directory for the on-disk token cache (`token-cache.json`, mode 0600)                |
| `ATRYUM_CHAT_MESSAGES_LIMIT`   | `100`                             | recent Amp thread messages sent as LLM-as-judge context                              |
| `ATRYUM_AMP_THREADS_DIR`       | `~/.local/share/amp/threads`      | Amp thread JSON directory                                                            |
| `ATRYUM_AMP_SESSION_FILE`      | `~/.local/share/amp/session.json` | Amp session state file used to identify the active thread                            |

## LLM-as-judge chat context

Before each tool call, the plugin builds compact recent context and sends it
to Atryum as `chat_context` and `chat_context_messages` (plus the deprecated
`context` alias for compatibility). It first tries the live plugin context
when available. If Amp has persisted the active thread under
`ATRYUM_AMP_THREADS_DIR`, the plugin includes that archived chat text.
For active sessions where Amp has not written a thread JSON file yet, it uses
the active thread ID from `ATRYUM_AMP_SESSION_FILE`, reads Amp's current
thread log, and includes fresh message/tool activity plus the tool calls and
results observed by the plugin.

Set `ATRYUM_CHAT_MESSAGES_LIMIT` to change how many recent messages are sent.
Set it to `0` to disable Amp chat context.

## Tagging invocations to an Agent Record

By default the plugin sends no agent identity, so invocations show up in
the UI unattached to any Agent Record. To get the agent column populated
and to make agent-scoped approval rules apply:

1. In the Atryum UI, open an Agent Record (or create one) and add a string
   to its **Agent IDs** field — e.g. `amp-local`, `amp-alice`, or any
   stable identifier you choose.
2. Export the same string in your shell:

   ```sh
   export ATRYUM_AGENT_ID=amp-local
   ```

3. Re-run amp. Future invocations carry `agent_id: "amp-local"`; Atryum
   looks it up via `agents.agent_ids @> ["amp-local"]` and tags the row.

This is a **self-declared** identity — anyone with network access to the
Atryum API can claim any agent id. For verified identity, run Atryum
behind OAuth (one or more `[[auth]]` blocks) and export an OAuth access
token instead:

```sh
export ATRYUM_ACCESS_TOKEN=<oauth-access-token>
```

The plugin sends it as `Authorization: Bearer ...` on every agent runtime
call. In auth mode Atryum derives the agent id from the token and ignores
`ATRYUM_AGENT_ID`. The token is used as-is and never refreshed — if it
expires, requests fail with `401`.

For short-lived tokens, set `ATRYUM_TOKEN_COMMAND` instead (if both are set,
`ATRYUM_TOKEN_COMMAND` wins and `ATRYUM_ACCESS_TOKEN` is ignored). This is a
shell command the plugin runs whenever it needs to mint a new token —
typically a client credentials request against your identity provider's token
endpoint, or a CLI that prints one:

```sh
export ATRYUM_TOKEN_COMMAND='curl -fsS -X POST "$OIDC_TOKEN_URL" \
  -d grant_type=client_credentials \
  -d client_id="$CLIENT_ID" \
  -d client_secret="$CLIENT_SECRET" \
  -d scope=atryum:mcp'
```

The command may print a raw token or JSON such as
`{"access_token":"...","expires_in":3600}`. The `expires_in` field is relative
seconds; `expires_at` (absolute Unix timestamp in seconds or milliseconds) is
also accepted. A raw token, or JSON without an expiry field, is assumed valid
for 55 minutes.

Token lifecycle: the plugin runs the command on the first request, then
caches the token — in memory and on disk at
`$ATRYUM_STATE_DIR/token-cache.json` (mode 0600) so restarts reuse it — and
reuses it until `ATRYUM_TOKEN_REFRESH_SKEW_MS` (default 60s) before expiry,
when it runs the command again to mint a replacement. If a request still gets
a `401`, the plugin bypasses the cache, mints a fresh token, and retries
the request once. The disk cache is keyed to `ATRYUM_TOKEN_COMMAND` and
`ATRYUM_URL`, so changing either invalidates it. If the command fails
(non-zero exit, timeout after `ATRYUM_TOKEN_COMMAND_TIMEOUT_MS`, or empty
output), the runtime call fails — run the command by hand in a shell to debug
it.

## API used

This plugin only depends on three endpoints atryum exposes for external
executors:

- `POST /api/v1/external/invocations` — submit a tool call for approval (returns immediately with `status: pending_approval`)
- `GET  /api/v1/external/invocations/:id` — poll status (`approved`, `denied`, …)
- `PATCH /api/v1/external/invocations/:id` — report `running` / `completed` / `failed` / `cancelled`

Human approval still happens in the Atryum UI; the plugin itself only calls
the non-admin external executor API above.
