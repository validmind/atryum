# Atryum quickstart

Install and initialize Atryum, then integrate Atryum with your coding agents and ValidMind.

## Install & initialize Atryum

Dowload Atryum and register a server in Atryum for testing.

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

## Integrate Atryum

After setting up your test test calculator server, connect Atryum up with your coding agents and ValidMind.

### With coding agents

Connect your coding agents to Atryum, allowing Atryum to review tool calls before your agents run them. Hooks and extensions are available for [Claude Code](https://www.anthropic.com/claude-code), [Cursor](https://cursor.com), [Amp](https://ampcode.com), [Pi](https://pi.dev), and [Codex](https://openai.com/codex).

Run the hooks command to see the available setup options for each supported coding agent:

```bash
./atryum hooks
```

### With ValidMind

Connect Atryum to ValidMind to sync AI agent records and evaluate tool calls against each agent’s constitution.

#### Prerequisites

Before connecting Atryum to ValidMind, prepare the agent records that Atryum will sync:

- Create or choose a ValidMind primary record type for AI agents, such as `ai-agents`.
- Add a custom long-text field to that record type for each agent's constitution. For example, use a field key like `constitution`.
- Create at least one agent record in that record type and fill in its constitution field.
- Make sure you have a ValidMind API key and secret for the environment you want Atryum to connect to.

#### Connect Atryum to ValidMind

1. Run the ValidMind setup command for Atryum:

```bash
./atryum setup validmind
```

2. Follow the prompts:

    - **ValidMind Base URL** — Enter the ValidMind environment you want Atryum to connect to.
    - **ValidMind API key** and **ValidMind API secret** — Enter credentials for that environment.

    Atryum saves the credentials to `$HOME/.config/atryum/atryum.toml`.

3. Restart Atryum so it loads the updated ValidMind credentials.

4. In your browser, navigate to [`localhost:8080/settings`](http://localhost:8080/settings).

5. Under **Agent Record Sync**, select the ValidMind organization, AI agent record type, and custom constitution field to use for synced agents.

6. Click **Save Settings**.

7. Create an AI Evaluation rule in Atryum. This routes matching tool calls to a model that evaluates the call against the agent's constitution.

The constitution defines what the agent is allowed to do, what it must not do, and which requests should require human approval.
