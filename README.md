<h1 align="center"> <img width="173" height="51" alt="Atryum" src="https://github.com/user-attachments/assets/c87319f8-215e-4e29-bd0c-f3b1dd9852cd" /> 
    </h1>
    <p align="center">
        <p align="center">The control plane for agent actions.
        </p>
        <p align="center">Self-hosted and lightweight. Every tool your agents call passes through one place to be approved, denied, and logged.</p>
        <p align="center">
        </p>
    </p>
<h4 align="center"> <a href="https://atryum.org/documentation/1_quickstart.html" target="_blank"> QuickStart</a> | <a href="https://validmind.com/platform/agent-authority/">ValidMind Agent Authority</a> | <a href="https://atryum.org" target="_blank">Website</a></h4>
<h4 align="center">

  <img src="https://img.shields.io/badge/license-Apache%202.0-blue?style=flat-square" alt="License: Apache 2.0" />
<img src="https://img.shields.io/badge/version-0.0.5-green?style=flat-square" alt="Version 0.0.5" />
<img src="https://img.shields.io/badge/MCP%20spec-2025--11--25-orange?style=flat-square" alt="MCP spec 2025-11-25" />
    <a href="https://github.com/validmind/atryum" target="_blank">
        <img src="https://img.shields.io/github/stars/validmind/atryum.svg?style=social" alt="GitHub Stars">
    </a>

</h4>



# Atryum

Atryum is a permissions gateway for AI agents. It sits between an agent harness and the tools that harness wants to call, intercepts each tool invocation, evaluates it against configured rules, and either auto-approves, auto-denies, or routes it to a human for approval. Every decision and every outcome is recorded as a durable invocation with a full lifecycle event log.

Atryum can intercept raw tool calls from the harness, proxy mcp tool calls, or coordinate approvals in an agent orchestrator like Claude Managed Agents. 

<img width="2098" height="804" alt="image" src="https://github.com/user-attachments/assets/c414b421-b7e0-448e-b791-e751eb2af641" />

