# Configure LLM providers

Configure local LLM providers to power AI evaluation rules and invocation summarization.

In addition to powering AI evaluation rules ([Rules](../3_rules.md)) and invocation summarization ([Invocations](../2_invocations.md)), configuring local large language model (LLM) providers also allows you to use credentials and endpoints controlled by your team to evaluate tool calls against each agent's *constitution*:

- A constitution is a plain-language policy that describes what an agent can do, what it cannot do, and which actions require human approval.
- Constitutions can be as terse or expressive as you would like. It's good to call out specifics when you can but be sure to include general statements to give the LLM something to hold on to when seeing an unanticipated tool call.
- Use our provided [`populate-constitution`](https://github.com/validmind/atryum/tree/main/.agents/skills/populate-constitution) skill to help you author effective constitutions.

## When should I use local LLMs?

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

## Add local LLM providers

1. In Atryum, click **Settings** in the left sidebar.

2. Under Local LLM Configurations, click **Add LLM**.

3. Provide the details for your LLM:

    - **Name**
    - **Provider** — Choose one of: `OpenAI`, `Anthropic`, `OpenAI-compatible` (for Ollama, LM Studio, Azure OpenAI, and other endpoints that expose an OpenAI-style API)
    - **Model** — The model identifier your provider expects, such as `gpt-4o`, `claude-3-5-sonnet-latest`, or `llama3`.
    - **API Key** — Your provider API key that will be stored encrypted.
    - (OpenAI-compatible only) **Base URL** — The root URL of the endpoint, such as `http://localhost:11434` for Ollama.

4. Make sure that **Enabled** is checked, then click **Add LLM**.

5. Confirm that your LLM appears as expected in the table with its <span style="font-variant: small-caps;">name</span>, <span style="font-variant: small-caps;">provider</span>, <span style="font-variant: small-caps;">model</span>, and <span style="font-variant: small-caps;">status</span>.

## Set up local LLM AI evaluation rules

After you add at least one enabled local LLM, you can set up a rule for an AI evaluation action:

1. In Atryum, click **Rules** in the left sidebar.

2. Click **New Rule**.

3. Under **Action**, select `AI Evaluation`.

4. Select a model from `Local LLMs` under **Evaluation Model**.

5. Under **Agents**, select the agents you want this rule to apply to. Leave this empty to match all agents.

6. Under **Servers / Sources**, select the servers or sources the rule should apply to. Leave this empty to match all servers.

7. Under **Tools**, select the tools the rule should apply to. Leave this empty to match all tools.

8. (Optional) Enter a **Description** so you can remember why the rule exists.

9. Make sure that **Enabled** is checked, then click **Create Rule**.

- When a matching tool call arrives, Atryum sends the call and the agent's constitution to your local LLM. The model returns a verdict for the invocation — approve, deny, escalate to a human, or defer to the next rule.
- If the agent has no constitution configured, Atryum denies the invocation.

## Edit or remove local LLM providers

### Edit local LLM providers

1. In Atryum, click **Settings** in the left sidebar.

2. Under **Local LLM Configurations**, click the provider you want to edit.

3. Make your updates to the provider details:

    - **Name**
    - **Provider** — Choose one of: `OpenAI`, `Anthropic`, `OpenAI-compatible`
    - **Model** — The model identifier your provider expects.
    - **API Key** — Leave blank when editing an existing configuration to keep the current key.
    - (OpenAI-compatible only) **Base URL** — The root URL of the endpoint.

4. To turn providers on or off:

    - Check **Enabled** to turn the provider on.
    - Uncheck **Enabled** to turn the provider off. Disabled LLMs remain listed but are not offered when creating rules or choosing a summary model.

5. Click **Save Changes** to apply your edits.

### Remove local LLM providers

:::
Removing a local LLM provider configuration is permanent and cannot be undone.
:::

1. In Atryum, click **Settings** in the left sidebar.

2. Under Local LLM Configurations, click **Delete** on the row for the provider you want to remove.
