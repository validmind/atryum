# Atryum quickstart

Install and initialize Atryum, then integrate Atryum with your coding agents and ValidMind.

## Install & initialize Atryum

Download Atryum and register a server in Atryum for testing.

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

5. Connect your preferred coding agent to Atryum:

    - Open your agent's MCP settings and add a standard MCP server with the calc server address: `localhost:8080/mcp/calc`.
    - The agent will think it is talking to a calculator MCP server, but its tool invocations now pass through Atryum first.

6. Trigger a test tool call from your agent. For example:

    ```text
    Use the calculator tools and show me 2*2
    ```

7. Within Atryum, click **Invocations** in the left sidebar to review tool calls. Confirm that the calculator invocation request is `Pending Approval` — human approval is required by default.

    - Under Approval Required, select **Approve** to let the tool call run.
    - Verify that the invocation's <span style="font-variant: small-caps;">auth</span> is `Succeeded` and that the approval was <span style="font-variant: small-caps;">decided by</span> a <span style="font-variant: small-caps;">human</span>.

To learn more about working with invocations, refer to **[Invocations](invocations.md)**.

### Set up calculation server rule

Rules are if/then policies that tell Atryum how to handle tool invocations. For example — if the server is `calc`, then approve the call automatically.

Let's add a rule to control future matching tool calls:

1. In Atryum, click **Rules** in the left sidebar.

2. Click **New Rule**.

3. Under **Action**, select `Auto Approve`.

4. Under **Servers/Sources**, select `calc` to apply the rule only to calls to the test calculator server.

5. Make sure that **Enabled** is checked, then click **Create Rule**.

6. Try a calculator prompt from your agent again. Atryum should apply the new rule instead of treating the tool call like a brand-new manual decision:

    -  Within Atryum, click **Invocations** in the left sidebar and confirm that your new call `Succeeded`.
    - Verify that the approval was <span style="font-variant: small-caps;">decided by</span> a <span style="font-variant: small-caps;">rule</span>.

To learn more about setting up rules to manage invocations, refer to **[Rules](rules.md)**.

## Integrate Atryum

After setting up your test calculator server, connect Atryum with your coding agents and ValidMind to streamline your agent oversight in one platform.

To learn more about connecting ValidMind or agents with Atryum, refer to **[Integrations](integrations.md)**.


### With ValidMind

Sync your ValidMind organization's agent records to Atryum to map tool invocations to agent records and use agent-scoped rules.

Connecting agent records from ValidMind also allows you to optionally use Atryum to evaluate tool invocations against each agent’s *constitution*. A constitution is a plain-language policy that describes what an agent can do, what it cannot do, and which actions require human approval.

### With coding agents

Connect your coding agents to Atryum, allowing Atryum to review tool invocations before your agents run them. Hooks and extensions are available for [Claude Code](https://www.anthropic.com/claude-code), [Cursor](https://cursor.com), [Amp](https://ampcode.com), [Pi](https://pi.dev), and [Codex](https://openai.com/codex).


