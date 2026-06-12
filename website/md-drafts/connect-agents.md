# Connect agents

Connect your coding agents to Atryum, allowing Atryum to review tool invocations before your agents run them.

Connect your coding agents to Atryum so tool invocations pass through Atryum before they run. Atryum evaluates each call against your rules, routes calls that need review to human approval, and records every outcome in the invocation audit log.


## Connect Cursor and Claude code

The hooks command currently supports direct setup for Cursor and Claude Code. To retrieve the available setup options for each supported coding agent:

```bash
./atryum hooks --help
```

### Install Atryum hooks for Cursor

```bash
./atryum hooks install cursor
```

### Install Atryum hooks for Claude code

```bash
./atryum hooks install claude-code
```

## Connect other coding agents

Use this path for agents that connect through Atryum's Model Context Protocol (MCP) proxy instead of the hook installers above. Your agent sends tool calls to Atryum; Atryum evaluates each call against your rules and forwards approved calls to the upstream MCP server registered under **Servers**.

Amp, Pi, Codex, and other harness integrations use this path. Claude Code can use hooks ([Connect Cursor and Claude code](#connect-cursor-and-claude-code)) or the MCP proxy — see the [Claude Code example](https://github.com/validmind/atryum/tree/main/examples/claude-code-hook) for the MCP setup.

### Point your agent at the MCP proxy

Add a standard MCP connection entry wherever your agent expects it and point it at:

```text
http://<atryum-host-and-port>/mcp/<server_name>
```

- Replace `<atryum-host-and-port>` with your Atryum base URL. The default local address is `localhost:8080`.
- Replace `<server_name>` with the name you gave the MCP server under **Servers** in the Atryum UI.

Register the upstream server in Atryum before connecting your agent ([Connect MCP servers](connect-mcp-servers.md)). Skip this if your server is already registered — for example, the demo `calc` server from [Quickstart](quickstart.md).

### Set environment variables

Before starting your agent, export these environment variables in the same shell session:

- **ATRYUM_URL** — Base URL of your Atryum server. Defaults to `http://localhost:8080`. Change this when Atryum runs on another host or port.
- **ATRYUM_AGENT_ID** — Self-declared agent identifier that Atryum matches against Agent Record **Agent IDs**. Leave this unset if you do not need invocations tagged to a specific agent record.

For example:

```bash
export ATRYUM_URL=http://localhost:8080
export ATRYUM_AGENT_ID=amp-local
```

Optionally, set these variables to label the harness in Atryum:

- **ATRYUM_CLIENT_NAME** — Harness name shown in the Atryum agent column. Defaults to each integration's source label when unset.
- **ATRYUM_CLIENT_VERSION** — Harness version shown in Atryum. Some integrations also read their native version variables, such as `AMP_VERSION` or `PI_VERSION`.

Make sure Atryum is running and reachable at **ATRYUM_URL** before you start your agent. Pending tool calls appear under **Invocations** in the Atryum UI.

### Tag invocations to an agent record

To apply agent-scoped rules or link invocations to a synced ValidMind record:

1. In Atryum, open an Agent Record or create one.
2. Add a stable string to its **Agent IDs** field, such as `amp-local` or `pi-alice`.
3. Export the same string as **ATRYUM_AGENT_ID** before starting your agent.
4. Try a tool invocation again. Atryum should attach the call to that Agent Record instead of leaving the agent column empty.

For how Atryum uses agent identity in no-auth and auth mode, refer to [Agent identity and authentication](#agent-identity-and-authentication).

### Setup examples

Refer to the repository examples for harness-specific configuration:

- [Amp](https://github.com/validmind/atryum/tree/main/examples/amp-plugin)
- [Pi](https://github.com/validmind/atryum/tree/main/examples/pi-extension)
- [Codex](https://github.com/validmind/atryum/tree/main/examples/codex-mcp)
- [Claude Code (MCP proxy)](https://github.com/validmind/atryum/tree/main/examples/claude-code-hook)
- [Other harnesses](https://github.com/validmind/atryum/tree/main/examples)

## Agent identity and authentication

When coding agents connect to Atryum, they present an *agent identity* that Atryum uses to tag invocations and match rules ([Rules](rules.md)) scoped to specific agents. Atryum supports two inbound authentication modes:

1. **No-auth mode** — The default when no `[[auth]]` blocks are configured in `atryum.toml`. Agents self-declare an agent ID.
2. **Auth mode** — Agents authenticate with OAuth bearer tokens. Atryum derives a verified agent ID from the token and ignores self-declared labels.

Use no-auth mode for local development and quick setup. Use auth mode when you need verified agent identity for production rule matching and audit.

### No-auth mode

In no-auth mode, agents and harnesses identify themselves with a self-declared agent ID. Atryum treats this as best-effort identity — useful for tagging invocations and applying agent-scoped rules, but not cryptographically verified.

- **MCP proxy clients** — Append `?agent_id=<your_id>` to the MCP proxy URL. For example: `http://localhost:8080/mcp/calc?agent_id=my-cool-id`
- **Harness clients** — Send the agent ID through the integration API or set **ATRYUM_AGENT_ID** before starting your agent. For MCP proxy setup, environment variables, and Agent Record mapping, refer to [Connect other coding agents](#connect-other-coding-agents).

Refer to the setup examples in the [`examples` directory](https://github.com/validmind/atryum/tree/main/examples) for harness-specific configuration.

:::
Self-declared agent IDs are ignored as soon as inbound auth is configured. Do not rely on `?agent_id=` or **ATRYUM_AGENT_ID** when auth mode is enabled.
:::

### Auth mode

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
