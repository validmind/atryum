# Connect Claude Managed Agents

Connect Anthropic-hosted Claude Managed Agents to Atryum to record hosted tool invocations, evaluate invocations against agent-scoped rules, and gate invocations when Anthropic pauses for approval.

Unlike local coding agents that send each tool call to Atryum before execution, Claude Managed Agents run on Anthropic's hosted infrastructure.

Under Anthropic's model, an *agent* is a reusable Claude configuration (tools, MCP servers, skills, and system prompt); an *environment* is the hosted sandbox where sessions run; a *session* is one running task; and *events* are the session log and stream that Atryum watches.

Atryum connects outbound to Anthropic and uses those events to record, evaluate, and gate tool calls — including built-in agent tools and MCP tools declared on the Claude agent:

1. A session emits a tool-use event, such as `agent.tool_use`, `agent.mcp_tool_use`, or `agent.custom_tool_use`. Atryum records it as an invocation and evaluates it against your rules. ([Rules](../3_rules.md))
2. When the session pauses with `session.status_idle` and `requires_action`, Atryum sends Claude an allow or deny response before the tool runs.
3. Atryum records the later tool result from the environment on the invocation log. ([Invocations](../2_invocations.md))

Tools configured as `always_allow` run without waiting for Atryum. Atryum can record those calls afterward, but it cannot block them before execution.

:::
- To connect non-hosted coding agents, refer to [Connect agents](2_connect-agents.md).
- To connect upstream MCP servers that Atryum proxies directly, refer to [Connect MCP servers](3_connect-mcp-servers.md).
:::

## Prerequisites

Before connecting Atryum to Claude Managed Agents, make sure you have:

- [x] An Anthropic API key created in the Claude workspace that owns the managed agents you want to list and watch.
- [x] At least one Claude Managed Agent configured in Anthropic.
- [x] At least one tool on the Agent definition configured with the `always_ask` permission. Tools set to `always_allow` run immediately in the hosted agent; Atryum can record those calls after the fact, but it cannot stop them before execution.
- [x] Atryum running with a writable database. ([Quickstart](../1_quickstart.md))
- [x] An Atryum agent record to link to the Claude Managed Agent. ([Connect agents](2_connect-agents.md#tag-invocations-to-agent-records))

## Enable Claude Managed Agents in Atryum

1. Open your `atryum.toml` configuration file:

    - On macOS, this is typically under `~/Library/Application Support/atryum/atryum.toml`.
    - On Linux, this is typically under `~/.config/atryum/atryum.toml`.

2. Add oane `[[managed_agents]]` block for each Anthropic account or workspace API key you want Atryum to use:

    ```toml
    [[managed_agents]]
    name = "default"
    workspace = "anthropic-workspace-name-or-id"
    api_key = "sk-ant-..."
    ```

    - **name** — The local Atryum account label. Use a unique value when configuring multiple Anthropic accounts.
    - **workspace** — A display and metadata label for the Anthropic workspace. It is not sent to Anthropic as a selector; use an API key created in the workspace whose agents you want to list.
    - **api_key** — An Anthropic API key created in that workspace.

3. Restart Atryum so it loads the updated Claude Managed Agents credentials:

    a. In the terminal where Atryum is running, press `Ctrl+C` to stop it.
    b. Start Atryum again:

        ```bash
        ./atryum run --init-servers
        ```
4. Confirm the bridge is available:

    a. Within Atryum, click **Agents** in the left sidebar.
    b. Click on an agent record to open its details.
    c. Confirm that **Claude Managed Agents** appears in the edit panel.

:::
When no `[[managed_agents]]` entry has an API key, Atryum disables the bridge and the Claude Managed Agents controls are hidden or show a not-configured message.
:::


## Link Claude Managed Agents

4. In Atryum, click **Agents** in the left sidebar.

5. Open the Atryum agent record that should own the Claude Managed Agent.

6. Under **Claude Managed Agents**, select the Claude agent to link.

    If you configured multiple `[[managed_agents]]` accounts, choose the account first.

7. Click **Save**.

Saving writes Atryum ownership metadata to the Claude Managed Agent. This prevents two Atryum instances from accidentally managing the same hosted Claude agent.

Use **Force connect** only when you intentionally want this Atryum instance to take over a Claude Managed Agent that already has Atryum ownership metadata.

When no `[[managed_agents]]` entry has an API key, Atryum disables the bridge and the Claude Managed Agents controls are hidden or show a not-configured message.

## Set up Claude Managed Agents rules

Create a rule in Atryum to evaluate matching Claude Managed Agents tool invocations:

1. In Atryum, click **Rules** in the left sidebar.

2. Click **New Rule**.

3. Under **Action**, select the action you want — for example `AI Evaluation`, `Allow`, `Deny`, or `Escalate`.

4. (For AI Evaluation) Under **Evaluation Model**, select the model configuration to use for the evaluation.

5. Under **Agents**, select the Atryum agent records linked to Claude Managed Agents you want this rule to apply to. Leave this empty to match all agents.

6. Under **Servers / Sources**, select the servers or sources the rule should apply to. Leave this empty to match all servers.

    - For built-in Claude agent tools, use `claude-managed-agents`.
    - For MCP tools declared on the Claude Managed Agent, use the MCP server name, such as `slack`.

7. Under **Tools**, select the tools the rule should apply to. Leave this empty to match all tools.

    - For built-in Claude agent tools, use the Claude tool name, such as `Bash`.
    - For MCP tools, use the MCP tool name, such as `slack_send_message`.

8. (Optional) Enter a **Description** so you can remember why the rule exists.

9. Make sure that **Enabled** is checked, then click **Create Rule**.

- When a Claude session emits a tool-use event, Atryum records it as an invocation and evaluates it against your rules.
- When the session pauses with `requires_action`, Atryum sends Claude an allow or deny response.
- Atryum records the later tool result on the invocation audit log.
- Rule matching is exact unless you use `*`. Empty pattern lists match everything. Rule order still applies: the first matching final rule wins.

## Manage watched sessions

Atryum discovers sessions for linked Claude Managed Agents and starts watchers automatically.

To review or clear watchers:

1. In Atryum, click **Settings** in the left sidebar.

2. Find **Claude Managed Agents**.

3. Review the watched sessions table.

4. Click **Delete** to remove one stale watcher, or click **Clear all** to remove every watcher.

Clearing watchers does not remove Claude agent links. Watchers for linked Claude agents can come back when the discovery loop finds active sessions again.

## Troubleshooting

### The Claude agent list shows the wrong workspace

Create or use an Anthropic API key in the workspace you want Atryum to list. The `workspace` value in `atryum.toml` is not sent to Anthropic as a selector.

### A tool call was recorded but not blocked

Check the Claude Managed Agent's tool permission policy. If the tool is `always_allow`, Anthropic runs it without waiting for Atryum.

### A rule did not match an MCP tool

Use the MCP server name as the rule's server pattern. For example, a Slack MCP tool call uses `slack` as the server pattern, not `claude-managed-agents`.

### Old watchers keep logging errors

Open **Settings** → **Claude Managed Agents** and click **Clear all**. Linked agents can rediscover active sessions afterward.
