# Atryum

A local-first Go proof of concept for mediating MCP tool calls. Atryum accepts invocation requests, forwards arbitrary tool names to configured upstream MCP servers, and records durable invocation state plus lifecycle events in SQLite by default, with optional PostgreSQL support.

## Features

- Go binary configured by TOML for process/bootstrap settings only
- Main public endpoint preserved: `POST /mcp/{server}`
- `/mcp/{server}` now supports a minimal MCP-over-HTTP JSON-RPC surface for Claude/MCP clients
- SQLite-backed runtime MCP server registry via `mcp_servers` by default; PostgreSQL is selectable with `server.database_url`
- Admin APIs for servers, invocations, and invocation events
- Generic connection/auth status surfaced for MCP servers, including reauth-needed state
- Minimal built-in web UI served from the Go binary at `/ui/` for creating, editing, enabling/disabling, testing, and deleting server connections
- Invocation UI renders MCP text content in a human-friendly view alongside raw JSON and refreshes live
- Invocation APIs surface stored input arguments in invocation detail/list responses and invocation event payloads for approval inspection
- SQLite-backed durable invocation state and lifecycle events by default; PostgreSQL is supported via pgx stdlib
- Request ID and idempotency key support
- Supports both HTTP upstreams and stdio-launched MCP servers
- Business-level tool failures are recorded as failed invocations

## MCP proxy support

`POST /mcp/{server}` accepts JSON-RPC 2.0 MCP-style requests.

Currently implemented methods:
- `initialize`
- `notifications/initialized`
- `tools/list`
- `tools/call`

Behavior:
- `initialize` returns Atryum server info/capabilities
- `tools/list` resolves the DB-backed server and asks the upstream for its tools where practical
- `tools/call` proxies to the upstream while reusing Atryum invocation logging, so admin/UI visibility remains intact
- responses from `/mcp/{server}` are MCP-compatible JSON-RPC responses, not the custom invocation envelope

The older custom/testing invocation path remains available at:
- `POST /api/v1/invocations`

## Debug logging

Set:

```bash
ATRYUM_MCP_DEBUG=1
```

to log concise MCP proxy activity in local run output. Current debug logging includes:
- server name
- method name
- request id
- transport mode
- duration
- concise result/error info

Secrets are not intentionally logged.

## Docker Compose

Two profiles, mutually exclusive (postgres always runs):

```bash
docker compose --profile dev up    # Go server + Vite dev frontend (HMR on :5175)
docker compose --profile prod up   # Single Go binary with embedded prod UI (:8080)
```

- **dev** (`Dockerfile`): backend at `:8080`, separate Vite dev server at `:5175`, source bind-mounted from `../frontend`. Use this for active development.
- **prod** (`Dockerfile.prod`): multi-stage build — Vite builds the React app, Go embeds the output via `//go:embed` and serves the SPA from `/ui/` (with router fallback). One binary, one port. `atryum.toml` is baked into the image.

Bare `docker compose up` only starts postgres. Run `docker compose down -v` between profile switches if you hit port or network weirdness.


## Auth debug mode

For local OAuth troubleshooting, Atryum can completely bypass inbound auth on
`/mcp/`:

```toml
[auth_debug]
skip_verify = true
```

or:

```bash
ATRYUM_AUTH_DEBUG_SKIP_VERIFY=1 go run ./cmd/atryum -config atryum.auth0.toml
```

When enabled, the `Authorization` header is ignored entirely on `/mcp/`: no
bearer token is required, no claims are parsed, and no agent identity is set
on the request context. Requests reach the upstream MCP server as if no
`[[auth]]` section had been configured.

This is only for local debugging. Do not enable it in shared or production
environments.

## Backend connection check

Atryum can verify its ValidMind backend machine-user credentials during
startup:

```toml
[backend]
base_url = "https://api.example.validmind.ai"
machine_key = "replace-me"
machine_secret = "replace-me"
connection_timeout_seconds = 5
```

The same values can be supplied with environment variables, which override
TOML: `VM_BASE_URL`, `VM_MACHINE_KEY`, `VM_MACHINE_SECRET`, and
`VM_CONNECTION_TIMEOUT_SECONDS`.

When `backend.base_url` is empty, the check is skipped for local standalone
runs. When it is set, startup fails if credentials are missing or the backend
rejects `GET /internal/v1/atryum/connection`.

## Database configuration

SQLite remains the default storage provider:

```toml
[server]
database_path = "./atryum.db"
database_url = ""
```

Set `server.database_url` to select a provider by URL scheme:
- `postgres://` or `postgresql://` uses PostgreSQL via pgx stdlib
- `sqlite://`, `file:`, no scheme, or an empty URL uses SQLite

