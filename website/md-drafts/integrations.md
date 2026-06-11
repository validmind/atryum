# Atryum integrations

# Connect ValidMind

Sync your ValidMind organization's agent records to Atryum to map tool invocations to agent records and use agent-scoped rules.

## The ValidMind Atryum integration

In addition to mapping tool invocations to agent records and allowing Atryum to use agent-scoped rules, the ValidMind integration also allows you to optionally use Atryum to evaluate tool invocations against each agent’s *constitution*. A constitution is a plain-language policy that describes what an agent can do, what it cannot do, and which actions require human approval.

## Prerequisites

Before connecting Atryum to ValidMind:

- [x] Set up a ValidMind record type for AI agents if one does not already exist. ([Manage inventory record types](https://docs.validmind.ai/guide/inventory/manage-inventory-record-types.html))
- [x] Add a custom long text field to that record type for each agent's constitution. ([Manage inventory fields](https://docs.validmind.ai/guide/inventory/manage-inventory-fields.html))
- [x] Create at least one record in that record type and fill in its constitution field. ([Register records in the inventory](https://docs.validmind.ai/guide/inventory/register-records-in-inventory.html))
- [x] Make sure you have valid ValidMind API and secret keys for the organization you want Atryum to connect to. ([Manage your profile](https://docs.validmind.ai/guide/configuration/manage-your-profile.html#access-keys))

## Connect Atryum to ValidMind

1. Set up ValidMind for Atryum:

```bash
./atryum setup validmind
```

2. Follow the prompts and enter your:

    - **ValidMind Base URL** — The URL for ValidMind organization you want Atryum to connect to. For example: `https://app.prod.validmind.ai/`
    - **ValidMind API key**  — Your organization API Key.
    - **ValidMind API secret** — Your organization Secret Key.

3. Atryum prints the path to your updated `atryum.toml` configuration file. Open the file and verify that it has been edited with your submitted values:
    - On macOS, this is typically under `~/Library/Application Support/atryum/atryum.toml`.
    - On Linux, this is typically under `~/.config/atryum/atryum.toml`.

4. Restart Atryum so it loads the updated ValidMind credentials:

    a. In the terminal where Atryum is running, press `Ctrl+C` to stop it.
    b. Start Atryum again:

        ```bash
        ./atryum run --init-servers
        ```

5. In your browser, navigate to [`localhost:8080/ui/settings`](http://localhost:8080/ui/settings).

6. Under Agent Record Sync, select the:

    - ValidMind **Organization** — The ValidMind organization to sync records from.
    - Organization **Record Type** — The record type that identifies your agent records.
    - **Constitution Field** — The custom long text field that stores each agent's policy.

7. Click **Save Settings** to apply your changes.

## Set up AI evaluation rule

Create a rule in Atryum to route matching tool invocations to a record that evaluates the call against the agent's constitution:

1. In Atryum, click **Rules** in the left sidebar.

2. Click **New Rule**.

3. Under **Action**, select `AI Evaluation`.

4. Under **Evaluation Model**, select the ValidMind model configuration to use for the evaluation.

5. Under **Agents**, select the synced ValidMind agent records you want this rule to apply to. Leave this empty to match all agents.
    - Under **Servers/Sources**, select the servers or sources the rule should apply to. Leave this empty to match all servers.
    - Under **Tools**, select the tools the rule should apply to. Leave this empty to match all tools.

7. Make sure that **Enabled** is checked, then click **Create**.

8. Try a tool invocation from your agent again. Atryum should evaluate the invocation against the agent's constitution instead of treating it like a brand-new manual decision.

# Connect agents

To retrieve the available setup options for each supported coding agent:

```bash
./atryum hooks --help
```

## Install Atryum hooks

The hooks command currently supports direct setup for Cursor and Claude Code. Other agent integrations may use separate extensions or MCP configuration.

To install Atryum hooks for Cursor:

```bash
./atryum hooks install cursor
```

To install Atryum hooks for Claude Code:

```bash
./atryum hooks install claude-code
```

To remove Atryum hook commands later, run the matching uninstall command:

```bash
./atryum hooks uninstall cursor
./atryum hooks uninstall claude-code
```

***

Connecting via MCP:

Add a standard mcp connection json wherever your agent expects it. Use this url:

http://<atryum-host-and-port>/mcp/<server_name>

Where atryumhost-and-port deaults to localhost:8080 and `server_name` is the name you gave the mcp server in the Servers section of the ui.


Connecting via coding agent/harness:

Examples exist in the repo for Claude Code, Amp, Pi, and others. These only support no-auth mode for now so you will need to set config env vars:

ATRYUM_URL
ATRYUM_AGENT_ID

Optionally:
ATRYUM_CLIENT_NAME
ATRYUM_CLIENT_VERSION


