# Atryum Claude Managed Agents setup

[Claude Managed Agents](https://platform.claude.com/docs/en/managed-agents/overview)
is Anthropic's *hosted* agent harness: the agent loop runs on Anthropic's
infrastructure, and tools execute in an Anthropic-managed (or self-hosted)
sandbox. Unlike Claude Code, it exposes **no `PreToolUse` / `PostToolUse`
hook**, so the command-hook adapter in `examples/claude-code-hook` does not
apply here.

The only integration surface is **MCP**. You declare Atryum's `/mcp/{server}`
endpoint as one of the agent's MCP servers; every `tools/call` the agent makes
to that server is gated by Atryum's rule engine and recorded as an invocation.

```
 ┌──────────────────────────────┐  outbound HTTPS     ┌────────────────────────┐  approval + audit  ┌──────────┐
 │ Anthropic cloud              │  POST /mcp/{server} │ Your Atryum            │───────────────────▶│ upstream │
 │ Managed Agent + sandbox      │────────────────────▶│ /mcp/{server}          │◀── result ─────────│   MCP    │
 │ (Claude runs the agent loop) │◀── JSON-RPC result ─│ (rule engine + audit)  │                    └──────────┘
 └──────────────────────────────┘                     └────────────────────────┘
        │
        └─ bash / read / write / web_fetch ...  ← run in the sandbox, NOT visible to Atryum (cannot be gated)
```

## What this covers

- Managed-agent calls to MCP tools routed through Atryum
- Atryum approval rules, invocation logging, and admin UI for those MCP calls

## What this does not cover

- The built-in agent toolset (`bash`, `read`, `write`, `edit`, `glob`, `grep`,
  `web_fetch`, `web_search`). These run inside Anthropic's sandbox with no
  interception point, so Atryum cannot see or gate them. If you need a tool
  gated, expose an equivalent through an MCP server behind Atryum and omit it
  from the built-in toolset.

## Prerequisites

1. **Atryum reachable from Anthropic's cloud.** The agent runs on Anthropic's
   infrastructure, so it makes the outbound call to Atryum — `localhost:8080`
   will not work. Use a public HTTPS ingress, or an
   [MCP tunnel](https://platform.claude.com/docs/en/agents-and-tools/mcp-tunnels/overview)
   if Atryum sits in a private network.
2. **A bearer token Atryum will accept.** Atryum's `/mcp/` auth validates
   **OIDC JWTs** against a configured `[[auth]]` block (issuer, audience, JWKS,
   optional scope). Mint a JWT from that issuer to present to Atryum. For a
   quick no-auth test, run Atryum with `[auth_debug] skip_verify = true`
   (local/dev only) and skip the vault credential below.

## Setup

All requests require the `managed-agents-2026-04-01` beta header. The SDKs set
it automatically.

### 1. Create the agent — declare Atryum as an MCP server

The `url` points at Atryum's `/mcp/{server}` endpoint (replace `shortcut` with
the MCP server name you registered in Atryum). Set the toolset's
`permission_policy` to `always_allow` so **Atryum** is the gate rather than
Anthropic's own per-call confirmation prompt.

```bash
curl -sS https://api.anthropic.com/v1/agents \
  -H "x-api-key: $ANTHROPIC_API_KEY" \
  -H "anthropic-version: 2023-06-01" \
  -H "anthropic-beta: managed-agents-2026-04-01" \
  -H "content-type: application/json" \
  -d '{
    "name": "Atryum-gated agent",
    "model": "claude-opus-4-8",
    "system": "Use the shortcut tools to manage tickets.",
    "mcp_servers": [
      { "type": "url", "name": "atryum-shortcut",
        "url": "https://atryum.example.com/mcp/shortcut" }
    ],
    "tools": [
      { "type": "agent_toolset_20260401" },
      { "type": "mcp_toolset", "mcp_server_name": "atryum-shortcut",
        "default_config": { "permission_policy": { "type": "always_allow" } } }
    ]
  }'
```

Save the returned `id` (and `version`). Every `mcp_servers` entry must be
referenced by an `mcp_toolset`, and vice versa.

### 2. Store the Atryum bearer token in a vault

The `mcp_servers` array carries **no auth**. Credentials are supplied at
session time via a [vault](https://platform.claude.com/docs/en/managed-agents/vaults).
Anthropic sends `access_token` as `Authorization: Bearer <token>` to the MCP
URL, so the token must be a JWT Atryum accepts. `mcp_server_url` must match the
agent's `url` exactly.

```bash
curl -sS https://api.anthropic.com/v1/vaults/$VAULT_ID/credentials \
  -H "x-api-key: $ANTHROPIC_API_KEY" \
  -H "anthropic-version: 2023-06-01" \
  -H "anthropic-beta: managed-agents-2026-04-01" \
  -H "content-type: application/json" \
  -d '{
    "display_name": "Atryum JWT",
    "auth": {
      "type": "mcp_oauth",
      "mcp_server_url": "https://atryum.example.com/mcp/shortcut",
      "access_token": "<OIDC JWT from your IdP>",
      "refresh": {
        "refresh_token": "<refresh token>",
        "client_id": "<your OAuth client_id>",
        "token_endpoint": "https://your-idp/realms/atryum/protocol/openid-connect/token",
        "token_endpoint_auth": { "type": "client_secret_post", "client_secret": "<secret>" }
      }
    }
  }'
```

The `refresh` block lets Anthropic auto-rotate the JWT before it expires. Omit
it only if you accept the token expiring (the agent then loses access). When
running Atryum with `skip_verify`, skip this step entirely.

### 3. Start a session with the vault attached

```bash
curl -sS https://api.anthropic.com/v1/sessions \
  -H "x-api-key: $ANTHROPIC_API_KEY" \
  -H "anthropic-version: 2023-06-01" \
  -H "anthropic-beta: managed-agents-2026-04-01" \
  -H "content-type: application/json" \
  -d '{ "agent": "'"$AGENT_ID"'", "environment_id": "'"$ENV_ID"'", "vault_ids": ["'"$VAULT_ID"'"] }'
```

When the agent calls a `shortcut` tool, Anthropic's server posts to
`https://atryum.example.com/mcp/shortcut` with the JWT. Atryum runs the MCP
handshake (`initialize`, `tools/list`, `tools/call`), evaluates the call against
your approval rules (auto-approve, auto-deny, or park for human approval), holds
the connection until decided, and records the invocation with its full event
log.

## Agent identity

The `sub` (or configured `agent_id_claim`) on the JWT is the authenticated
agent ID Atryum uses for `agent_id_pattern` rule matching. Map it to an Agent
Record in the Atryum UI so agent-scoped rules apply.

## Atryum-side changes

No repository code changes are required for the standard OIDC-JWT path — just:

- make Atryum internet-reachable, and
- add an `[[auth]]` block matching the JWT issuer/audience you mint tokens from
  (see `atryum.example.toml`).

A code change would only be needed if you cannot mint a signed JWT and must
present a static/opaque bearer token; Atryum's `/mcp/` validator has no
static-bearer path today.