Optional local PostgreSQL test/dev DSN:

```toml
[server]
database_url = "postgresql://postgres:password@127.0.0.1:5432/postgres"
```

Normal tests do not require PostgreSQL. To run the optional PostgreSQL integration test locally:

```bash
ATRYUM_POSTGRES_TESTS=1 go test ./internal/store
# optionally override the DSN:
ATRYUM_POSTGRES_TESTS=1 ATRYUM_POSTGRES_TEST_DSN="postgresql://postgres:password@127.0.0.1:5432/postgres" go test ./internal/store
```

## Endpoints

Public:
- `POST /mcp/:server`
- `POST /api/v1/invocations`
- `GET /api/v1/agent/rules?server=&source=&tool=&agent_id=&request_id=` - returns enabled approval rules applicable to the caller; protected by the same bearer auth as `/mcp/` when auth is configured
- `GET /healthz`

MCP clients connected through `/mcp/` also see a synthetic `atryum.rules.get`
tool in `tools/list`. It returns the same rules payload without creating an
approval-gated invocation, so agents can inspect their rule environment before
choosing which tool to call.

Admin APIs:
- `GET /api/v1/admin/invocations?offset=0&limit=50&server=&tool=&status=`
- `GET /api/v1/admin/invocations/stream`
- `GET /api/v1/admin/invocations/:id`
- `GET /api/v1/admin/invocations/:id/events?offset=0&limit=200`
- `GET /api/v1/admin/servers?offset=0&limit=50&enabled=`
- `GET /api/v1/admin/servers/:name`
- `POST /api/v1/admin/servers`
- `PUT /api/v1/admin/servers/:name`
- `DELETE /api/v1/admin/servers/:name`
- `DELETE /api/v1/admin/servers/:name?mode=disable`
- `POST /api/v1/admin/servers/:name/test`
- `POST /api/v1/admin/servers/:name/connect`
- `GET /api/v1/admin/servers/:name/connect/status`
- `GET /api/v1/admin/oauth/callback`

UI:
- `GET /ui/`

## Built-in UI invocation improvements

The invocations page now:
- subscribes to a simple SSE stream for live invocation updates
- updates the invocation list live without a manual refresh
- refreshes selected invocation detail/events on incoming updates
- surfaces stored invocation input arguments in the invocation payload and event payload views
- renders human-friendly text boxes when invocation result/error/event JSON contains MCP-style text content such as `{ "type": "text", "text": "..." }`
- preserves line breaks in those extracted text blocks while still showing the raw JSON below
- keeps raw/friendly selection and friendly expand/collapse state stable across live updates

## Managing server connections

Runtime server resolution uses the `mcp_servers` SQLite table as the source of truth.

Manage MCP server connections through:
- the built-in UI at `/ui/`
- the admin server APIs under `/api/v1/admin/servers`

Do not treat TOML as the normal place to add or edit runtime MCP servers.

In the built-in UI, disabled servers are now shown by default so disabling a server does not make it appear to disappear.

## OAuth-style connect/reconnect flow

For HTTP-mode servers that use hosted OAuth, Atryum owns the browser connect flow.

The normal UI path is now URL-first and provider-driven:
- admins enter the server URL
- Atryum detects or assigns an auth provider where practical
- the UI shows provider/status plus `Connect` / `Reconnect`
- the normal UI does not require admins to fill in raw authorize/token URLs or scopes

Runtime behavior:
- connection metadata lives in `mcp_servers`
- OAuth tokens are stored separately in SQLite
- `Connect` / `Reconnect` is launched from the built-in UI
- callback completion updates server auth status fields in the DB
- missing/expired OAuth auth fails fast with actionable `auth_status` / `reauth_needed` / `action_required`

Current pragmatic provider support:
- `github_hosted` for GitHub-style hosted HTTP servers detected from URL
- a generic provider framework exists for future providers/discovery work

## Server auth and connection status semantics

Each server now exposes generic status metadata suitable for current token/stdio servers and future hosted or OAuth-like servers:
- `connection_status`
- `auth_status`
- `reauth_needed`
- `auth_type`
- `last_checked_at`
- `last_check_ok`
- `last_error_summary`
- `action_required`

`POST /api/v1/admin/servers/{name}/test` updates these fields in the DB and returns the latest status snapshot.

## Remaining TOML/bootstrap behavior

`[[upstreams]]` entries are bootstrap-only:
- if `mcp_servers` is empty at startup, Atryum seeds it from TOML once for convenience
- if `mcp_servers` already has rows, TOML upstreams are ignored for runtime resolution

After bootstrap, server changes should be made through the UI/API, not by editing TOML.
