# Atryum Agentforce example

Mediate Salesforce Agentforce Agent tool calls through Atryum by exposing
Atryum as a set of **Apex Invocable Actions** — sidestepping the Agentforce
Registry / MCP-connection path entirely.

## Pattern

```
╭──────────────╮  AtryumDispatcher (Apex action)         ╭────────────╮     ╭──────────╮
│  Agentforce  │ ─── JSON-RPC tools/call ────────────▶   │  Atryum    │ ──▶ │ upstream │
│    Agent     │     server, toolName, arguments         │  approval  │     │   MCP    │
╰──────┬───────╯                                         ╰─────┬──────╯     ╰──────────╯
       │  synchronous wait                                     │
       │ ◀───── result | denied | error ──── human/rule ───▶ /ui/
       ▼
   agent gets resultText/resultJson or errorMessage
```

`AtryumDispatcher` POSTs an MCP JSON-RPC `tools/call` envelope to
`callout:Atryum/mcp/{server}`. Atryum runs its approval gate (rules first,
human second), proxies to the upstream MCP server on approval, and returns
the result inline. The agent sees the tool's output without ever talking to
the upstream directly.

`AtryumListTools` lets the agent discover what servers/tools exist at runtime
by calling Atryum's admin API (`/api/v1/admin/servers` and
`/api/v1/admin/servers/{name}/tools`).

## Files

- `force-app/main/default/classes/AtryumDispatcher.cls` — invokes a tool via Atryum's `/mcp/{server}` proxy
- `force-app/main/default/classes/AtryumListTools.cls` — discover servers & tools at runtime
- `force-app/main/default/classes/AtryumPoll.cls` — legacy stub for an external-invocations async pattern; not wired into the current dispatcher

## Setup

### 1. Atryum + Keycloak reachable from Salesforce

Salesforce Apex callouts go out from Salesforce's data centers, so both
Atryum *and* its OAuth issuer (Keycloak in the default `atryum.toml`) need
public URLs. ngrok works for local development:

```
atryum    → https://<atryum-host>
keycloak  → https://<keycloak-host>
```

In Keycloak (`https://<keycloak-host>/admin/`, realm `atryum`):

1. **Realm Settings → General → Frontend URL** = `https://<keycloak-host>`
   so tokens are issued with the public `iss` claim.
2. **Clients → Create**:
   - Client ID: `salesforce-agentforce`
   - Client authentication: **On**
   - Authentication flow: **Service accounts roles** only (= client_credentials)
   - Copy the generated **Client Secret** from the Credentials tab.
3. **Client Scopes → atryum:mcp → Mappers → Add → Audience**:
   - Included Custom Audience: `atryum`
   - Add to access token: **On**
   Then assign `atryum:mcp` as a **Default** scope on the
   `salesforce-agentforce` client.

In `atryum.toml`, point `[[auth]]` at the public Keycloak URL:

```toml
[[auth]]
enabled  = true
issuer   = "https://<keycloak-host>/realms/atryum"
audience = "atryum"
```

Verify by hand:

```sh
TOK=$(curl -s -X POST https://<keycloak-host>/realms/atryum/protocol/openid-connect/token \
  -d grant_type=client_credentials \
  -d client_id=salesforce-agentforce \
  -d client_secret=<secret> \
  -d scope=atryum:mcp | jq -r .access_token)
echo "$TOK" | cut -d. -f2 | base64 -d 2>/dev/null | jq '{iss, aud, scope}'
```

`iss` should be the public Keycloak URL and `aud` should include `atryum`.

### 2. Salesforce: External Auth Identity Provider

Setup → Named Credentials → **External Credentials** → Identity Providers
tab → New.

- Provider Type: **OpenID Connect** (or "Custom" if the UI differs)
- **Authorize Endpoint URL**: `https://<keycloak-host>/realms/atryum/protocol/openid-connect/auth`
- **Token Endpoint URL**: `https://<keycloak-host>/realms/atryum/protocol/openid-connect/token`
- **Client ID**: `salesforce-agentforce`
- **Client Secret**: paste from Keycloak

