# Atryum

A local-first Go proof of concept for mediating MCP tool calls. Atryum accepts invocation requests, forwards arbitrary tool names to configured upstream MCP servers, and records durable invocation state plus lifecycle events in SQLite.

## Features

- Go binary configured by TOML
- Main public endpoints: `POST /mcp/{server}`
- Admin inspection endpoints for listing invocations and events
- SQLite-backed durable invocation state
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

- `POST /mcp/:server`
- `POST /api/v1/invocations` (optional/internal-compatible)
- `GET /api/v1/admin/invocations`
- `GET /api/v1/admin/invocations/:id`
- `GET /api/v1/admin/invocations/:id/events`
- `GET /healthz`

## Request shape

Requests to `POST /mcp/:server` and `POST /api/v1/invocations` use:

```json
{
  "tool": "workflows-list",
  "input": {}
}
```

For `/mcp/:server`, the `server` path segment selects the configured upstream. No tool-to-upstream routing config is needed.

## Config modes

Each upstream can run in one of two modes:

- `mode = "http"` for an HTTP MCP proxy endpoint
- `mode = "stdio"` for a locally launched MCP server command

### Example stdio Shortcut config

```toml
[[upstreams]]
name = "shortcut"
mode = "stdio"
command = "npx"
args = ["-y", "@shortcut/mcp@latest"]
timeout_seconds = 60
enabled = true

[upstreams.env]
SHORTCUT_API_TOKEN = "replace-me"
SHORTCUT_READONLY = "true"
```

## Run

```bash
go mod tidy
go run ./cmd/atryum -config ./atryum.example.toml
```

## CLI for manual testing

A small local CLI is available at `cmd/atryum-cli` and targets `POST /mcp/{server}` by default.

```bash
go run ./cmd/atryum-cli --server shortcut --tool workflows-list --input '{}'
```

Useful flags:
- `--base-url http://localhost:8080`
- `--server shortcut`
- `--tool workflows-list`
- `--input '{}'`
- `--request-id req-123`
- `--idempotency-key demo-1`

Equivalent curl example:

```bash
curl -X POST http://localhost:8080/mcp/shortcut \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: demo-1' \
  -d '{"tool":"workflows-list","input":{}}'
```

## How stdio Shortcut mode works

When an upstream is configured with `mode = "stdio"`, Atryum:
1. launches the configured command locally
2. sends MCP JSON-RPC messages over stdin/stdout
3. performs `initialize`
4. sends `tools/call` for the requested tool
5. captures the result and stores invocation state and events in SQLite

## Notes

- You must provide a valid `SHORTCUT_API_TOKEN` to use the Shortcut MCP server.
- `SHORTCUT_READONLY=true` is recommended for local proof-of-concept use.
- The stdio implementation is intentionally minimal and currently launches a fresh MCP process per invocation.
- Approvals are not implemented yet, but the API reserves future-friendly approval fields such as `request_id` and `expires_at`.
