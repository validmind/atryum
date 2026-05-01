# Atryum

A local-first Go proof of concept for mediating MCP tool calls. Atryum accepts invocation requests, forwards arbitrary tool names to configured upstream MCP servers, and records durable invocation state plus lifecycle events in SQLite.

## Features

- Go binary configured by TOML for process/bootstrap settings only
- Main public endpoint preserved: `POST /mcp/{server}`
- SQLite-backed runtime MCP server registry via `mcp_servers`
- Admin APIs for servers, invocations, and invocation events
- Minimal built-in web UI served from the Go binary at `/ui/` for creating, editing, enabling/disabling, testing, and deleting server connections
- SQLite-backed durable invocation state and lifecycle events
- Request ID and idempotency key support
- Supports both HTTP upstreams and stdio-launched MCP servers
- Business-level tool failures are recorded as failed invocations

## API contract

Public invocation envelope:

```json
{
  "invocation_id": "inv_123",
  "status": "succeeded",
  "approval": null,
  "request_id": "req_123",
  "submitted_at": "2026-04-30T12:00:00Z",
  "completed_at": "2026-04-30T12:00:01Z",
  "result": {"ok": true}
}
```

## Endpoints

Public:
- `POST /mcp/:server`
- `POST /api/v1/invocations`
- `GET /healthz`

Admin APIs:
- `GET /api/v1/admin/invocations?offset=0&limit=50&server=&tool=&status=`
- `GET /api/v1/admin/invocations/:id`
- `GET /api/v1/admin/invocations/:id/events?offset=0&limit=200`
- `GET /api/v1/admin/servers?offset=0&limit=50&enabled=`
- `GET /api/v1/admin/servers/:name`
- `POST /api/v1/admin/servers`
- `PUT /api/v1/admin/servers/:name`
- `DELETE /api/v1/admin/servers/:name`
- `DELETE /api/v1/admin/servers/:name?mode=disable`
- `POST /api/v1/admin/servers/:name/test`

UI:
- `GET /ui/`

## Request shape

Requests to `POST /mcp/:server` and `POST /api/v1/invocations` use:

```json
{
  "tool": "workflows-list",
  "input": {}
}
```

For `/mcp/:server`, the `server` path segment selects the runtime server from SQLite. No tool-to-upstream mapping config is required.

## Managing server connections

Runtime server resolution uses the `mcp_servers` SQLite table as the source of truth.

Manage MCP server connections through:
- the built-in UI at `/ui/`
- the admin server APIs under `/api/v1/admin/servers`

Do not treat TOML as the normal place to add or edit runtime MCP servers.

### Remaining TOML/bootstrap behavior

`[[upstreams]]` entries are bootstrap-only:
- if `mcp_servers` is empty at startup, Atryum seeds it from TOML once for convenience
- if `mcp_servers` already has rows, TOML upstreams are ignored for runtime resolution

After bootstrap, server changes should be made through the UI/API, not by editing TOML.

## Run

```bash
go run ./cmd/atryum -config ./atryum.example.toml
```

Then open:
- `http://localhost:8080/ui/`

In the Servers tab you can:
- create a server
- edit an existing server
- enable or disable a server
- test a server connection
- delete a server

## CLI for manual testing

A small local CLI is available at `cmd/atryum-cli` and targets `POST /mcp/{server}` by default.

```bash
go run ./cmd/atryum-cli --server shortcut --tool workflows-list --input '{}'
```

Equivalent curl example:

```bash
curl -X POST http://localhost:8080/mcp/shortcut \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: demo-1' \
  -d '{"tool":"workflows-list","input":{}}'
```