Ready to install and try it? Follow the [quickstart](https://atryum.org/documentation/1_quickstart.html) for a 5 minute tour!

--- 
## How agents reach Atryum

Atryum mediates three kinds of tool calls:

- **Pre-tool hooks from agent harnesses.** Managed harnesses (Claude Code, Cursor, amp, Pi) and autonomous ones (Microsoft Foundry, custom orchestrators) post their intended tool call to `POST /api/v1/external/invocations` (when the harness executes the tool itself) or `POST /api/v1/invocations` (when Atryum should execute it). The harness blocks on the response and only proceeds if Atryum returns an approved status. In the hook path Atryum never touches the tool — it just answers "may this call happen."
- **Direct MCP proxying.** Agents that speak MCP connect to `POST /mcp/{server}` as their MCP endpoint. Atryum implements the JSON-RPC surface (`initialize`, `notifications/initialized`, `tools/list`, `tools/call`) and proxies calls to the configured upstream — HTTP or stdio. Because Atryum is the MCP client to the upstream, it holds the credentials (OAuth tokens, bearer tokens, custom headers) and the agent never sees them. The same approval engine runs on every `tools/call`.
- **Claude Managed Agents events bridge.** Anthropic's hosted harness runs the agent loop on its own infrastructure and never calls Atryum, so for those sessions Atryum dials *out*: it discovers linked Claude sessions, streams their [events](https://platform.claude.com/docs/en/managed-agents/events-and-streaming), records the raw session events on a synthetic audit invocation, and — when the session blocks on a tool call (`session.status_idle` / `requires_action`) — runs the normal approval rules and answers Claude with a `user.tool_confirmation` (or `user.custom_tool_result`). Each tool call is also recorded as its own invocation. This gates both built-in and MCP tools. Enable it by declaring one or more `[[managed_agents]]` accounts (each with a `name`, `workspace`, and `api_key`) and link Claude agents from the Agents UI. Manual session registration remains available at `POST /api/v1/admin/managed-agents/sessions`. See `examples/managed-agents/`.

These paths converge on a single service so rules, audit, and the UI work identically regardless of how the call arrived.

## The rule engine

<img width="2192" height="927" alt="rules_example" src="https://github.com/user-attachments/assets/6453d060-8a69-4e70-966d-29d97d7e0fed" />


Rules live in the `approval_rules` table and are evaluated in priority order (lowest `rule_order` first). Each rule has:

- **Match dimensions** — server patterns, tool patterns, agent ID pattern (the authenticated identity from the harness's JWT), and agent record IDs. An empty list or `"*"` means "match any."
- **Action** — one of:
  - `auto_approve` — execute immediately, record as auto-approved.
  - `auto_deny` — refuse the call, record the reason.
  - `human_approval` — park in `pending_approval`; a human approves or denies via the UI/API.
- **Order** — first matching rule wins. If no rule matches, the configured default policy provider decides (`always_approve`, `manual_approval`, or `always_deny`; `manual_approval` is the recommended default — it fails closed).

## Invocation lifecycle

Every tool call is a durable invocation row with status transitions logged as `invocation_events`:


<img width="1769" height="647" alt="image" src="https://github.com/user-attachments/assets/40ae26d8-9ab9-420e-acfe-13d4cda52c7b" />


The approval record tracks status (auto_approved, auto_denied, approved, denied), reason, actor (for human decisions), and decision timestamp. The matched rule's ID is recorded alongside for audit.

External invocations (the hook path where the harness executes the tool itself) have additional `running`, `completed`, `failed`, `cancelled` execution states reported via `PATCH /api/v1/external/invocations/{id}`.

## Agent identity

Three pieces of identity travel with every invocation:

- **Authenticated agent ID** — the configured claim on the bearer token presented by the harness. By default Atryum uses `client_id`, then falls back to `azp`, then `sub`. Used for `agent_id_pattern` matching.
- **Agent record** — an optional local row in the `agents` table that maps one or more authenticated agent IDs to a named agent for organizational rule targeting.
- **Client info** — `clientInfo.name` and `clientInfo.version` from the MCP `initialize` handshake (e.g., `claude-code 1.2.3`, `cursor 0.42`, `amp 0.0.1234`). Captured even when auth is disabled, so anonymous traffic is still attributable to a harness.

Auth is OIDC-based and supports multiple authorization servers concurrently (Keycloak, Auth0, etc.) — see `[[auth]]` blocks. API-key-protected legacy endpoints (`/agent_ids`, `/invocations/{agent_id}`) exist for tooling that hasn't moved to bearer tokens yet.

For local no-auth runtime calls, when no `[[auth]]` blocks are configured, callers may provide a best-effort agent identity with `?agent_id=` on `/mcp/{server}`, `/api/v1/invocations`, and `/api/v1/agent/rules`, or with `agent_id` in external invocation API payloads. For example: `http://localhost:8080/mcp/shortcut?agent_id=hunners-codex`. This ID is ignored as soon as inbound auth is configured.

When inbound auth is configured, all agent runtime surfaces require OAuth bearer tokens: `/mcp/{server}`, `/api/v1/invocations`, `/api/v1/external/invocations`, `/api/v1/external/invocations/{id}`, and `/api/v1/agent/rules`. The Amp and Pi examples, plus the shared hook script installed for agent hooks, read `ATRYUM_ACCESS_TOKEN` and send it as `Authorization: Bearer ...` — the token is used as-is and never refreshed. For short-lived tokens, set `ATRYUM_TOKEN_COMMAND` instead: a shell command that mints a fresh token (raw, or OAuth token JSON with `access_token` plus optional `expires_in`) — typically a client credentials request against your identity provider's token endpoint. The integrations run it on the first request, cache the token, run it again shortly before expiry, and retry once with a freshly minted token after a `401`. If both are set, `ATRYUM_TOKEN_COMMAND` wins and `ATRYUM_ACCESS_TOKEN` is ignored.

The Settings UI can also select a default ValidMind agent record. AI Evaluation uses that record when an incoming runtime agent ID is missing or does not map to a synced agent, allowing local no-auth runs to evaluate against a known charter without adding TOML.

## Admin authentication

Admin UI and admin API authentication is optional. When no `[[auth]]` block has `admin_enabled = true`, the admin UI/API behave as before and remain open. When one or more blocks are admin-enabled, Atryum requires a browser OIDC access token for admin API calls and accepts tokens from any admin-enabled issuer. The upstream MCP OAuth callback at `/api/v1/mcp/oauth/callback` remains public so external identity providers can complete browser redirects.

Admin auth reuses the same issuer/audience/JWKS validation as agent auth, then checks the admin claim configured on the matched `[[auth]]` block. This means different IdPs can use different admin claims at the same time.

Example:

```toml
[[auth]]
enabled = true
issuer = "https://your-tenant.us.auth0.com/"
audience = "https://api.example.com/atryum"
agent_id_claim = "client_id"

admin_enabled = true
admin_provider = "auth0"
admin_client_id = "atryum-admin-spa-client"
admin_scopes = "openid profile email offline_access"
admin_claim = "atryum_admin"
admin_claim_value = "true"

[[auth]]
enabled = true
issuer = "http://localhost:8089/realms/atryum"
audience = "atryum"
required_scope = "atryum:mcp"
agent_id_claim = "client_id"

admin_enabled = true
admin_provider = "keycloak"
admin_client_id = "atryum-admin"
admin_scopes = "openid profile email atryum:mcp"
admin_claim = "atryum_admin"
admin_claim_value = true
```

The admin client must be a browser-safe public SPA client that supports authorization code with PKCE. For Auth0, create a Single Page Application client and allow `http://localhost:5174/ui/auth/callback` for Vite development and `http://localhost:8080/ui/auth/callback` for the embedded UI. For local Keycloak, run `KC_URL=http://localhost:8089 ./keycloak/setup-realm.sh`; it provisions the `atryum-admin` public client and an `atryum_admin=true` access-token claim.

The frontend fetches `/api/v1/admin-auth/config`, shows a sign-in screen, redirects through the selected provider, attaches `Authorization: Bearer <access_token>` to admin API calls, and uses authenticated fetch-based SSE for `/api/v1/admin/invocations/stream`. With one configured provider, the screen skips the provider selector; with several, it shows an identity-provider selector. It attempts silent token refresh before retrying an expiry-related `401` once. Browser console debug logs are emitted for refresh attempts and outcomes under the `[admin-auth]` prefix; access token values are never logged.

## HTTP surface

Public (auth-protected when `[[auth]]` is configured):

- `POST /mcp/{server}` — MCP JSON-RPC. `tools/list` annotates each tool with its policy disposition for the calling agent; a synthetic `atryum_rules_get` tool lets an agent inspect its applicable rules before deciding what to call, and when plan-scoped rules apply it also lists synthetic `atryum.plan.submit` and `atryum.plan.get` tools so the agent can submit and poll a batch plan before running tools. The rules helper uses an underscore because dotted MCP tool names are spec-compliant, but some common harness implementations reject them.
- `GET /mcp/{server}` — Streamable HTTP / legacy SSE channel for MCP clients that need a long-lived event stream.
- `POST /api/v1/invocations` — direct invocation (Atryum executes).
- `POST /api/v1/external/invocations`, `PATCH /api/v1/external/invocations/{id}` — hook path (harness executes, Atryum gates and records).
- `POST /api/v1/external/plans`, `GET /api/v1/external/plans/{id}` — agent-submitted preapproval plans for batches of intended tool calls. Each action receives a unique `action_id`. Once a plan is approved, tool calls matching its declared actions are checked by an adherence judge against both the plan and the agent charter — only calls that satisfy both auto-approve; off-plan or charter-violating calls are denied. Send `plan_action_id` with a call when multiple steps use the same tool/server. Steps may be skipped, but after a later step runs, earlier steps are denied; polling the plan's own status always passes.
- `GET /api/v1/agent/rules` — agent-facing rule introspection.
- `GET /healthz` — liveness.

Admin (UI and operators):

- `/api/v1/admin/invocations`, `/{id}`, `/{id}/events`, `/{id}/approve`, `/{id}/deny`, `/stream` (SSE)
- `/api/v1/admin/servers`, `/{name}`, `/{name}/test`, `/{name}/connect`, `/{name}/connect/status`
- `/api/v1/admin/rules`, `/{id}` (including reorder/move)
- `/api/v1/admin/agents`, `/{id}`
- `/api/v1/admin/settings`, `/api/v1/admin/policy`
- `/api/v1/admin/managed-agents/accounts`, `/managed-agents/agents` — discover configured Anthropic accounts and Claude agents for UI linking
- `/api/v1/admin/managed-agents/sessions` — manually register a Claude Managed Agents session for the events bridge to watch; kept as a debugging escape hatch
- `/api/v1/mcp/oauth/callback` — public OAuth callback for upstream MCP server connect flows
- `/api/v1/admin-auth/config` — public, non-secret OIDC metadata for the admin UI login screen

## Frontend

The local React UI lives under `ui/`. `just build-ui` builds it and copies the Vite output into `internal/api/web`, which is embedded into the Go binary and served at `/ui/`. It covers servers, rules, invocations (list + detail with live SSE updates), agent records, and settings.

The embedded invocation view subscribes to the admin SSE stream, updates list/detail/event views live, surfaces stored input arguments, and renders MCP-style text content in a friendly view alongside raw JSON.

## Storage

SQLite by default, PostgreSQL optional via `server.database_url`. Both are first-class — migrations live in `internal/store/migrations/` and apply at startup. Core tables:

- `mcp_servers` — upstream connection settings and generic auth/connection status, including `connection_status`, `auth_status`, `reauth_needed`, `auth_type`, `last_checked_at`, `last_check_ok`, `last_error_summary`, and `action_required`.
- `oauth_credentials` and related OAuth client registration tables — tokens and client registrations held by Atryum on behalf of agents.
- `invocations` — durable per-call state, including matched rule, agent identity, harness clientInfo, approval record, and request/response/error payloads.
- `invocation_events` — append-only lifecycle events.
- `approval_rules` — the rule engine.
- `agents` — local agent records and their authenticated-ID mappings.
- `managed_agent_bindings` — local Atryum agent to Claude Managed Agent links used for session discovery.
- `managed_agent_sessions` — Claude Managed Agents sessions watched by the events bridge, with each one's event cursor for resume-after-restart.

## Config

A single TOML file configures process and bootstrap settings; runtime entities (servers, rules, agents) live in the database and are managed via the UI/API.

```toml
[server]
listen_addr     = ":8080"
public_base_url = "http://localhost:8080" # browser-facing API URL for OAuth callbacks
atryum_instance = ""                      # stable metadata identity; defaults to public_base_url
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

[[managed_agents]]                 # optional, repeatable — one per Anthropic account/workspace key
name     = "default"               # unique label; the session-registration "account" targets it
workspace = ""                     # required label when api_key is set; used for display/metadata
api_key  = ""                      # Anthropic API key created in that workspace; empty entries are skipped
recent_chat_messages_limit = 100   # recent chat messages supplied as LLM-as-judge context
                                   # env override (single account only): ATRYUM_MANAGED_AGENTS_API_KEY, then ANTHROPIC_API_KEY; workspace label via ATRYUM_MANAGED_AGENTS_WORKSPACE

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
docker compose --profile dev  up    # backend :8080 + Vite dev UI :5174/ui/
docker compose --profile prod up    # one binary, embedded UI :8080
```

`docker compose up` alone starts only Postgres. Use `docker compose down -v` between profile switches if you hit port or network state weirdness.

## Debugging

```bash
ATRYUM_MCP_DEBUG=1                                # concise MCP proxy activity
ATRYUM_AUTH_DEBUG_SKIP_VERIFY=1                   # local only — bypass inbound auth on agent runtime APIs
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
