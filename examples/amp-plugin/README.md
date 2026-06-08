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

Drop the plugin into `~/.config/amp/plugins/` and it will gate every amp
session on the machine:

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

## Configure (env vars)

| var               | default                | meaning                                                     |
| ----------------- | ---------------------- | ----------------------------------------------------------- |
| `ATRYUM_URL`      | `http://localhost:8080`| base URL of the atryum server                               |
| `ATRYUM_SOURCE`   | `amp`                  | label shown as the "upstream" column in the atryum UI       |
| `ATRYUM_POLL_MS`  | `2000`                 | how often the plugin polls atryum while awaiting approval   |
| `ATRYUM_AGENT_ID` | _(empty)_              | self-declared agent identifier; matched against Agent Record `agent_ids` (see below) |

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
behind OAuth and authenticate the plugin instead.

## API used

This plugin only depends on three endpoints atryum exposes for external
executors:

- `POST /api/v1/external/invocations` — submit a tool call for approval (returns immediately with `status: pending_approval`)
- `GET  /api/v1/external/invocations/:id` — poll status (`approved`, `denied`, …)
- `PATCH /api/v1/external/invocations/:id` — report `running` / `completed` / `failed` / `cancelled`

The same admin UI and admin endpoints (`/api/v1/admin/invocations/:id/approve|deny`) used for MCP-proxied calls work for these external invocations too.
