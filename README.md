# Atryum

Atryum is a permissions gateway for AI agents. It sits between an agent harness and the tools that harness wants to call, intercepts each tool invocation, evaluates it against configured rules, and either auto-approves, auto-denies, or routes it to a human for approval. Every decision and every outcome is recorded as a durable invocation with a full lifecycle event log.

Atryum runs standalone and enforces locally. It is the open-source nervous system: the interception path, the rule engine, the approval UI, and the audit store. Higher-order policy reasoning (an agent registry, per-agent charters, LLM-as-judge evaluation) lives in ValidMind, the commercial product that consumes Atryum's surface.

## How agents reach Atryum

Atryum mediates two kinds of tool calls:

- **Pre-tool hooks from agent harnesses.** Managed harnesses (Claude Code, Cursor, amp) and autonomous ones (Microsoft Foundry, custom orchestrators) post their intended tool call to `POST /api/v1/external/invocations` (when the harness executes the tool itself) or `POST /api/v1/invocations` (when Atryum should execute it). The harness blocks on the response and only proceeds if Atryum returns an approved status. In the hook path Atryum never touches the tool — it just answers "may this call happen."
- **Direct MCP proxying.** Agents that speak MCP connect to `POST /mcp/{server}` as their MCP endpoint. Atryum implements the JSON-RPC surface (`initialize`, `notifications/initialized`, `tools/list`, `tools/call`) and proxies calls to the configured upstream — HTTP or stdio. Because Atryum is the MCP client to the upstream, it holds the credentials (OAuth tokens, bearer tokens, custom headers) and the agent never sees them. The same approval engine runs on every `tools/call`.

The two paths converge on a single service so rules, audit, and the UI work identically regardless of how the call arrived.

## The rule engine

Rules live in the `approval_rules` table and are evaluated in priority order (lowest `rule_order` first). Each rule has:

- **Match dimensions** — server patterns, tool patterns, agent ID pattern (the authenticated identity from the harness's JWT), and agent record IDs. An empty list or `"*"` means "match any."
- **Action** — one of:
  - `auto_approve` — execute immediately, record as auto-approved.
  - `auto_deny` — refuse the call, record the reason.
  - `human_approval` — park in `pending_approval`; a human approves or denies via the UI/API.
- **Order** — first matching rule wins. If no rule matches, the configured default policy provider decides (`always_approve`, `manual_approval`, or `always_deny`; `manual_approval` is the recommended default — it fails closed).

## Invocation lifecycle

Every tool call is a durable invocation row with status transitions logged as `invocation_events`:

```
received → executing → succeeded | failed
        ↘ pending_approval → approved → executing → succeeded | failed
                          ↘ denied
                          ↘ expired
                          ↘ cancelled
```

The approval record tracks status (auto_approved, auto_denied, approved, denied), reason, actor (for human decisions), and decision timestamp. The matched rule's ID is recorded alongside for audit.

External invocations (the hook path where the harness executes the tool itself) have additional `running`, `completed`, `failed`, `cancelled` execution states reported via `PATCH /api/v1/external/invocations/{id}`.

## Agent identity

Three pieces of identity travel with every invocation:

- **Authenticated agent ID** — the `sub` (or configured claim) on the bearer token presented by the harness. Used for `agent_id_pattern` matching.
- **Agent record** — an optional local row in the `agents` table that maps one or more authenticated agent IDs to a named agent for organizational rule targeting.
- **Client info** — `clientInfo.name` and `clientInfo.version` from the MCP `initialize` handshake (e.g., `claude-code 1.2.3`, `cursor 0.42`, `amp 0.0.1234`). Captured even when auth is disabled, so anonymous traffic is still attributable to a harness.

Auth is OIDC-based and supports multiple authorization servers concurrently (Keycloak, Auth0, etc.) — see `[[auth]]` blocks. API-key-protected legacy endpoints (`/agent_ids`, `/invocations/{agent_id}`) exist for tooling that hasn't moved to bearer tokens yet.

For local no-auth MCP runs, when no `[[auth]]` blocks are configured, callers may provide a best-effort agent identity with `?agent_id=` on `/mcp/{server}` and `/api/v1/agent/rules`. For example: `http://localhost:8080/mcp/shortcut?agent_id=hunners-codex`. This ID is ignored as soon as inbound auth is configured.

The Settings UI can also select a default ValidMind agent record. AI Evaluation uses that record when an incoming runtime agent ID is missing or does not map to a synced agent, allowing local no-auth runs to evaluate against a known constitution without adding TOML.

## HTTP surface

Public (auth-protected when `[[auth]]` is configured):

- `POST /mcp/{server}` — MCP JSON-RPC. `tools/list` annotates each tool with its policy disposition for the calling agent; a synthetic `atryum.rules.get` tool lets an agent inspect its applicable rules before deciding what to call.
- `GET /mcp/{server}` — Streamable HTTP / legacy SSE channel for MCP clients that need a long-lived event stream.
- `POST /api/v1/invocations` — direct invocation (Atryum executes).
- `POST /api/v1/external/invocations`, `PATCH /api/v1/external/invocations/{id}` — hook path (harness executes, Atryum gates and records).
- `GET /api/v1/agent/rules` — agent-facing rule introspection.
- `GET /healthz` — liveness.

Admin (UI and operators):

- `/api/v1/admin/invocations`, `/{id}`, `/{id}/events`, `/{id}/approve`, `/{id}/deny`, `/stream` (SSE)
- `/api/v1/admin/servers`, `/{name}`, `/{name}/test`, `/{name}/connect`, `/{name}/connect/status`
- `/api/v1/admin/rules`, `/{id}` (including reorder/move)
- `/api/v1/admin/agents`, `/{id}`
- `/api/v1/admin/settings`, `/api/v1/admin/policy`
- `/api/v1/admin/oauth/callback` — OAuth callback for upstream MCP server connect flows

## Frontend

The repo currently embeds a minimal built-in React UI under `internal/api/web` served at `/ui/`. The standalone Atryum frontend (in the sibling `frontend/` repo under `src/atryum/`) is being pulled into this repo to replace the embedded UI as the open-source frontend. It covers servers, rules, invocations (list + detail with live SSE updates), agent records, and settings.

The embedded invocation view subscribes to the admin SSE stream, updates list/detail/event views live, surfaces stored input arguments, and renders MCP-style text content in a friendly view alongside raw JSON.

## Storage

SQLite by default, PostgreSQL optional via `server.database_url`. Both are first-class — migrations live in `internal/store/migrations/` and apply at startup. Core tables:

- `mcp_servers` — upstream connection settings and generic auth/connection status, including `connection_status`, `auth_status`, `reauth_needed`, `auth_type`, `last_checked_at`, `last_check_ok`, `last_error_summary`, and `action_required`.
- `oauth_credentials` and related OAuth client registration tables — tokens and client registrations held by Atryum on behalf of agents.
- `invocations` — durable per-call state, including matched rule, agent identity, harness clientInfo, approval record, and request/response/error payloads.
- `invocation_events` — append-only lifecycle events.
- `approval_rules` — the rule engine.
- `agents` — local agent records and their authenticated-ID mappings.

## Config

A single TOML file configures process and bootstrap settings; runtime entities (servers, rules, agents) live in the database and are managed via the UI/API.

```toml
[server]
listen_addr     = ":8080"
database_path   = "./atryum.db"   # or set database_url for Postgres
database_url    = ""              # postgres://, postgresql://, sqlite://, file:, or a SQLite path
log_level       = "info"

[defaults]
request_timeout_seconds = 30

[backend]                         # optional ValidMind backend credential check
base_url = ""
machine_key = ""
machine_secret = ""
connection_timeout_seconds = 5

[[auth]]                           # optional — repeatable per authorization server
issuer    = "https://keycloak.example/realms/agents"
audience  = "atryum"
# jwks_uri, agent_id_claim, etc.

[[upstreams]]                      # bootstrap-only: seeds mcp_servers on first run
name         = "local-reference"
base_url     = "http://localhost:9000"
enabled      = true
```

After first-run bootstrap, edit MCP servers through the UI/API; TOML `[[upstreams]]` is ignored once `mcp_servers` has rows. Disabled servers remain visible in the UI so disabling a server does not make it disappear.

`server.database_url` selects the storage provider by URL scheme. `postgres://` and `postgresql://` use PostgreSQL via pgx stdlib; `sqlite://`, `file:`, an empty URL, or a bare path use SQLite. Normal tests do not require PostgreSQL; run the optional store integration test with `ATRYUM_POSTGRES_TESTS=1 go test ./internal/store`.

When `backend.base_url` is empty, the ValidMind backend connection check is skipped for local standalone runs. When it is set, startup fails if credentials are missing or `GET /api/atryum/unstable/connection` is rejected. Environment variables override TOML: `VM_BASE_URL`, `VM_MACHINE_KEY`, `VM_MACHINE_SECRET`, and `VM_CONNECTION_TIMEOUT_SECONDS`.

## Running

Single-binary Go service.

```bash
go run ./cmd/atryum run -config atryum.toml
```

Docker Compose has two mutually-exclusive profiles (Postgres always runs):

```bash
docker compose --profile dev  up    # backend :8080 + Vite dev frontend :5175
docker compose --profile prod up    # one binary, embedded UI :8080
```

`docker compose up` alone starts only Postgres. Use `docker compose down -v` between profile switches if you hit port or network state weirdness.

## Debugging

```bash
ATRYUM_MCP_DEBUG=1                                # concise MCP proxy activity
ATRYUM_AUTH_DEBUG_SKIP_VERIFY=1                   # local only — bypass inbound auth on /mcp/
```

Or in TOML:

```toml
[auth_debug]
skip_verify = true
```

When `skip_verify` is on, no bearer is required, no claims are parsed, and no agent identity is set on the request. Local debugging only — do not enable in shared environments.

## Server connection management

Runtime server resolution uses the `mcp_servers` table as the source of truth. Manage MCP server connections through the built-in UI at `/ui/` or the admin server APIs under `/api/v1/admin/servers`; do not treat TOML as the normal place to add or edit runtime MCP servers after bootstrap.

For HTTP-mode servers that use hosted OAuth, Atryum owns the browser connect flow. Admins enter the server URL, Atryum detects or assigns an auth provider where practical, and the UI shows provider/status plus `Connect` / `Reconnect`. OAuth tokens are stored separately, callback completion updates server auth status fields in the database, and missing or expired OAuth auth fails fast with actionable status fields. Current pragmatic provider support includes `github_hosted` for GitHub-style hosted HTTP servers plus a generic provider framework for future providers and discovery work.

`POST /api/v1/admin/servers/{name}/test` updates server connection/auth status fields and returns the latest snapshot.

## How it differs from "just an MCP proxy"

A plain MCP proxy forwards JSON-RPC and maybe logs it. Atryum's value is what happens between receipt and forward:

- **Credentials never leave Atryum.** The agent gets the result; the upstream gets a credentialed call; the agent never holds the OAuth token.
- **Every call is a decision, not a pass-through.** Rules match on agent identity, server, tool, and arguments; the decision and its rationale are recorded.
- **The hook path covers what MCP doesn't.** Harness-local tools (file edits, shell, in-process functions) are mediated through the same rule engine via pre-tool hooks, so a single policy spans MCP and non-MCP tools.
- **Audit is the storage layer, not a side channel.** Invocations and their event streams are the canonical state; the UI, the SSE stream, and the per-agent listing are all just views over the same durable rows.
