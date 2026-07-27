# Atryum opencode plugin

A minimal [opencode](https://opencode.ai) plugin that routes opencode tool
calls through Atryum before execution and reports completed tool results back
for audit.

opencode runs the tool. Atryum mediates approval and records the decision trail.

OpenCode cannot add slash commands from a plugin at runtime, so this example
ships a companion command file you can install as `/atryum`.

## How it works

```diagram
[ opencode tool.execute.before ]  POST /api/v1/external/invocations  [ Atryum approval ]
[         plugin              ] ----------------------------------> [ pending/review  ]
           |      poll GET /api/v1/external/invocations/:id                 |
           | <--------------------------------------------------------------+
           v
       allow | throw
           |
           |  tool runs in opencode
           v
[ opencode tool.execute.after  ]  PATCH /api/v1/external/invocations/:id
[                              ] ----------------------------------------> audit
                                  {execution_status: completed|failed}
```

## Try it locally

Make sure Atryum is running:

```sh
go run ./cmd/atryum run -config atryum.example.toml
```

Then install the companion `/atryum` command for one project:

```sh
mkdir -p .opencode/commands
cp examples/opencode-plugin/commands/atryum.md .opencode/commands/atryum.md
```

Start opencode in that project and run:

```text
/atryum
```

The command will talk you through setup, ask you to paste a token or token
command, copy the plugin into `.opencode/plugins/atryum.ts`, and write the
secret config to `~/.config/opencode/atryum.json` with mode `0600`. Restart
opencode once it finishes so the plugin can load.

If you prefer the manual path instead, install the plugin for one project:

```sh
mkdir -p .opencode/plugins
cp examples/opencode-plugin/atryum.ts .opencode/plugins/atryum.ts
```

Restart opencode from that project. Pending tool calls will appear at
<http://localhost:8080/ui/> unless your Atryum rules auto-approve or auto-deny
them.

## Install

For the guided setup command in one project:

```sh
mkdir -p .opencode/commands
cp examples/opencode-plugin/commands/atryum.md .opencode/commands/atryum.md
```

For all opencode sessions on the machine, install the plugin globally:

```sh
mkdir -p ~/.config/opencode/plugins
cp examples/opencode-plugin/atryum.ts ~/.config/opencode/plugins/atryum.ts
```

For one project:

```sh
mkdir -p .opencode/plugins
cp examples/opencode-plugin/atryum.ts .opencode/plugins/atryum.ts
```

Alternatively, reference the plugin from `opencode.json`:

```json
{
  "$schema": "https://opencode.ai/config.json",
  "plugin": ["./examples/opencode-plugin/atryum.ts"]
}
```

Restart opencode after installing or changing the plugin. opencode loads
plugins at startup.

## Guided Setup With `/atryum`

The companion command is a thin wrapper around the example plugin and README.
It gives OpenCode enough context to do the boring setup work for you:

1. Copy `examples/opencode-plugin/commands/atryum.md` into `.opencode/commands/`.
2. Start opencode.
3. Run `/atryum`.
4. Follow the prompt, paste a web token or token command, and let OpenCode
   write the config files.
5. Restart opencode.

The command intentionally stores secrets in `~/.config/opencode/atryum.json`
unless you explicitly ask for project-local storage, so you do not accidentally
commit a token to git. It should also leave that file user-readable only
(`chmod 600`).

## Configure

| var | default | meaning |
| --- | --- | --- |
| `ATRYUM_URL` | `http://localhost:8080` | base URL of the Atryum server |
| `ATRYUM_SOURCE` | `opencode` | source label in Atryum |
| `ATRYUM_POLL_MS` | `2000` | approval polling interval |
| `ATRYUM_CLIENT_NAME` | `opencode` | harness name shown in Atryum |
| `ATRYUM_CLIENT_VERSION` | `OPENCODE_VERSION` if set | harness version shown in Atryum |
| `ATRYUM_AGENT_ID` | _(empty)_ | self-declared agent identifier; matched against Agent Record `agent_ids` |
| `ATRYUM_ACCESS_TOKEN` | _(empty)_ | optional OAuth bearer token for Atryum agent runtime APIs |
| `ATRYUM_TOKEN_COMMAND` | _(empty)_ | optional command run to mint each new token; prints a raw token with no whitespace or OAuth token JSON with `access_token` |
| `ATRYUM_TOKEN_REFRESH_SKEW_MS` | `60000` | refresh command cache skew before token expiry |
| `ATRYUM_TOKEN_COMMAND_TIMEOUT_MS` | `10000` | timeout for the token command subprocess |
| `ATRYUM_STATE_DIR` | `~/.atryum/opencode-plugin-state` | directory for the on-disk token cache (`token-cache.json`, mode 0600) |

The plugin also reads JSON config files from these locations:

- `~/.config/opencode/atryum.json`
- `.opencode/atryum.json`

Project config overrides global config. Environment variables override both.
The JSON keys mirror the env vars in camelCase form, for example:

```json
{
  "url": "https://atryum.example.com",
  "accessToken": "paste-your-token-here"
}
```

## Tagging invocations to an Agent Record

By default the plugin sends no agent identity, so invocations show up in the UI
unattached to any Agent Record. To populate the agent column and make
agent-scoped approval rules apply:

1. In the Atryum UI, open an Agent Record or create one, then add a stable
   string to its **Agent IDs** field, such as `opencode-local` or
   `opencode-alice`.
2. Export the same string in your shell:

   ```sh
   export ATRYUM_AGENT_ID=opencode-local
   ```

3. Re-run opencode. Future invocations carry `agent_id: "opencode-local"`;
   Atryum looks it up via `agents.agent_ids @> ["opencode-local"]` and tags
   the row.

This is a self-declared identity. Anyone with network access to the Atryum API
can claim any agent id. For verified identity, run Atryum behind OAuth and
authenticate the plugin instead.

## OAuth Setup

When Atryum is protected by one or more `[[auth]]` blocks, the plugin must send
`Authorization: Bearer ...` on every agent-runtime request.

For a long-lived token, set it directly in one of the supported config sources:

```sh
export ATRYUM_ACCESS_TOKEN=<oauth-access-token>
```

Or store it in `~/.config/opencode/atryum.json`:

```json
{
  "accessToken": "<oauth-access-token>"
}
```

If you create that file by hand, lock it down afterward:

```sh
chmod 600 ~/.config/opencode/atryum.json
```

For short-lived tokens, set `ATRYUM_TOKEN_COMMAND` instead (if both are set,
`ATRYUM_TOKEN_COMMAND` wins and `ATRYUM_ACCESS_TOKEN` is ignored). This is a
shell command the plugin runs whenever it needs to mint a new token —
typically a client-credentials request against your identity provider's token
endpoint, or a CLI that prints one:

```sh
export ATRYUM_TOKEN_COMMAND='curl -fsS -X POST "$OIDC_TOKEN_URL" \
  -d grant_type=client_credentials \
  -d client_id="$CLIENT_ID" \
  -d client_secret="$CLIENT_SECRET" \
  -d scope=atryum:mcp'
```

The command may print a raw token (a single string with no whitespace) or JSON
such as `{"access_token":"...","expires_in":3600}`. The `expires_in`
field is relative seconds; `expires_at` (absolute Unix timestamp in seconds or
milliseconds) is also accepted, and either may be a JSON number or a numeric
string. A raw token, or JSON without a usable positive expiry field, is assumed
valid for 55 minutes.

Token lifecycle: the plugin runs the command on the first request, then caches
the token in memory and on disk at `$ATRYUM_STATE_DIR/token-cache.json` (mode
0600) so restarts can reuse it. It refreshes the token
`ATRYUM_TOKEN_REFRESH_SKEW_MS` before expiry, and if a request still gets a
`401`, it bypasses the cache, mints a fresh token, and retries the request
once.

## API used

This plugin uses Atryum's external executor API:

- `POST /api/v1/external/invocations` submits the pending opencode tool call.
- `GET /api/v1/external/invocations/:id` waits for approval or denial.
- `PATCH /api/v1/external/invocations/:id` records `running`, `completed`,
  `failed`, or best-effort `cancelled` execution state.

## Notes

The public opencode plugin hook surface exposes `tool.execute.before` and
`tool.execute.after`. The before hook is enough to block execution by throwing
an error. The after hook runs after returned tool results, so result auditing is
best effort for tools that abort before returning.
