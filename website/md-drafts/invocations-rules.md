# Invocations & rules

## Invocations

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

4. (Optional) Enter a reason for denial — this reason is returned to the agent so you can steer what it does next.

5. Click **Deny** to confirm the denial.

## Rules

Rules are if/then policies that tell Atryum how to handle tool invocations:

- Rules let you reuse manual decisions for future tool calls that match the same conditions.
- Rules are applied from top to bottom — the first matching rule wins.

### Create new rules

1. In Atryum, click **Rules** in the left sidebar.

2. Click **New Rule**.

3. Select an **Action**:
    - **Auto Approve** — Allows matching tool calls run without stopping for manual approval.
    - **Auto Deny** — Blocks matching tool calls automatically.
    - **Human Approval** — Pauses matching tool calls until a human approves or denies them.
    - **AI Evaluation** — Sends matching tool calls to a record for review against an agent's constitution — requires Atryum to be synced to ValidMind. ([Connect ValidMind](connect_validmind.md))

4. Select the **Agents**, **Servers / Sources**, and **Tools** that the rule should apply to.

    (AI Evaluation Action only) Select the **Evaluation Model** — The large language model (LLM) that reviews matching tool calls against the agent's constitution and decides whether to approve, deny, escalate to a human, or defer to the next rule. ([placeholder](link))

5. (Optional) Add a **Description** so you can remember why the rule exists.

6. Make sure that **Enabled** is checked, then click **Create Rule**.

### Create rules from existing invocations

1. In Atryum, click **Invocations** in the left sidebar.

2. Click on the invocation you want to create a rule from.

3. On the invocations detail panel, select **Create Rule From This**.

4. Select the:

    - **Action** — What Atryum should do when the rule matches: Auto Approve or Auto Deny
    - **Server patterns** — The MCP server or agent source names this rule applies to in comma-separated format. Leave this empty, or use `*`, to match all servers.
    - **Tool patterns** — The tool names this rule applies to in comma-separated format. Leave this empty, or use `*`, to match all tools on the selected server.
    - **User pattern** — The authenticated agent ID this rule applies to. Leave this empty, or use `*`, to match any agent.

5. (Optional) Enter a **Description** so you can remember why the rule exists.

6. Click **Save Rule** to create your rule.


***

## OLD

Atryum supports 4 rule results: 
- Auto-Approve
- Human Approve
- Auto Deny
- AI Evaluation


AI Evaluation is only available after you have set up an AI provider on the settings page or connected Atryum to a ValidMind organization.


Atryum rules are evaluated top to bottom with 'first match wins' logic. The first rule that matches a tool call returns the result to the system.
However, AI Evaluation is an exception, it can return 'forward for more rule evaluations', usually it does this if the Charter doesn't have anything to do with the tool call being evaluated.


If no rule matches, then atryum falls back to `policy.provider` value from `atryum.toml` and if unset, defaults to `manual_approval`.


Rules can be mapped to agents, servers, and specific tool calls.

Harness level tools (e.g. `read`, `write`) can't be detected by atryum, but you can just type them in and press save.


We reccomend you create 4 sections of rules:
- At the top put rules for tools you never want automatically run, send them to Human Approval or Auto Deny.
- In the middle put your auto approves: list/read operations you want to just happen. Speeds up agents and saves on tokens.
- Below that put your AI Evaluations
- Lastly put an explicit blanket Auto Deny or Human Approval.
