# Rules

Configure rules that control how Atryum handles future tool calls.

## What are rules?

Rules are if/then policies that tell Atryum how to handle tool invocations. Rules let you reuse manual decisions for future tool calls that match the same conditions.

Rules support four types of actions:

1. **Auto Approve** — Allows matching tool calls to run without stopping for manual approval.
2. **Auto Deny** — Blocks matching tool calls automatically.
3. **Human Approval** — Pauses matching tool calls until a human approves or denies them.
4. **AI Evaluation** — Sends matching tool calls for review against an agent's charter — requires Atryum to be synced to ValidMind or a configured local large language model (LLM). ([Connect ValidMind](1_integrations/1_connect-validmind.md), [Configure LLM providers](1_integrations/4_configure-llm-providers.md))

## Rules best practices

### Rule ordering

Rules are generally applied from top to bottom — the first matching rule wins, with the exception of AI Evaluation actions. In the case of AI evaluations, when the agent's charter does not apply to the tool call, the evaluation model can defer to the next matching rule instead of deciding.

As such, we recommend you curate four tiers of rules:

1. At the top, put rules for tools you never want automatically run. Set their **Action**s to `Human Approval` or `Auto Deny`.
2. Then, put rules for list/read operations you want to automatically run. Set their **Action**s to `Auto Approve` — this speeds up agents and saves on tokens.
3. Below that, set your rules for `AI Evaluation` **Action**s.
4. Finally, set up an explicit blanket rule with the **Action** set to `Auto Deny` or `Human Approval`.

### Rule matching

- When no rule matches a tool call, Atryum uses the fallback action set in your `atryum.toml` configuration file under `policy.provider`. Available values:

    - **`always_approve`** — Run the tool call without stopping for approval.
    - **`manual_approval`** — Pause the call until a human approves or denies it. Manual approval is the default when unset.
    - **`always_deny`** — Block the tool call automatically.

- Each rule can apply to specific agents, Model Context Protocol (MCP) servers, and tools. Leave a field empty or use `*` to match all agents, servers, or tools in that category.

### Harness-level tools

Some tools are built into your coding agent's harness (for example, `read` and `write` in Cursor or Claude Code) rather than coming from MCP servers registered in Atryum.

These are not listed automatically on the Rules page — enter the exact tool name manually when creating a rule.

## Create rules

1. In Atryum, click **Rules** in the left sidebar.

2. Click **New Rule**.

3. Select an **Action** type.

4. Select the **Agents**, **Servers / Sources**, and **Tools** that the rule should apply to.

    (AI Evaluation Action only) Select the **Evaluation Model** — The large language model (LLM) that reviews matching tool calls against the agent's charter and decides whether to approve, deny, escalate to a human, or defer to the next rule. ([Connect ValidMind](1_integrations/1_connect-validmind.md), [Configure LLM providers](1_integrations/4_configure-llm-providers.md))

5. (Optional) Add a **Description** so you can remember why the rule exists.

6. Make sure that **Enabled** is checked, then click **Create Rule**.

### Create rules from existing invocations

1. In Atryum, click **Invocations** in the left sidebar.

2. Click on the invocation you want to create a rule from.

3. On the invocation detail panel, select **Create Rule From This**.

4. Select the:

    - **Action** — What Atryum should do when the rule matches: Auto Approve or Auto Deny
    - **Server patterns** — The MCP server or agent source names this rule applies to in comma-separated format. Leave this empty, or use `*`, to match all servers.
    - **Tool patterns** — The tool names this rule applies to in comma-separated format. Leave this empty, or use `*`, to match all tools on the selected server.
    - **User pattern** — The authenticated agent ID this rule applies to. Leave this empty, or use `*`, to match any agent.

5. (Optional) Enter a **Description** so you can remember why the rule exists.

6. Click **Save Rule** to create your rule.

## Edit or reorder rules

### Edit rules

1. In Atryum, click **Rules** in the left sidebar.

2. Click on the rule you want to edit to make your changes to:

    - The **Action** type
    - The **Agents**, **Servers / Sources**, and **Tools** that the rule should apply to
    - (AI Evaluation Action only) The **Evaluation Model** that reviews matching tool calls against the agent's charter and decides whether to approve, deny, escalate to a human, or defer to the next rule. ([Connect ValidMind](1_integrations/1_connect-validmind.md), [Configure LLM providers](1_integrations/4_configure-llm-providers.md))
    - (Optional) The rule **Description**

3. To turn rules on or off:

    - Check **Enabled** to turn the rule on.
    - Uncheck **Enabled** to turn the rule off.

4. Click **Save** to apply your changes.

### Reorder rules

1. In Atryum, click **Rules** in the left sidebar.

2. Hover over the rule you want to reorder.

3. Under the <span style="font-variant: small-caps;">order</span> column, click:

    - **&#8743;** to move the rule up.
    - **&#8744;** to move the rule down.

Changes to rule ordering are saved automatically.

## Delete rules

:::
Deleting a rule is permanent and cannot be undone.
:::

1. In Atryum, click **Rules** in the left sidebar.

2. Click the rule you want to delete.

3. Click **Delete**, then select **OK** to confirm deletion.




