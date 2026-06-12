# Connect ValidMind

Sync your ValidMind organization's agent records to Atryum to map tool invocations to agent records, use agent-scoped rules, and power invocation summarization.

In addition to mapping tool invocations to agent records, allowing Atryum to use agent-scoped rules ([Rules](../3_rules.md)), and powering invocation summarization ([Invocations](../2_invocations.md)), the ValidMind integration also allows you to use Atryum to evaluate tool invocations against each agent's *constitution*:

- A constitution is a plain-language policy that describes what an agent can do, what it cannot do, and which actions require human approval.
- Constitutions can be as terse or expressive as you would like. It's good to call out specifics when you can but be sure to include general statements to give the LLM something to hold on to when seeing an unanticipated tool call.
- Use our provided [`populate-constitution`](https://github.com/validmind/atryum/tree/main/.agents/skills/populate-constitution) skill to help you author effective constitutions.


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

    - **ValidMind Base URL** — The URL for the ValidMind organization you want Atryum to connect to. For example: `https://app.prod.validmind.ai/`
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

5. Within Atryum, click **Settings** in the left sidebar.

6. Under Agent Record Sync, select the:

    - ValidMind **Organization** — The ValidMind organization to sync records from.
    - Organization **Record Type** — The record type that identifies your agent records.
    - **Constitution Field** — The custom long text field that stores each agent's policy.

7. Click **Save Settings** to apply your changes.

## Set up ValidMind AI evaluation rules

Create a rule in Atryum to evaluate matching tool invocations against the synced ValidMind agent's constitution:

1. In Atryum, click **Rules** in the left sidebar.

2. Click **New Rule**.

3. Under **Action**, select `AI Evaluation`.

4. Under **Evaluation Model**, select the ValidMind model configuration to use for the evaluation.

5. Under **Agents**, select the synced ValidMind agent records you want this rule to apply to. Leave this empty to match all agents.

6. Under **Servers / Sources**, select the servers or sources the rule should apply to. Leave this empty to match all servers.

7. Under **Tools**, select the tools the rule should apply to. Leave this empty to match all tools.

8. (Optional) Enter a **Description** so you can remember why the rule exists.

9. Make sure that **Enabled** is checked, then click **Create Rule**.

- When a matching tool call arrives, Atryum sends the call and the agent's constitution to the ValidMind evaluation model. The model returns a verdict for the invocation — approve, deny, escalate to a human, or defer to the next rule.
- If Atryum cannot resolve the agent or its constitution, Atryum denies the invocation.
