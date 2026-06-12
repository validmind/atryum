# Connect agents

Connect your coding agents to Atryum, allowing Atryum to review tool invocations before your agents run them.

Connect your coding agents to Atryum so tool invocations pass through Atryum before they run. Atryum evaluates each call against your rules ([Rules](rules.md)), routes calls that need review to human approval, and records every outcome in the invocation audit log ([Invocations](invocations.md)).

## Connect Cursor and Claude Code

The hooks command currently supports direct setup for [Cursor](https://cursor.com) and [Claude Code](https://www.anthropic.com/claude-code). To retrieve the available setup options for each supported coding agent:

```bash
./atryum hooks --help
```

### Install Atryum hooks for Cursor

```bash
./atryum hooks install cursor
```

Restart Cursor after changing hooks.

### Install Atryum hooks for Claude Code

```bash
./atryum hooks install claude-code
```

Restart Claude Code after changing hooks.

## Connect other coding agents

Other coding agents connect to Atryum in one of two ways:

- **Hook or extension** — The agent sends native tool calls to Atryum over the external invocation API. Atryum evaluates each call against your rules; the agent still runs the tool. Used by [Amp](https://ampcode.com), [Pi](https://pi.dev), and the [Claude Code hook example](https://github.com/validmind/atryum/tree/main/examples/claude-code-hook).
- **MCP proxy** — The agent connects to Atryum's MCP proxy at `/mcp/<server_name>`. Atryum evaluates each call, then forwards approved calls to the upstream MCP server you registered. Used by [Codex](https://openai.com/codex) for MCP tool calls today.

Hook and extension integrations do not use the MCP proxy URL steps below. MCP proxy integrations do not need `./atryum hooks install`.

### Connect hook and extension agents

Use this path for agents that gate native tools (shell, file edits, in-process tools) through Atryum before execution:

1. Make sure Atryum is running and reachable ([Quickstart](quickstart.md)).

2. Set `ATRYUM_URL` (and optionally `ATRYUM_AGENT_ID`) in the same terminal session where you start the agent. ([Set environment variables](#set-environment-variables))

3. Follow the setup steps in the example for your agent:

    - [Amp plugin](https://github.com/validmind/atryum/tree/main/examples/amp-plugin) — Requires `PLUGINS=all` when starting Amp
    - [Pi extension](https://github.com/validmind/atryum/tree/main/examples/pi-extension)
    - [Claude Code hooks](https://github.com/validmind/atryum/tree/main/examples/claude-code-hook) — Or use [Connect Cursor and Claude Code](#connect-cursor-and-claude-code) to install hooks automatically.

4. Start your agent from that terminal session. Pending tool calls appear under **Invocations** in the Atryum platform left sidebar.

#### Set environment variables

Use these variables for hook and extension integrations. MCP proxy clients do not read `ATRYUM_AGENT_ID` — tag agent identity with `?agent_id=` on the proxy URL instead.

1. Open the terminal session you will use to start your agent.

2. Export `ATRYUM_URL` — the base URL of your Atryum server:

    ```bash
    export ATRYUM_URL=http://localhost:8080
    ```

    Change the host or port when Atryum runs elsewhere. When unset, integrations default to `http://localhost:8080`.

3. (Optional) Export `ATRYUM_AGENT_ID` — a self-declared agent identifier that Atryum matches against Agent Record **Agent IDs**.

    Leave this unset if you do not need invocations tagged to a specific agent record:

    ```bash
    export ATRYUM_AGENT_ID=amp-local
    ```

4. (Optional) Export these variables to label the agent in Atryum:

    - `ATRYUM_CLIENT_NAME` — Harness name shown in the **Agent** column on **Invocations**. Defaults to each integration's source label when unset.
    - `ATRYUM_CLIENT_VERSION` — Harness version shown in Atryum. Some integrations also read their native version variables, such as `AMP_VERSION` or `PI_VERSION`.

5. Make sure Atryum is running and reachable at `ATRYUM_URL`, then start your agent from the same terminal session.

### Connect via MCP proxy

Use this path when your agent speaks MCP and you want tool calls routed through Atryum to an upstream server registered under **Servers**:

1. Register the upstream server in Atryum before connecting your agent. ([Connect MCP servers](connect-mcp-servers.md))

    Skip this if your server is already registered.

2. In your agent's MCP settings, add a standard MCP connection entry and point it at:

    ```text
    http://<atryum-host-and-port>/mcp/<server_name>
    ```

3. Replace `<atryum-host-and-port>` with your Atryum base URL.

    The default local address is `localhost:8080`.

4. Replace `<server_name>` with the name you gave the MCP server under **Servers** in the Atryum platform left sidebar.

5. Start your agent. To tag invocations to an agent record in no-auth mode, append `?agent_id=<your_id>` to the MCP proxy URL — for example, `http://localhost:8080/mcp/calc?agent_id=amp-local`. ([Agent identity and authentication](#agent-identity-and-authentication))

### Setup examples

Refer to the repository examples for agent-specific configuration:

#### Hook and extension integrations

- [Amp](https://github.com/validmind/atryum/tree/main/examples/amp-plugin)
- [Pi](https://github.com/validmind/atryum/tree/main/examples/pi-extension)
- [Claude Code (hooks)](https://github.com/validmind/atryum/tree/main/examples/claude-code-hook)

#### MCP proxy integrations

- [Codex](https://github.com/validmind/atryum/tree/main/examples/codex-mcp)
- [Other agents](https://github.com/validmind/atryum/tree/main/examples)

## Tag invocations to agent records

To apply agent-scoped rules, attach invocations to an agent record, or supply a constitution for local AI evaluation:

1. In Atryum, click **Agents** in the left sidebar.

2. Click **New Agent** to create a local agent record, or click an existing agent — including records synced from ValidMind. ([Connect ValidMind](connect-validmind.md))

    If creating a new local agent:
    
    a. Enter the:

    - **Name**
    - (Optional) **Description**
    - (Optional, but recommended) **Constitution** — The rules and constraints governing this agent's behavior. Atryum uses this for local LLM-as-judge evaluation rules. ([Configure LLM providers](configure-llm-providers.md))

    b. Add a stable string to **Agent IDs** — Type the ID and press **Enter** to add it.

    c. Make sure that **Enabled** is checked.

    d. Click **Save**.

3. If you selected an existing agent instead, add a stable string to its **Agent IDs** field — type the ID and press **Enter** to add it, such as `amp-local` or `pi-alice`, then click **Save**.

4. Tell your agent which **Agent IDs** string to send:

    a. **Hook and extension agents** — In the same terminal session where you will start your agent, export that string as `ATRYUM_AGENT_ID`:

    ```bash
    export ATRYUM_URL=http://localhost:8080
    export ATRYUM_AGENT_ID=amp-local
    ```

    - Use the exact string you added in **Agent IDs** — matching is case-sensitive, so `amp-local` and `Amp-Local` are different IDs.
    - Export both variables in the **same shell session** you use to launch your agent. If you export them in one terminal and start the agent from another, the agent will not see the values.
    - `ATRYUM_URL` tells the integration where Atryum is running. It defaults to `http://localhost:8080` when unset; change the host or port if Atryum runs elsewhere. Refer to [Set environment variables](#set-environment-variables) for the full list of supported variables.
    - In auth mode, put the token's JWT `sub` claim or OAuth `client_id` in **Agent IDs** instead of a self-declared string. ([Agent identity and authentication](#agent-identity-and-authentication))

    b. **MCP proxy agents** — Append the same string to your MCP proxy URL as `?agent_id=<your_id>` (for example, `http://localhost:8080/mcp/calc?agent_id=amp-local`).

    - Use the exact string you added in **Agent IDs** — matching is case-sensitive, so `amp-local` and `Amp-Local` are different IDs.
    - In auth mode, put the token's JWT `sub` claim or OAuth `client_id` in **Agent IDs** instead of a self-declared string, and send a bearer token rather than a query-parameter agent ID. ([Agent identity and authentication](#agent-identity-and-authentication))

5. Start your agent, then send a tool invocation again.

    Atryum should attach the call to that agent record in the <span style="font-variant: small-caps;">agent record</span> column on **Invocations** instead of leaving it empty.

:::
For how Atryum uses agent identity in no-auth and auth mode, refer to [Agent identity and authentication](#agent-identity-and-authentication).
:::

## Edit connected coding agents

After you connect an agent, update its integration files, environment variables, or MCP settings on the machine where the agent runs. Update agents in the Atryum platform user interface when you need to change agent IDs, constitutions, or which record invocations map to.

### Edit Cursor and Claude Code hooks

To refresh the shared hook script or re-apply hook entries after an Atryum upgrade:

```bash
./atryum hooks install cursor
```

```bash
./atryum hooks install claude-code
```

Restart Cursor or Claude Code after changing hooks.

To change `ATRYUM_URL` or `ATRYUM_AGENT_ID`, export the new values in the terminal session where you start the agent, then restart the agent. The installed hook reads these at runtime — you do not need to reinstall hooks when only the values change.

### Edit hook and extension agents

Amp, Pi, and manually configured Claude Code hooks follow the same pattern:

1. Update `ATRYUM_URL`, `ATRYUM_AGENT_ID`, or optional label variables in the terminal session where you start the agent. ([Set environment variables](#set-environment-variables))

2. If you changed plugin or extension source files, copy the updated file from the repository example into your install path — for example `~/.config/amp/plugins/atryum.ts` or `~/.pi/agent/extensions/atryum/index.ts`.

3. Restart the agent from that terminal session so it picks up the changes.

    Pi can reload extensions without a full restart when the extension is already installed — run `/reload` in an active Pi session.

To retag invocations to a different agent record, update **Agent IDs** in Atryum and export the matching `ATRYUM_AGENT_ID` before restarting. ([Tag invocations to agent records](#tag-invocations-to-agent-records))

### Edit MCP proxy connections

1. Open your agent's MCP configuration file — for example `~/.codex/config.toml` for Codex.

2. Update the proxy URL when Atryum's host, port, upstream **Servers** name, or `?agent_id=` value changes:

    ```text
    http://<atryum-host-and-port>/mcp/<server_name>?agent_id=<your_id>
    ```

3. Restart your agent so it reconnects with the updated URL.

When the upstream MCP server itself changes, edit the registration under **Servers** in Atryum. ([Connect MCP servers](connect-mcp-servers.md))

### Edit agent records

1. In Atryum, click **Agents** in the left sidebar.

2. Click the agent you want to edit.

3. Update the fields as desired:

    - **Name**
    - **Description**
    - **Constitution** — Editable for local agent records only. Synced ValidMind records show the constitution as read-only — edit the source field in ValidMind instead. ([Connect ValidMind](connect-validmind.md))
    - **Agent IDs** — Type an ID and press **Enter** to add it. Remove individual IDs with the control on each tag.

4. To turn the record on or off:

    - Check **Enabled** to mark the agent record as active.
    - Uncheck **Enabled** to mark it inactive.

5. Click **Save**.

When you change **Agent IDs**, update `ATRYUM_AGENT_ID` or the `?agent_id=` query parameter on the MCP proxy URL to match, then restart the agent. ([Tag invocations to agent records](#tag-invocations-to-agent-records))

### Disconnect or remove agents

**Cursor and Claude Code** — Remove Atryum hook commands:

```bash
./atryum hooks uninstall cursor
```

```bash
./atryum hooks uninstall claude-code
```

Restart Cursor or Claude Code after uninstalling hooks. The shared hook script remains at `~/.atryum/hooks/atryum-hook.mjs` until you delete it manually.

- **Amp** — Delete `atryum.ts` from `~/.config/amp/plugins/` or `.amp/plugins/` in your project.
- **Pi** — Delete `index.ts` from `~/.pi/agent/extensions/atryum/` or `.pi/extensions/atryum/` in your project.
- **MCP proxy agents** — Remove or comment out the Atryum MCP server entry in your agent's MCP configuration, then restart the agent.
- **Local agent records** — In Atryum, open the agent under **Agents** and click **Delete**. This action is permanent.

Synced ValidMind agent records cannot be deleted from Atryum — remove them by changing Agent Record Sync settings or deleting the source record in ValidMind. ([Connect ValidMind](connect-validmind.md))

## Agent identity and authentication

When coding agents connect to Atryum, they present an *agent identity* that Atryum uses to tag invocations and match rules ([Rules](rules.md)) scoped to specific agents. Atryum supports two inbound authentication modes:

1. **No-auth mode** — The default when no `[[auth]]` blocks are configured in `atryum.toml`. Agents self-declare an agent ID.
2. **Auth mode** — Agents authenticate with OAuth bearer tokens. Atryum derives a verified agent ID from the token and ignores self-declared labels.

Use no-auth mode for local development and quick setup. Use auth mode when you need verified agent identity for production rule matching and audit.

### No-auth mode

In no-auth mode, agents identify themselves with a self-declared agent ID. Atryum treats this as best-effort identity — useful for tagging invocations and applying agent-scoped rules, but not cryptographically verified.

- **MCP proxy clients** — Append `?agent_id=<your_id>` to the MCP proxy URL. For example: `http://localhost:8080/mcp/calc?agent_id=my-cool-id`
- **Hook and extension agents** — Send the agent ID through the integration API or set `ATRYUM_AGENT_ID` before starting your agent.

Refer to the setup examples in the [GitHub `examples` directory](https://github.com/validmind/atryum/tree/main/examples) for agent-specific configuration.

:::
Self-declared agent IDs are ignored as soon as inbound auth is configured. Do not rely on `?agent_id=` or `ATRYUM_AGENT_ID` when auth mode is enabled.
:::

### Auth mode

In auth mode, agents must authenticate to Atryum with an OAuth bearer token. Atryum validates the token against one or more authorization servers configured in `atryum.toml`, then uses the token's **agent ID claim** — by default `client_id`, falling back to `azp`, then `sub` — as the authenticated agent ID when evaluating rules and recording invocations.

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

2. Restart Atryum so it loads the updated configuration:

        ```bash
        ./atryum run --init-servers
        ```

3. Configure your agent to obtain an OAuth access token from the same authorization server and present it as a bearer token when connecting to Atryum.

For local development, a [Keycloak](https://www.keycloak.org/) container is included in the repository's [Docker Compose](https://docs.docker.com/compose/) setup:

```bash
docker compose --profile dev up -d keycloak
```

Keycloak runs at [`localhost:8089`](http://localhost:8089). On first startup, the `keycloak-init` service provisions the `atryum` realm, client scope, and Dynamic Client Registration policies. Refer to [`keycloak/setup-realm.sh`](https://github.com/validmind/atryum/blob/main/keycloak/setup-realm.sh) for the equivalent manual setup steps.

For deployments outside local development, use your organization's identity provider (IdP) instead of the bundled Keycloak instance.

:::
Most hook and extension integrations in the [GitHub `examples` directory](https://github.com/validmind/atryum/tree/main/examples) currently support no-auth mode only. When auth mode is enabled, configure your agent to send a bearer token rather than a query-parameter agent ID.
:::
