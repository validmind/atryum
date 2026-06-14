# Quickstart

Install and initialize Atryum, then integrate Atryum with ValidMind and your other agentic tools.

## Install & initialize Atryum

Download Atryum and register a server in Atryum for testing.

:::
Atryum is not production ready yet.
:::

### Download Atryum

```bash
curl -fsSL https://github.com/validmind/atryum/raw/main/install_atryum.sh | bash
```

The install script downloads the latest Atryum release and selects the correct binary for your operating system and CPU architecture. Supported platforms are [Linux](https://www.kernel.org/) and [macOS](https://www.apple.com/macos/), each on `amd64` (`x86_64`) or `arm64` (`aarch64`).

[Microsoft Windows](https://www.microsoft.com/windows/) is not supported natively — use [Windows Subsystem for Linux (WSL)](https://learn.microsoft.com/en-us/windows/wsl/) as a workaround. Alternatively, clone the [repository](https://github.com/validmind/atryum) and run Atryum with [Docker](https://www.docker.com/) — refer to the repository [README](https://github.com/validmind/atryum/blob/main/README.md#running) for [Docker Compose](https://docs.docker.com/compose/) setup.

### Set up Atryum test server

:::
This quickstart will run atryum with a local sqlite. This is great for first use, bad for performance and real work. See [docker-compose.yaml](https://github.com/validmind/atryum/blob/main/docker-compose.yml#L2) for an example running with postgres.
:::

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

    - Open your agent's MCP settings and add a standard MCP server with the calc server address:

    ```
    http://localhost:8080/mcp/calc
    ```

    - The agent will think it is talking to a calculator MCP server, but its tool invocations now pass through Atryum first.

6. Trigger a test tool call from your agent. For example:

    ```text
    Use the calculator tools and show me 2*2
    ```

7. Within Atryum, click **Invocations** in the left sidebar to review tool calls. Confirm that the calculator invocation request is `Pending Approval` — human approval is required by default.

    - Under Approval Required, select **Approve** to let the tool call run.
    - Verify that the invocation's <span style="font-variant: small-caps;">status</span> is `Succeeded` and that the approval was <span style="font-variant: small-caps;">decided by</span> a <span style="font-variant: small-caps;">human</span>.

:::
To learn more about working with invocations, refer to **[Invocations](2_invocations.md)**.
:::

### Set up calculation server rule

Rules are if/then policies that tell Atryum how to handle tool invocations. For example — if the server is `calc`, then approve the call automatically.

Let's add a rule to control future matching tool calls:

1. In Atryum, click **Rules** in the left sidebar.

2. Click **New Rule**.

3. Under **Action**, select `Auto Approve`.

4. Under **Servers / Sources**, select `calc` to apply the rule only to calls to the test calculator server.

5. Make sure that **Enabled** is checked, then click **Create Rule**.

6. Try a calculator prompt from your agent again. Atryum should apply the new rule instead of treating the tool call like a brand-new manual decision:

    - Within Atryum, click **Invocations** in the left sidebar and confirm that your new call `Succeeded`.
    - Verify that the approval was <span style="font-variant: small-caps;">decided by</span> a <span style="font-variant: small-caps;">rule</span>.

:::
To learn more about setting up rules to manage invocations, refer to **[Rules](3_rules.md)**.
:::

## Integrate Atryum

After setting up your test calculator server, connect Atryum with ValidMind and the rest of your tools to streamline your agentic oversight in one platform.

### With ValidMind

Sync your ValidMind organization's agent records to Atryum to map tool invocations to agent records, use agent-scoped rules, and power invocation summarization.

Connecting agent records from ValidMind also allows you to use Atryum to evaluate tool invocations against each agent's *charter*, a plain-language policy that describes what an agent can do, what it cannot do, and which actions require human approval.

:::
To learn more about connecting ValidMind with Atryum, refer to **[Connect ValidMind](1_integrations/1_connect-validmind.md)**.
:::


### With coding agents

Connect your coding agents to Atryum, allowing Atryum to review tool invocations before your agents run them. Hooks and extensions are available for [Claude Code](https://www.anthropic.com/claude-code), [Cursor](https://cursor.com), [Amp](https://ampcode.com), [Pi](https://pi.dev), and [Codex](https://openai.com/codex).

:::
To learn more about connecting agents with Atryum, refer to **[Connect agents](1_integrations/2_connect-agents.md)**.
:::

### With MCP servers

Register upstream Model Context Protocol (MCP) servers so Atryum can proxy tool calls to them. Atryum evaluates each call against your rules, holds upstream credentials on your behalf, and forwards approved calls to the server.

Connect MCP servers to add other upstream tools — such as local subprocess servers, remote HTTP servers with a static bearer token, or OAuth-protected services.

:::
To learn more about registering MCP servers in Atryum, refer to **[Connect MCP servers](1_integrations/3_connect-mcp-servers.md)**.
:::

### With local LLM providers

Set up local large language model (LLM) providers in Atryum to power AI evaluation rules and invocation summarization with models and credentials your team controls. Configuring LLM providers also allows you to use credentials and endpoints controlled by your team to evaluate tool calls against each agent's *charter*, a plain-language policy that describes what an agent can do, what it cannot do, and which actions require human approval.

Local LLMs are useful for bring-your-own-key setups, on-prem or air-gapped deployments, and teams that want to register multiple models and pick the right one per rule or feature.

:::
To learn more about configuring local LLM providers in Atryum, refer to **[Configure LLM providers](1_integrations/4_configure-llm-providers.md)**.
:::





