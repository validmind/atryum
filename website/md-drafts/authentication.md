# Integrations (draft)

Draft sections below are destined for merge into [integrations.md](integrations.md).

---

## Connect agents

### Agent identity and authentication

When coding agents connect to Atryum, they present an **agent identity** that Atryum uses to tag invocations and match [rules](rules.md) scoped to specific agents. Atryum supports two inbound authentication modes:

1. **No-auth mode** — The default when no `[[auth]]` blocks are configured in `atryum.toml`. Agents self-declare an agent ID.
2. **Auth mode** — Agents authenticate with OAuth bearer tokens. Atryum derives a verified agent ID from the token and ignores self-declared labels.

Use no-auth mode for local development and quick setup. Use auth mode when you need verified agent identity for production rule matching and audit.

#### No-auth mode

In no-auth mode, agents and harnesses identify themselves with a self-declared agent ID. Atryum treats this as best-effort identity — useful for tagging invocations and applying agent-scoped rules, but not cryptographically verified.

**MCP proxy clients** — Append `?agent_id=<your_id>` to the MCP proxy URL. For example:

```text
http://localhost:8080/mcp/calc?agent_id=my-cool-id
```

**Harness clients** — Send the agent ID through the integration API or set the **ATRYUM_AGENT_ID** environment variable before starting your agent. For MCP proxy setup, environment variables, and Agent Record mapping, refer to [Connect other coding agents](#connect-other-coding-agents).

Refer to the setup examples in the [`examples`](https://github.com/validmind/atryum/tree/main/examples) directory for harness-specific configuration.

:::
Self-declared agent IDs are ignored as soon as inbound auth is configured. Do not rely on `?agent_id=` or **ATRYUM_AGENT_ID** when auth mode is enabled.
:::

#### Auth mode

In auth mode, agents and harnesses must authenticate to Atryum with an OAuth bearer token. Atryum validates the token against one or more authorization servers configured in `atryum.toml`, then uses the token's **agent ID claim** — by default `client_id`, falling back to `azp`, then `sub` — as the authenticated agent ID when evaluating rules and recording invocations.

1. Add one or more `[[auth]]` blocks to your `atryum.toml` configuration file:

    ```toml
    [[auth]]
    enabled        = true
    issuer         = "https://keycloak.example/realms/atryum"
    audience       = "atryum"
    required_scope = "atryum:mcp"
    agent_id_claim = "client_id"
    ```

    - **issuer** — The OIDC issuer URL for your authorization server.
    - **audience** — The audience Atryum expects on incoming tokens.
    - **required_scope** — Optional. When set, tokens must include this scope.
    - **agent_id_claim** — JWT claim Atryum uses as the authenticated agent ID for rule matching.

2. Restart Atryum so it loads the updated configuration.

3. Configure your agent or harness to obtain an OAuth access token from the same authorization server and present it as a bearer token when connecting to Atryum.

For local development, a Keycloak container is included in the repository's Docker Compose setup:

```bash
docker compose --profile dev up -d keycloak
```

Keycloak runs at [`localhost:8089`](http://localhost:8089). On first startup, the `keycloak-init` service provisions the `atryum` realm, client scope, and Dynamic Client Registration policies. See [`keycloak/setup-realm.sh`](https://github.com/validmind/atryum/blob/main/keycloak/setup-realm.sh) for the equivalent manual setup steps.

For deployments outside local development, use your organization's identity provider (IdP) instead of the bundled Keycloak instance.

:::
Most MCP proxy integrations in the [`examples`](https://github.com/validmind/atryum/tree/main/examples) directory currently support no-auth mode only. When auth mode is enabled, configure your harness to send a bearer token rather than a query-parameter agent ID.
:::

---

## Connect MCP servers

Atryum can authenticate to remote Model Context Protocol (MCP) servers on your behalf. The agent connects to Atryum's MCP proxy; Atryum holds upstream OAuth credentials and forwards credentialed calls to the remote server. The agent never receives the upstream token.

Atryum natively supports remote MCP servers that use OAuth 2.0, including servers that support Dynamic Client Registration (DCR). For servers that do not support DCR, you can register an OAuth client manually and enter the credentials in the Atryum UI.

### Add an OAuth-protected MCP server

1. In Atryum, click **Servers** in the left sidebar.

2. Click **New Server**.

3. Provide the server details:

    - **Name** — A short label for the server, used in MCP proxy URLs (`/mcp/<name>`) and rule scoping.
    - **Mode** — Select `http` for remote MCP servers.
    - **Base URL** — The upstream MCP server URL.

4. Click **Save** (or **Create Server** for a new entry).

5. When the server requires OAuth, Atryum detects the authorization server and shows **Connect** (or **Reconnect** if credentials have expired). Click **Connect** and complete the browser authorization flow.

6. After authorization succeeds, confirm that the server's <span style="font-variant: small-caps;">connection</span> and <span style="font-variant: small-caps;">auth</span> columns both show as ready.

7. Point your coding agent at Atryum's MCP proxy for this server: `http://<atryum-host-and-port>/mcp/<server_name>`. For agent setup, refer to [Connect agents](#connect-agents).

### Manual OAuth registration

Use manual registration when the upstream server does not support Dynamic Client Registration — for example, Slack or GitHub OAuth apps where you register a client out of band.

1. In Atryum, click **Servers** in the left sidebar and select the server you want to configure.

2. Expand **Manual OAuth configuration (advanced)**.

3. Enter the OAuth client details from your provider:

    - **Client ID**
    - **Client Secret** — Leave blank when editing an existing server to keep the stored value.
    - **Authorize URL** — Optional. Atryum auto-discovers this from the server URL when left blank.
    - **Token URL** — Optional. Atryum auto-discovers this from the server URL when left blank.
    - **Scopes** — Optional. Leave **Use default scopes** checked to use whatever scopes the OAuth app declared with the provider.

4. Click **Save**, then click **Connect** (or **Reconnect**) to complete authorization.

When a server needs re-authentication, the auth status shows that action is required and the **Reconnect** button appears in the server detail panel.
