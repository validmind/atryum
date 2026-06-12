# Connect MCP servers

Register upstream MCP servers in Atryum, allowing your coding agents to reach external tools through Atryum's MCP proxy.

Register upstream Model Context Protocol (MCP) servers so tool invocations pass through Atryum before they reach the upstream server. Atryum evaluates each call against your rules, holds upstream credentials on your behalf, and forwards approved calls to the remote server.

Atryum supports two server modes:

- **stdio** — Run a local MCP server as a subprocess (for example, the demo `calc` server from the [Quickstart](quickstart.md)).
- **HTTP** — Connect to a remote MCP server over the network. Atryum can authenticate on your behalf with a static bearer token or OAuth. Your coding agent never receives upstream credentials.

After you register a server, point your coding agent at Atryum's proxy.

::::
For agent setup, refer to [Connect agents](connect-agents.md#connect-other-coding-agents).
:::

## Add local MCP servers (stdio)

Use stdio mode for MCP servers that run locally as a subprocess — for example, packages installed with `npx` or a Python module.

1. In Atryum, click **Servers** in the left sidebar.

2. Click **New Server**.

3. Provide the server details:

    - **Name** — A short label for the server, used in MCP proxy URLs (`/mcp/<name>`) and rule scoping
    - **Mode** — Select `stdio`.
    - **Command** — The executable that starts the MCP server, such as `npx` or `python`
    - **Args (JSON array)** — Arguments passed to the command. For example: `["-y", "@coo-quack/calc-mcp@latest"]`
    - **Env (JSON object)** — Environment variables for the subprocess. Use `{}` when none are required.

4. Make sure that **Enabled** is checked, then click **Create Server**.

5. Click **Test** to confirm Atryum can reach the server.

    Confirm that the server's <span style="font-variant: small-caps;">connection</span> column shows as <span style="font-variant: small-caps;">`ready`</span>.

:::
stdio servers do not use OAuth. You will not see a **Connect** button for this mode.
:::

## Add HTTP MCP servers with bearer tokens

Use HTTP mode with a **Bearer Token** when the upstream server accepts a static pre-shared token instead of OAuth.

1. In Atryum, click **Servers** in the left sidebar.

2. Click **New Server** (or select an existing HTTP server).

3. Provide the server details:

    - **Name** — A short label for the server, used in MCP proxy URLs (`/mcp/<name>`) and rule scoping.
    - **Mode** — Select `HTTP`.
    - **Base URL** — The upstream MCP server URL.
    - (Optional) **Timeout (seconds)** — Maximum time Atryum waits for upstream responses. Defaults to `30`.
    - **Bearer Token** — The upstream API token. Atryum stores this and sends it on your behalf.

4. Make sure that **Enabled** is checked, then click **Create Server** (or **Save** when editing an existing server).

5. Click **Test** to confirm Atryum can reach the server with the token.

6. Point your coding agent at Atryum's MCP proxy for this server: `http://<atryum-host-and-port>/mcp/<server_name>`.

:::
For agent setup, refer to [Connect agents](connect-agents.md#connect-other-coding-agents).
:::

## Add OAuth-protected MCP servers

Use HTTP mode when the upstream MCP server requires OAuth. Atryum holds the upstream token and forwards credentialed calls — your coding agent connects only to Atryum's MCP proxy.

1. In Atryum, click **Servers** in the left sidebar.

2. Click **New Server**.

3. Provide the server details:

    - **Name** — A short label for the server, used in MCP proxy URLs (`/mcp/<name>`) and rule scoping.
    - **Mode** — Select `HTTP`.
    - **Base URL** — The upstream MCP server URL.
    - (Optional) **Timeout (seconds)** — Maximum time Atryum waits for upstream responses. Defaults to `30`.
    - **Bearer Token** — Leave blank. OAuth servers authenticate through **Connect**, not a static token. ([Add HTTP MCP servers with bearer tokens](#add-http-mcp-servers-with-bearer-tokens))

4. Make sure that **Enabled** is checked, then click **Create Server**.

5. When the server requires OAuth, Atryum detects the authorization server and shows **Connect** (or **Reconnect** if credentials have expired). Click **Connect** and complete the browser authorization flow.

6. After authorization succeeds, confirm that the server's <span style="font-variant: small-caps;">connection</span> and <span style="font-variant: small-caps;">auth</span> columns both show as <span style="font-variant: small-caps;">`ready`</span>.

7. Point your coding agent at Atryum's MCP proxy for this server: `http://<atryum-host-and-port>/mcp/<server_name>`.

:::
For agent setup, refer to [Connect agents](connect-agents.md#connect-other-coding-agents).
:::


### Use manual OAuth registration

Use manual registration when the upstream server does not support Dynamic Client Registration — for example, [Slack](https://slack.com) or [GitHub](https://github.com) OAuth apps where you register a client out of band.

1. Expand **Manual OAuth configuration (advanced)**.

2. Enter the OAuth client details from your provider:

    - **Client ID**
    - **Client Secret** — Leave blank when editing an existing server to keep the stored value.
    - (Optional) **Authorize URL** — Atryum auto-discovers this from the server URL when left blank.
    - (Optional) **Token URL** — Atryum auto-discovers this from the server URL when left blank.
    - Set your scopes:
        - Leave **Use default scopes** checked to use whatever scopes the OAuth app declared with the provider.
        - Uncheck **Use default scopes** to define custom scopes, then enter in the **Scopes (requested)**.

3. Make sure that **Enabled** is checked.

4. Click **Save**, then click **Connect** (or **Reconnect**) to complete authorization.

When a server needs re-authentication, the auth status shows that action is required and the **Reconnect** button appears in the server detail panel.

:::
Leave **Bearer Token** blank when using the OAuth **Connect** flow. Atryum hides the bearer field once OAuth credentials are in place.
:::

## Edit or remove MCP servers

### Edit MCP servers

1. In Atryum, click **Servers** in the left sidebar.

2. Click the server you want to edit.

3. Update the fields as desired:

    - **Name** is read-only after creation — Atryum does not support renaming servers. To use a new proxy path (`/mcp/<name>`), create a new server and update your agent's MCP configuration. ([Connect agents](connect-agents.md#connect-via-mcp-proxy))

    - For stdio servers:

        - **Command**
        - **Args (JSON array)**
        - **Env (JSON object)**

    - For HTTP servers:

        - **Base URL**
        - (Optional) **Timeout (seconds)**
        - **Bearer Token** — Enter a new value only when rotating credentials. Saving with an empty field clears the stored token. Hidden for OAuth-managed servers.
        - **Manual OAuth configuration (advanced)** — Update OAuth client fields when needed. Leave **Client Secret** blank to keep the stored value. ([Use manual OAuth registration](#use-manual-oauth-registration))

4. To turn the server on or off:

    - Check **Enabled** to turn the server on.
    - Uncheck **Enabled** to turn the server off.

    Disabled servers remain in the list. Check **Show disabled** above the table to include them.

5. Click **Save** to apply your edits.

6. Click **Test** to confirm Atryum can still reach the server.

    For OAuth HTTP servers, click **Reconnect** when credentials have expired or the <span style="font-variant: small-caps;">auth</span> column shows that action is required.

7. When **Base URL**, credentials, or the server name (via a new registration) change, update your agent's MCP proxy URL to match. ([Connect agents](connect-agents.md#connect-via-mcp-proxy))

### Remove MCP servers

:::
Deleting a server is permanent and cannot be undone.
:::

1. In Atryum, click **Servers** in the left sidebar.

2. Click the server you want to remove.

3. Click **Delete**, then select **OK** to confirm deletion.

4. Remove or update the MCP proxy entry in your agent's configuration so it no longer points at the deleted server. ([Connect agents](connect-agents.md#disconnect-or-remove-agents))

To stop routing calls without deleting the configuration, disable them instead. ([Edit MCP servers](#edit-mcp-servers))