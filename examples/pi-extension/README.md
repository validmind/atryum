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

| var | default | meaning |
| --- | --- | --- |
| `ATRYUM_URL` | `http://localhost:8080` | base URL of the Atryum server |
| `ATRYUM_SOURCE` | `pi` | source label in Atryum |
| `ATRYUM_POLL_MS` | `2000` | approval polling interval |
| `ATRYUM_CLIENT_NAME` | `pi` | harness name shown in the Atryum Agent column |
| `ATRYUM_CLIENT_VERSION` | `PI_VERSION` if set | harness version shown in Atryum |
| `ATRYUM_AGENT_ID` | _(empty)_ | self-declared agent identifier; matched against Agent Record `agent_ids` |
| `ATRYUM_CHAT_MESSAGES_LIMIT` | `100` | recent Pi session chat messages sent as LLM-as-judge context |

## LLM-as-judge chat context

Before each tool call, the extension first reads recent user/assistant
messages from the live Pi session state (`ctx.sessionManager.getBranch()` when
available), then falls back to parsing the session file on disk as a
best-effort backup. It sends that chat log to Atryum as `chat_context`
(and the deprecated `context` alias for compatibility). LLM-as-judge rules
then receive that chat context along with the tool call and tool metadata.

Set `ATRYUM_CHAT_MESSAGES_LIMIT` to change how many recent messages are sent.
Set it to `0` to disable Pi chat context.

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
can claim any agent id. For verified identity, run Atryum behind OAuth and
authenticate the extension instead.

## API used

This extension uses Atryum's external executor API:

- `POST /api/v1/external/invocations` submits the pending Pi tool call.
- `GET /api/v1/external/invocations/:id` waits for approval or denial.
- `PATCH /api/v1/external/invocations/:id` records `running`, `completed`,
  `failed`, or best-effort `cancelled` execution state.

Pi's extension API currently exposes `tool_call` before execution and
`tool_execution_end` after execution, which is enough to gate and audit native
Pi tools without routing the tool implementation itself through Atryum.
