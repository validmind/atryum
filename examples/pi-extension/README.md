# Atryum Pi extension

A minimal [Pi](https://pi.dev) extension that routes Pi tool calls through
Atryum before execution and reports the result back for audit.

Pi runs the tool. Atryum mediates approval and records the decision trail.

## How it works

```diagram
[ Pi tool_call ]  POST /api/v1/external/invocations   [ Atryum approval ]
[  extension  ] ------------------------------------> [ pending/review  ]
       |     poll GET /api/v1/external/invocations/:id       |
       | <----------------------------------------------------+
       v
   allow | block
       |
       |  tool runs in Pi
       v
[ tool_execution_end ]  PATCH /api/v1/external/invocations/:id
[                    ] --------------------------------------> audit
                         {execution_status: completed|failed}
```

## Try it locally

Make sure Atryum is running:

```sh
go run ./cmd/atryum run -config atryum.example.toml
```

Then launch Pi with the extension for a one-off smoke test:

```sh
pi -e ./examples/pi-extension/index.ts
```

Open <http://localhost:8080/ui/>. Pi tool calls should appear as pending
invocations unless your Atryum rules auto-approve or auto-deny them.

## Install

For all Pi sessions on the machine:

```sh
./atryum hooks install pi
```

Or copy it manually:

```sh
mkdir -p ~/.pi/agent/extensions/atryum
cp examples/pi-extension/index.ts ~/.pi/agent/extensions/atryum/index.ts
```

For one project:

```sh
mkdir -p .pi/extensions/atryum
cp examples/pi-extension/index.ts .pi/extensions/atryum/index.ts
```

Restart Pi after installing the extension. If Pi is already running, `/reload`
will reload extensions in the auto-discovered extension directories.

To remove the global extension later:

```sh
./atryum hooks uninstall pi
```

## Configure

| var                               | default                        | meaning                                                                                                                    |
| --------------------------------- | ------------------------------ | -------------------------------------------------------------------------------------------------------------------------- |
| `ATRYUM_URL`                      | `http://localhost:8080`        | base URL of the Atryum server                                                                                              |
| `ATRYUM_SOURCE`                   | `pi`                           | source label in Atryum                                                                                                     |
| `ATRYUM_POLL_MS`                  | `2000`                         | approval polling interval                                                                                                  |
| `ATRYUM_CLIENT_NAME`              | `pi`                           | harness name shown in the Atryum Agent column                                                                              |
| `ATRYUM_CLIENT_VERSION`           | `PI_VERSION` if set            | harness version shown in Atryum                                                                                            |
| `ATRYUM_AGENT_ID`                 | _(empty)_                      | self-declared agent identifier; matched against Agent Record `agent_ids`                                                   |
| `ATRYUM_ACCESS_TOKEN`             | _(empty)_                      | optional OAuth bearer token for Atryum agent runtime APIs                                                                  |
| `ATRYUM_TOKEN_COMMAND`            | _(empty)_                      | optional command run to mint each new token; prints a raw token with no whitespace or OAuth token JSON with `access_token` |
| `ATRYUM_TOKEN_REFRESH_SKEW_MS`    | `60000`                        | refresh command cache skew before token expiry                                                                             |
| `ATRYUM_TOKEN_COMMAND_TIMEOUT_MS` | `10000`                        | timeout for the token command subprocess                                                                                   |
| `ATRYUM_STATE_DIR`                | `~/.atryum/pi-extension-state` | directory for the on-disk token cache (`token-cache.json`, mode 0600)                                                      |

## LLM-as-judge session context

The extension does **not** send a free-form chat/context blob to Atryum. The
harness is trusted to report _which_ session a tool call belongs to, but a
runaway agent must not be able to hand the judge arbitrary text to poison it.

Instead, the extension sends Pi's own session id as `client_session_id` on
every `POST /api/v1/external/invocations` and lets Atryum manage the session
server-side. Atryum resolves the internal session with get-or-create keyed by
(agent binding, `client_session_id`) and reconstructs the judge's context from
the prior tool calls it recorded for that session — trusting tool outputs more
than tool inputs, and ignoring agent chat entirely. The extension never mints,
persists, or echoes an Atryum session id.

If the caller has no agent binding, Atryum simply resolves no session and
evaluates the call history-free (tool calls are still gated, just without
prior-call context) rather than blocking the agent.

## Preapproval plans

When plan-scoped rules apply to the current agent, the extension injects plan
submission guidance before the first agent turn after every `session_start`
and also repeats it in blocked tool messages as a fallback. It discovers
support via `GET /api/v1/agent/rules`. The agent can submit a batch plan to
`POST /api/v1/external/plans?source=<source>` (the hint provides the exact
endpoint; the source parameter scopes the plan's actions to this harness so
later tool calls match), wait for approval, and then continue with normal
tool calls. Atryum's adherence judge checks each call matching a declared
action against the approved plan: confirmed calls are preapproved until the
final action succeeds or the plan expires, off-plan calls are denied, and polling the approved plan's
status URL is always allowed.

## Tagging invocations to an Agent Record

By default the extension sends no agent identity, so invocations show up in
the UI unattached to any Agent Record. To populate the agent column and make
agent-scoped approval rules apply:

1. In the Atryum UI, open an Agent Record or create one, then add a stable
   string to its **Agent IDs** field, such as `pi-local` or `pi-alice`.
2. Export the same string in your shell:

   ```sh
   export ATRYUM_AGENT_ID=pi-local
   ```

3. Re-run Pi. Future invocations carry `agent_id: "pi-local"`; Atryum looks
   it up via `agents.agent_ids @> ["pi-local"]` and tags the row.

This is a self-declared identity. Anyone with network access to the Atryum API
can claim any agent id. For verified identity, run Atryum behind OAuth (one or
more `[[auth]]` blocks) and export an OAuth access token instead:

```sh
export ATRYUM_ACCESS_TOKEN=<oauth-access-token>
```

The extension sends it as `Authorization: Bearer ...` on every agent runtime
call. In auth mode Atryum derives the agent id from the token and ignores
`ATRYUM_AGENT_ID`. The token is used as-is and never refreshed — if it
expires, requests fail with `401`.

For short-lived tokens, set `ATRYUM_TOKEN_COMMAND` instead (if both are set,
`ATRYUM_TOKEN_COMMAND` wins and `ATRYUM_ACCESS_TOKEN` is ignored). This is a
shell command the extension runs whenever it needs to mint a new token —
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

Token lifecycle: the extension runs the command on the first request, then
caches the token — in memory and on disk at
`$ATRYUM_STATE_DIR/token-cache.json` (mode 0600) so restarts reuse it — and
reuses it until `ATRYUM_TOKEN_REFRESH_SKEW_MS` (default 60s) before expiry,
when it runs the command again to mint a replacement. If a request still gets
a `401`, the extension bypasses the cache, mints a fresh token, and retries
the request once. The disk cache is keyed to `ATRYUM_TOKEN_COMMAND` and
`ATRYUM_URL` (trailing slashes ignored), so changing either invalidates it;
environment variables referenced by the command are not part of the cache
key. If the command fails
(non-zero exit, timeout after `ATRYUM_TOKEN_COMMAND_TIMEOUT_MS`, empty output,
or invalid token output), the runtime call fails — run the command by hand in
a shell to debug it.

## API used

This extension uses Atryum's external executor API:

- `POST /api/v1/external/invocations` submits the pending Pi tool call.
- `GET /api/v1/external/invocations/:id` waits for approval or denial.
- `PATCH /api/v1/external/invocations/:id` records `running`, `completed`,
  `failed`, or best-effort `cancelled` execution state.

Pi's extension API currently exposes `tool_call` before execution and
`tool_execution_end` after execution, which is enough to gate and audit native
Pi tools without routing the tool implementation itself through Atryum.