### 3. Salesforce: External Credential

Setup → Named Credentials → **External Credentials** → New.

- Label / Name: `Atryum`
- Authentication Protocol: **OAuth 2.0**
- Authentication Flow Type: **Client Credentials with Client Secret Flow**
- **Scope** parameter: `atryum:mcp`
- **Identity Provider** parameter: the one created in step 2
- **Principals → New**: Named Principal, sequence 1

### 4. Salesforce: Permission Set for the principal

External Credentials gate access through Permission Sets, not profiles.

1. Setup → Permission Sets → New → label `Atryum Access`.
2. Open it → **External Credential Principal Access** → Edit → enable the
   `Atryum` Principal → Save.
3. Assign this Permission Set to:
   - your admin user (for anonymous Apex testing)
   - the **Agentforce Agent User** (whoever the agent runs as — check Agent
     Builder → agent settings → User)

### 5. Salesforce: Named Credential

Setup → Named Credentials → **Named Credentials** tab → New.

- Label / Name: `Atryum`
- URL: `https://<atryum-host>` (base only, no path, no trailing slash)
- External Credential: `Atryum`
- Generate Authorization Header: **On**

### 6. Deploy the Apex

```sh
sf project deploy start -d force-app
```

### 7. Sanity-check with anonymous Apex

Setup → Developer Console → Debug → Execute Anonymous:

```apex
HttpRequest req = new HttpRequest();
req.setEndpoint('callout:Atryum/api/v1/admin/servers');
req.setMethod('GET');
HttpResponse r = new Http().send(req);
System.debug(r.getStatusCode() + ' ' + r.getBody());
```

Expect `200` plus a JSON list of servers. If 401, decode a fresh token (see
step 1 verification) and reconcile `iss`/`aud` with `atryum.toml`. If
"unable to fetch OAuth token", check that the Permission Set is assigned to
the user running Execute Anonymous.

### 8. Wire actions into an agent

Agent Builder → your agent → Topics → pick or create a topic for external
tools → Actions tab → Add → From Apex Class → add both `AtryumDispatcher`
and `AtryumListTools`.

Topic instructions (paste into the topic's Instructions field):

> Whenever the user asks for an action that requires an external system,
> first call `AtryumListTools` (optionally with `keyword` to narrow to a
> server name or tool keyword like "github" or "issue") to see what's
> available. Then call `AtryumDispatcher` with the matching `server`,
> `toolName`, and `argumentsJson` shaped per the returned `inputSchema`.
> Report `resultText` to the user. If `status` is `denied`, `error`, or
> `tool_error`, surface `errorMessage` and stop — do not retry.

## Limits / caveats

- **120s callout ceiling.** Apex synchronous callouts cap at 120 seconds.
  The dispatcher uses Apex's full timeout (120000 ms) on the call to
  Atryum, so any human approval that takes longer than ~2 minutes will hit
  the limit and surface as `error`. Auto-approval rules in Atryum are the
  intended workaround for the long-tail.
- **Generic dispatcher routing.** One Apex action handles all tools, so the
  LLM does the routing from natural language → `server`/`toolName`/`args`.
  This degrades as the catalog grows. For better routing quality at scale,
  consider per-tool Apex wrappers, or have Atryum emit an OpenAPI spec and
  register it via Salesforce **External Services** (one auto-generated
  action per tool with its real schema).
- **Auth scope.** Atryum's `[[auth]]` config only protects `/mcp/`; admin
  endpoints (`/api/v1/admin/*`) are unauthenticated by default, which is
  why `AtryumListTools` works even if `/mcp/` auth is misconfigured. Put
  Atryum behind a network boundary or add gateway auth if admin endpoints
  shouldn't be world-readable in your deployment.
