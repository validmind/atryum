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

| var                               | default                           | meaning                                                                                                                    |
| --------------------------------- | --------------------------------- | -------------------------------------------------------------------------------------------------------------------------- |
| `ATRYUM_URL`                      | `http://localhost:8080`           | base URL of the atryum server                                                                                              |
| `ATRYUM_SOURCE`                   | `amp`                             | label shown as the "upstream" column in the atryum UI                                                                      |
| `ATRYUM_POLL_MS`                  | `2000`                            | how often the plugin polls atryum while awaiting approval                                                                  |
| `ATRYUM_CLIENT_NAME`              | `amp`                             | harness name shown in the Atryum Agent column                                                                              |
| `ATRYUM_CLIENT_VERSION`           | `AMP_VERSION` if set              | harness version shown in Atryum                                                                                            |
| `ATRYUM_AGENT_ID`                 | _(empty)_                         | self-declared agent identifier; matched against Agent Record `agent_ids` (see below)                                       |
| `ATRYUM_ACCESS_TOKEN`             | _(empty)_                         | optional OAuth bearer token for Atryum agent runtime APIs                                                                  |
| `ATRYUM_TOKEN_COMMAND`            | _(empty)_                         | optional command run to mint each new token; prints a raw token with no whitespace or OAuth token JSON with `access_token` |
| `ATRYUM_TOKEN_REFRESH_SKEW_MS`    | `60000`                           | refresh command cache skew before token expiry                                                                             |
| `ATRYUM_TOKEN_COMMAND_TIMEOUT_MS` | `10000`                           | timeout for the token command subprocess                                                                                   |
| `ATRYUM_STATE_DIR`                | `~/.atryum/amp-plugin-state`      | directory for the on-disk token cache (`token-cache.json`, mode 0600)                                                      |
| `ATRYUM_AMP_SESSION_FILE`         | `~/.local/share/amp/session.json` | Amp session state file used to derive Amp's thread id, sent as `client_session_id`                                        |

## LLM-as-judge session context

The plugin does **not** send a free-form chat/context blob to Atryum. The
harness is trusted to report _which_ session a tool call belongs to, but a
runaway agent must not be able to hand the judge arbitrary text to poison it.

Instead, the plugin sends Amp's own thread id as `client_session_id` on every
`POST /api/v1/external/invocations` and lets Atryum manage the session
server-side. Atryum resolves the internal session with get-or-create keyed by
(agent binding, `client_session_id`) and reconstructs the judge's context from
the prior tool calls it recorded for that session — trusting tool outputs more
than tool inputs, and ignoring agent chat entirely. The plugin never mints,
persists, or echoes an Atryum session id.

If the caller has no agent binding, Atryum simply resolves no session and
evaluates the call history-free (tool calls are still gated, just without
prior-call context) rather than blocking the agent.

## Preapproval plans

When plan-scoped rules apply to the current agent, the plugin injects plan
submission guidance into the first agent turn after every `session.start` and
also repeats it in blocked tool messages as a fallback. It discovers support
via `GET /api/v1/agent/rules`. The agent can submit a batch plan to
`POST /api/v1/external/plans?source=<source>` (the hint provides the exact
endpoint; the source parameter scopes the plan's actions to this harness so
later tool calls match), wait for approval, and then continue with normal
tool calls. Atryum's adherence judge checks each call matching a declared
action against the approved plan: confirmed calls are preapproved until the
final action succeeds or the plan expires, off-plan calls are denied, and polling the approved plan's
status URL is always allowed.

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

(`$OIDC_TOKEN_URL`, `$CLIENT_ID`, and `$CLIENT_SECRET` are placeholders for
your identity provider's values — export them alongside the command.)

The command may print a raw token (a single string with no whitespace) or JSON such as
`{"access_token":"...","expires_in":3600}`. The `expires_in` field is relative
seconds; `expires_at` (absolute Unix timestamp in seconds or milliseconds) is
also accepted, and either may be a JSON number or a numeric string. A raw
token, or JSON without a usable (positive, numeric) expiry field, is assumed
valid for 55 minutes.

Token lifecycle: the plugin runs the command on the first request, then
caches the token — in memory and on disk at
`$ATRYUM_STATE_DIR/token-cache.json` (mode 0600) so restarts reuse it — and
reuses it until `ATRYUM_TOKEN_REFRESH_SKEW_MS` (default 60s) before expiry,
when it runs the command again to mint a replacement. If a request still gets
a `401`, the plugin bypasses the cache, mints a fresh token, and retries
the request once. The disk cache is keyed to `ATRYUM_TOKEN_COMMAND` and
`ATRYUM_URL` (trailing slashes ignored), so changing either invalidates it;
environment variables referenced by the command are not part of the cache
key. If the command fails
(non-zero exit, timeout after `ATRYUM_TOKEN_COMMAND_TIMEOUT_MS`, empty output,
or invalid token output), the runtime call fails — run the command by hand in
a shell to debug it.

## API used

This plugin only depends on three endpoints atryum exposes for external
executors:

- `POST /api/v1/external/invocations` — submit a tool call for approval (returns immediately with `status: pending_approval`)
- `GET  /api/v1/external/invocations/:id` — poll status (`approved`, `denied`, …)
- `PATCH /api/v1/external/invocations/:id` — report `running` / `completed` / `failed` / `cancelled`

Human approval still happens in the Atryum UI; the plugin itself only calls
the non-admin external executor API above.
