# Integrations

## Connect ValidMind

In addition to mapping tool invocations to agent records and allowing Atryum to use agent-scoped rules, the ValidMind integration also allows you to optionally use Atryum to evaluate tool invocations against each agent’s *constitution*:

- A constitution is a plain-language policy that describes what an agent can do, what it cannot do, and which actions require human approval.
- Constitutions can be as terse or expressive as you would like. It's good to call out specifics when you can but be sure to include general statements to give the LLM something to hold on to when seeing an unanticipated tool call.
- Use our provided [`populate-constitution`](https://github.com/validmind/atryum/tree/main/.agents/skills/populate-constitution) skill to help you author effective constitutions.


### Prerequisites

Before connecting Atryum to ValidMind:

- [x] Set up a ValidMind record type for AI agents if one does not already exist. ([Manage inventory record types](https://docs.validmind.ai/guide/inventory/manage-inventory-record-types.html))
- [x] Add a custom long text field to that record type for each agent's constitution. ([Manage inventory fields](https://docs.validmind.ai/guide/inventory/manage-inventory-fields.html))
- [x] Create at least one record in that record type and fill in its constitution field. ([Register records in the inventory](https://docs.validmind.ai/guide/inventory/register-records-in-inventory.html))
- [x] Make sure you have valid ValidMind API and secret keys for the organization you want Atryum to connect to. ([Manage your profile](https://docs.validmind.ai/guide/configuration/manage-your-profile.html#access-keys))

### Connect Atryum to ValidMind

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

### Set up ValidMind AI evaluation rules

Create a rule in Atryum to route matching tool invocations to a record that evaluates the call against the ValidMind agent's constitution:

1. In Atryum, click **Rules** in the left sidebar.

2. Click **New Rule**.

3. Under **Action**, select `AI Evaluation`.

4. Under **Evaluation Model**, select the ValidMind model configuration to use for the evaluation.

5. Under **Agents**, select the synced ValidMind agent records you want this rule to apply to. Leave this empty to match all agents.

6. Under **Servers/Sources**, select the servers or sources the rule should apply to. Leave this empty to match all servers.

7. Under **Tools**, select the tools the rule should apply to. Leave this empty to match all tools.

8. (Optional) Enter a **Description** so you can remember why the rule exists.

9. Make sure that **Enabled** is checked, then click **Create**.

- When a matching tool call arrives, Atryum sends the call and the agent's constitution to the ValidMind evaluation model. The model returns a verdict for the invocation — approve, deny, escalate to a human, or defer to the next rule.
- If Atryum cannot resolve the agent or its constitution, Atryum denies the invocation.

## Connect agents

Connect your coding agents to Atryum so tool invocations pass through Atryum before they run. Atryum evaluates each call against your rules, routes calls that need review to human approval, and records every outcome in the invocation audit log.

To retrieve the available setup options for each supported coding agent:

```bash
./atryum hooks --help
```

### Connect Cursor and Claude code

The hooks command currently supports direct setup for Cursor and Claude Code.
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

### Connect other coding agents

Other agent integrations connect through Atryum's Model Context Protocol (MCP) proxy instead of calling upstream MCP servers directly.

Add a standard MCP connection entry wherever your agent expects it and point it at: `http://<atryum-host-and-port>/mcp/<server_name>`

- Replace `<atryum-host-and-port>` with your Atryum base URL. The default local address is `localhost:8080`.
- Replace `<server_name>` with the name you gave the MCP server under **Servers** in the Atryum UI.

Refer to the setup examples for:

- [Claude Code](https://github.com/validmind/atryum/tree/main/examples/claude-code-hook)
- [Amp](https://github.com/validmind/atryum/tree/main/examples/amp-plugin)
- [Pi](https://github.com/validmind/atryum/tree/main/examples/pi-extension)
- [Codex](https://github.com/validmind/atryum/tree/main/examples/codex-mcp)
- [Other harnesses](https://github.com/validmind/atryum/tree/main/examples)

These integrations currently support no-auth mode only. Your agent sends tool invocations to Atryum over the network without OAuth credentials. Use OAuth-backed Atryum when you need verified agent identity instead of self-declared labels.

Before starting your agent, export these environment variables in the same shell session:

- **ATRYUM_URL** — Base URL of your Atryum server. Defaults to `http://localhost:8080`. Change this when Atryum runs on another host or port.
- **ATRYUM_AGENT_ID** — Self-declared agent identifier that Atryum matches against Agent Record **Agent IDs**. Leave this unset if you do not need invocations tagged to a specific agent record.

For example:

```bash
export ATRYUM_URL=http://localhost:8080
export ATRYUM_AGENT_ID=amp-local
```

To tag invocations to an Agent Record and apply agent-scoped rules:

1. In Atryum, open an Agent Record or create one.
2. Add a stable string to its **Agent IDs** field, such as `amp-local` or `pi-alice`.
3. Export the same string as `ATRYUM_AGENT_ID` before starting your agent.
4. Try a tool invocation again. Atryum should attach the call to that Agent Record instead of leaving the agent column empty.

Optionally, set these variables to label the harness in Atryum:

- **ATRYUM_CLIENT_NAME** — Harness name shown in the Atryum agent column. Defaults to each integration's source label when unset.
- **ATRYUM_CLIENT_VERSION** — Harness version shown in Atryum. Some integrations also read their native version variables, such as `AMP_VERSION` or `PI_VERSION`.

Make sure Atryum is running and reachable at `ATRYUM_URL` before you start your agent. Pending tool calls appear under **Invocations** in the Atryum UI.

## Configure LLM providers

In addition to powering AI evaluation rules ([Rules](rules.md)) and invocation summarization ([Invocations](invocations.md)), configuring local large language model (LLM) providers allow you to use credentials and endpoints controlled by your team  to evaluate tool calls against each agent's *constitution* and generate plain-language invocation summaries:

- A constitution is a plain-language policy that describes what an agent can do, what it cannot do, and which actions require human approval.
- Constitutions can be as terse or expressive as you would like. It's good to call out specifics when you can but be sure to include general statements to give the LLM something to hold on to when seeing an unanticipated tool call.
- Use our provided [`populate-constitution`](https://github.com/validmind/atryum/tree/main/.agents/skills/populate-constitution) skill to help you author effective constitutions.

### When should I use local LLMs?

Use local LLMs when you want to:

- Run AI evaluations with a local or self-hosted model instead of a ValidMind model.
- Bring your own API keys for [OpenAI](https://platform.openai.com), [Anthropic](https://www.anthropic.com), or any OpenAI-compatible endpoint ([Ollama](https://ollama.com), [LM Studio](https://lmstudio.ai), [Azure OpenAI](https://azure.microsoft.com/products/ai-services/openai-service), and similar).
- Keep evaluation and summarization on your network in on-prem or air-gapped setups.
- Register multiple models and pick the right one per rule or feature.

| | ValidMind model | Local LLM |
| --- | --- | --- |
| Requires ValidMind connection | Yes | No |
| Model list source | Provided by ValidMind | Provided by your team |
| Required API credentials | Your ValidMind API key and secret, configured in `atryum.toml` when you connect Atryum to ValidMind | Provider key or endpoint configured per local LLM |
| Designed for | Teams using ValidMind out-of-the-box setup | Standalone, on-prem, or bring-your-own-key setups |

### Add local LLM providers

1. In Atryum, click **Settings** in the left sidebar.

2. Under Local LLM Configurations, click **Add LLM**.

3. Provide the details for your LLM:

    - **Name**
    - **Provider** — Choose one of:  `OpenAI`, `Anthropic`, `OpenAI-compatible` (For Ollama, LM Studio, Azure OpenAI, and other endpoints that expose an OpenAI-style API)
    - **Model** — The model identifier your provider expects, such as `gpt-4o`, `claude-3-5-sonnet-latest`, or `llama3`.
    - **API Key** — Your provider API key that will be stored encrypted.
    - (OpenAI-compatible only) **Base URL**  — The root URL of the endpoint, such as `http://localhost:11434` for Ollama.

4. Make sure that **Enabled** is checked, then click **Add LLM**.

5. Confirm that your LLM appears as expected in the table with its <span style="font-variant: small-caps;">name</span>, <span style="font-variant: small-caps;">provider</span>, <span style="font-variant: small-caps;">model</span>, and <span style="font-variant: small-caps;">status</span>.

### Set up local LLM AI evaluation rules

After you add at least one enabled local LLM, you can set up a rule for an `AI Evaluation` Action:

1. In Atryum, click **Rules** in the left sidebar.

2. Click **New Rule**.

3. Under **Action**, select `AI Evaluation`.

4. Select a model from `Local LLMs` under **Evaluation Model**.

5. Under **Agents**, select the agents you want this rule to apply to. Leave this empty to match all agents.

6. Under **Servers/Sources**, select the servers or sources the rule should apply to. Leave this empty to match all servers.

7. Under **Tools**, select the tools the rule should apply to. Leave this empty to match all tools.

8. (Optional) Enter a **Description** so you can remember why the rule exists.

9. Make sure that **Enabled** is checked, then click **Create Rule**.

- When a matching tool call arrives, Atryum sends the call and the agent's constitution to your local LLM. The model returns a verdict for the invocation — approve, deny, escalate to a human, or defer to the next rule.
- If the agent has no constitution configured, Atryum denies the invocation.

### Edit or remove local LLM providers

#### Edit local LLM providers

1. In Atryum, click **Settings** in the left sidebar.

2. Under **Local LLM Configurations**, click the provider you want to edit.

3. Make your updates to the provider details:

    - **Name**
    - **Provider** — Choose one of:  `OpenAI`, `Anthropic`, `OpenAI-compatible`
    - **Model** — The model identifier your provider expects.
    - **API Key** — Leave blank when editing an existing configuration to keep the current key.
    - (OpenAI-compatible only) **Base URL**  — The root URL of the endpoint.

4. To turn providers on or off:

    - Check **Enabled** to turn the provider on.
    - Uncheck **Enabled** to turn the provider off. Disabled LLMs remain listed but are not offered when creating rules or choosing a summary model.

5. Click **Save Changes** to apply your edits.

#### Remove local LLM providers

:::
Removing a local LLM provider configuration is permanent and cannot be undone.
:::

1. In Atryum, click **Settings** in the left sidebar.

2. Under Local LLM Configurations, click **Delete** on the row for the provider you want to remove.


