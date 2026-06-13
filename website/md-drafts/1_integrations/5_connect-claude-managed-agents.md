# Connect Claude Managed Agents

Connect Anthropic-hosted Claude Managed Agents to Atryum so Atryum can review hosted tool calls before Claude continues.

Claude Managed Agents run in Anthropic's hosted harness and sandbox. Atryum polls Anthropic for tool approval requests and responds by approving or denynig them.

Managed Agents do not call Atryum before every tool call. Instead, Atryum connects outbound to Anthropic, watches linked session events, creates Atryum invocations for tool-use events, and answers Claude when a session stops for `requires_action`.

Use this integration when you want Atryum to gate Claude Managed Agents tools, including built-in agent tools and MCP tools declared on the Claude agent.

:::
For non-hosted coding agents, refer to [Connect agents](2_connect-agents.md). For upstream MCP servers that Atryum proxies directly, refer to [Connect MCP servers](3_connect-mcp-servers.md).
:::

## How the integration works

In Anthropic terms:

- **Agent** — A reusable Claude configuration, including tools, MCP servers, skills, and a system prompt.
- **Environment** — The hosted sandbox where Anthropic runs sessions.
- **Session** — One running Claude agent task in an environment.
- **Events** — The session log and stream that Atryum watches.

Atryum uses those events to manage approvals:

1. A Claude session emits a tool-use event, such as `agent.tool_use`, `agent.mcp_tool_use`, or `agent.custom_tool_use`.

2. Atryum records the tool call as an invocation and evaluates it against your rules. ([Rules](../3_rules.md))

3. When the Claude session pauses with `session.status_idle` and `requires_action`, Atryum sends Claude an allow or deny response.

4. Atryum records the later tool result on the invocation audit log. ([Invocations](../2_invocations.md))

## Before you begin

Make sure you have:

- [x] An Anthropic API key created in the Claude workspace that owns the managed agents you want to list and watch.
- [x] At least one Claude Managed Agent configured in Anthropic.
- [x] At least one tool on the Agent definition configured with the `always_ask` permission.
- [x] Atryum running with a writable database. ([Quickstart](../1_quickstart.md))
- [x] An Atryum agent record to link to the Claude Managed Agent. ([Tag invocations to agent records](2_connect-agents.md#tag-invocations-to-agent-records))

The `workspace` value in `atryum.toml` is a label Atryum uses for display and ownership metadata. It does not retarget an Anthropic API key to a different workspace. Use an API key created in the target workspace.

## Enable Claude Managed Agents in Atryum

1. Open your `atryum.toml` file.

2. Add one `[[managed_agents]]` block for each Anthropic account or workspace API key you want Atryum to use:

    ```toml
    [[managed_agents]]
    name = "default"
    workspace = "anthropic-workspace-name-or-id"
    api_key = "sk-ant-..."
    ```

    - **name** — The local Atryum account label. Use a unique value when configuring multiple Anthropic accounts.
    - **workspace** — A display and metadata label for the Anthropic workspace.
    - **api_key** — An Anthropic API key created in that workspace.

3. Restart Atryum.

4. Confirm the bridge is available:

    - Open **Agents** in the Atryum platform left sidebar.
    - Open an agent record.
    - Confirm that **Claude Managed Agents** appears in the edit panel.

:::
When no `[[managed_agents]]` entry has an API key, Atryum disables the bridge and the Claude Managed Agents controls are hidden or show a not-configured message.
:::

## Link a Claude Managed Agent

1. In Atryum, click **Agents** in the left sidebar.

2. Open the Atryum agent record that should own the Claude Managed Agent.

3. Under **Claude Managed Agents**, select the Claude agent to link.

    If you configured multiple `[[managed_agents]]` accounts, choose the account first.

4. Click **Save**.

Saving writes Atryum ownership metadata to the Claude Managed Agent. This prevents two Atryum instances from accidentally managing the same hosted Claude agent.

Use **Force connect** only when you intentionally want this Atryum instance to take over a Claude Managed Agent that already has Atryum ownership metadata.

## Configure Claude tool approvals

Claude tools must be configured to ask before Atryum can block them.

Set the Claude Managed Agent tool permission policy to `always_ask`. Tools left as `always_allow` run immediately in Anthropic's harness. Atryum can record those calls after the fact, but it cannot stop them before execution.

## Write rules for Claude Managed Agents

Rules match Claude Managed Agents calls by the source Atryum receives for that tool call.

For built-in Claude agent tools, use:

| Rule field | Value |
| --- | --- |
| Server patterns | `claude-managed-agents` |
| Tool patterns | The Claude tool name, such as `Bash` |

For MCP tools declared on the Claude Managed Agent, use:

| Rule field | Value |
| --- | --- |
| Server patterns | The MCP server name, such as `slack` |
| Tool patterns | The MCP tool name, such as `slack_send_message` |

Rule matching is exact unless you use `*`. Empty pattern lists match everything. Rule order still applies: the first matching final rule wins.

## Manage watched sessions

Atryum discovers sessions for linked Claude Managed Agents and starts watchers automatically.

To review or clear watchers:

1. Open **Settings** in the Atryum platform left sidebar.

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
