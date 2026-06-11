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

5. Connect your preferred coding agent to Atryum. Open your agent's MCP settings and add a standard MCP server with the calc server address: `localhost:8080/mcp/calc`.

    The agent will think it is talking to a calculator MCP server, but its tool invocations now pass through Atryum first.

6. Trigger a test tool call from your agent. For example:

    ```text
    Use the calculator tools and show me 2*2
    ```

7. Within Atryum, click **Invocations** in the left sidebar. Confirm that the calculator invocation request is `Pending Approval` — human approval is required by default.

    - Under Approval Required, select **Approve** to let the tool call run.
    - Verify that the invocation's <span style="font-variant: small-caps;">auth</span> is `Succeeded` and that the approval was <span style="font-variant: small-caps;">decided by</span> a <span style="font-variant: small-caps;">human</span>.

To learn more about working with invocations and what rules you can set up to manage invocations, refer to **[Invocations & rules](invocations_and_rules.md)**.

## Integrate Atryum

After setting up your test calculator server, connect Atryum with your coding agents and ValidMind to streamline your agent oversight in one platform.

### With coding agents

Connect your coding agents to Atryum, allowing Atryum to review tool invocations before your agents run them. Hooks and extensions are available for [Claude Code](https://www.anthropic.com/claude-code), [Cursor](https://cursor.com), [Amp](https://ampcode.com), [Pi](https://pi.dev), and [Codex](https://openai.com/codex).

To retrieve the available setup options for each supported coding agent:

```bash
./atryum hooks --help
```

To learn more about connecting agents with Atryum, refer to **[Connect agents](connect_agents.md)**.

### With ValidMind

Sync your ValidMind organization's agent records to Atryum to map tool invocations to agent records, and use agent-scoped rules.

Connecting agent records from ValidMind allows you to use Atryum to evaluate tool invocations against each agent’s *constitution*. A constitution is a plain-language policy that describes what an agent can do, what it cannot do, and which actions require human approval.

#### Prerequisites

Before connecting Atryum to ValidMind, prepare the agent records that Atryum will sync:

- [x] Set up a ValidMind record type for AI agents if one does not already exist. ([Manage inventory record types](https://docs.validmind.ai/guide/inventory/manage-inventory-record-types.html))
- [x] Add a custom long text field to that record type for each agent's constitution. ([Manage inventory fields](https://docs.validmind.ai/guide/inventory/manage-inventory-fields.html))
- [x] Create at least one record in that record type and fill in its constitution field. ([Register records in the inventory](https://docs.validmind.ai/guide/inventory/register-records-in-inventory.html))
- [x] Make sure you have valid ValidMind API and secret keys for the organization you want Atryum to connect to. ([Manage your profile](https://docs.validmind.ai/guide/configuration/manage-your-profile.html#access-keys))

#### Connect Atryum to ValidMind

1. Set up ValidMind for Atryum:

```bash
./atryum setup validmind
```

2. Follow the prompts and enter your:

    - **ValidMind Base URL** — The URL for ValidMind organization you want Atryum to connect to. For example: `https://app.prod.validmind.ai/`
    - **ValidMind API key**  — Your organization API Key.
    - **ValidMind API secret** — Your organization Secret Key.

3. Atryum prints the path to the updated `atryum.toml` file. Open the file and verify that it has been edited with your submitted configurations:
    - On macOS, this is typically under `~/Library/Application Support/atryum/atryum.toml`.
    - On Linux, this is typically under `~/.config/atryum/atryum.toml`.

4. Restart Atryum so it loads the updated ValidMind credentials:

    a. In the terminal where Atryum is running, press `Ctrl+C` to stop it.
    b. Start Atryum again:

        ```bash
        ./atryum run --init-servers
        ```

5. In your browser, navigate to [`localhost:8080/ui/settings`](http://localhost:8080/ui/settings).

6. Under **Agent Record Sync**, select the:

    - ValidMind **Organization**
    - Organization **Record Type**
    - **Constitution Field**

    The UI labels the constitution field as optional because agent sync can run without it. For this quickstart, select it because AI Evaluation needs the constitution field to evaluate tool invocations against each agent's policy.

7. Click **Save Settings** to apply your changes.

8. Create an AI Evaluation rule in Atryum. This routes matching tool invocations to a model that evaluates the call against the agent's constitution.

