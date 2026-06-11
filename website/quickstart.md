# Atryum quickstart

Install and initialize Atryum, then integrate Atryum with your coding agents and ValidMind.

## Install & initialize Atryum

### Download Atryum

**Linux**

```bash
curl -L https://github.com/validmind/atryum/releases/download/0.0.2/atryum-linux -o atryum && chmod +x atryum
```

**macOS**

```bash
curl -L https://github.com/validmind/atryum/releases/download/0.0.2/atryum-mac -o atryum && chmod +x atryum
```

### Set up Atryum test server

1. Generate a minimal testing configuration with a simple calculator Model Context Protocol (MCP) server that does not require any external credentials:

    ```bash
    ./atryum setup demo
    ```

2. Start the Atryum service and register the test calculator server in Atryum's local database:

    ```bash
    ./atryum run --init-servers
    ```

3. In your browser, navigate to [`localhost:8080`](http://localhost:8080) to open the Atryum local web user interface.

    Here, you can view servers, manage tool invocation approvals, configure rules, and more.

4. Within Atryum, click **Servers** in the left sidebar. Confirm that your test calculator server was successfully registered:

    - Verify that the server's <span style="font-variant: small-caps;">connection</span> and <span style="font-variant: small-caps;">auth</span> both display as <span style="font-variant: small-caps;">`ready`</span>.
    - Verify that Atryum exposed the server at [`localhost:8080/mcp/calc`](http://localhost:8080/mcp/calc).

5. Connect your preferred coding agent to Atryum. Open your agent's MCP settings and add a standard MCP server with the calc server address: `localhost:8080/mcp/calc`.

    The agent will think it is talking to a calculator MCP server, but its tool calls now pass through Atryum first.

6. Trigger a test tool call from your agent. For example:

    ```text
    Use the calculator tools and show me 2*2
    ```

7. Within Atryum, click **Invocations** in the left sidebar. Confirm that you see the calculator invocation `Pending Approval` — human approval is required by default.

    - Under Approval Required, select **Approve** to let the tool call run.
    - Verify that the invocation's <span style="font-variant: small-caps;">auth</span> is `Succeeded` and that the approval was <span style="font-variant: small-caps;">decided by</span> a <span style="font-variant: small-caps;">human</span>.

#### Approval timeout

Human approvals have a default timeout of 30 seconds. To adjust the timeout, update your local Atryum config file (`atryum.toml`), then restart Atryum. For example:

```toml
[defaults]
request_timeout_seconds = 120
```

#### Invocation denials

1. In Atryum, click **Invocations** in the left sidebar.

2. Click on the invocation you want to deny.

3. On the invocations detail panel and select **Deny** under Approval Required.

4. (Optional) Enter in a reason for denial — this reason is returned to the agent so you can steer what it does next.

5. Click **Deny** to confirm the denial.

### Set up rules for Atryum

Rules are if/then policies that tell Atryum how to handle tool calls:

- Rules let you reuse manual decisions for future tool calls that match the same conditions.
- Rules are applied from top to bottom — the first matching rule wins.

For example — if the server is `calc`, then approve the call automatically. Let's add a rule to control future matching tool calls:

1. In Atryum, click **Rules** in the left sidebar.

2. Click **New Rule**.

3. Choose an Action:
    - **Auto Approve** lets matching tool calls run without stopping for manual approval.
    - **Auto Deny** blocks matching tool calls automatically.
    - **Human Approval** pauses matching tool calls until a human approves or denies them.

    For this demo, select **Auto Approve**.

4. Choose the Agents, Servers / Sources, and Tools that the rule should apply to.

    For this demo, select `calc` under **Servers/Sources** to apply the rule only to calls to the test calculator server.

5. (Optional) Add a Description so you can remember why the rule exists.

6. Make sure that **Enabled** is checked, then click **Create**.

7. Try a calculator prompt from your agent again. Atryum should apply the new rule instead of treating the call like a brand-new manual decision:

    -  Within Atryum, click **Invocations** in the left sidebar and confirm that your new call `Succeeded`.
    - Verify that the approval was <span style="font-variant: small-caps;">decided by</span> a <span style="font-variant: small-caps;">rule</span>.

#### Create a rule from existing invocation

You can also create a rule from an existing invocation by clicking on the invocation to expand details, then selecting **Create Rule From This**.

## Integrate Atryum

### With coding agents

Coding agents can be connected to atryum at the harness level. Hooks and extensions are available for Claude Code, Cursor, Amp, Pi, and Codex.

Run

```
./atryum hooks
```

### With ValidMind

To connect your atryum to ValidMind run:

```
./atryum setup validmind
ValidMind Base URL: (you probably want dev)
ValidMind API key: abcd1234
ValidMind API secret: arstarst
updated ValidMind credentials in $HOME/.config/atryum/atryum.toml
```

Once setup, restart and return to the UI:

[`localhost:8080/settings`](http://localhost:8080/settings)

Fill out the form. It is helpful to have the following setup in ValidMind:

- A primary record type specifically for ai-agents.
- A long text field on that record for agent charters.

The charter is where you define "allowed to do X", "deny the agent trying to do Y", and "pass requests to do Z for human approval".

![ValidMind charter configuration example](assets/charter.png)

Since this is a quick demo, set the default agent in the settings page. We'll connect with agent identity later.

Finally you need a rule, relatively high in priority, mapping that default Agent Record to AI Evaluation.

With all that setup, you're ready to rock and roll. Ask the agent to do work, then use the charter in ValidMind to restrict its scope.
