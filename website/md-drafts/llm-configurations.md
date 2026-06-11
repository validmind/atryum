# Local LLM configurations

Configure LLM providers directly in Atryum to power **AI Evaluation** rules and other native LLM features without connecting to ValidMind.

## Use cases

- **AI Evaluation without ValidMind** — Run constitution-based tool-call review using a local or self-hosted model instead of a ValidMind model configuration.
- **Bring your own API keys** — Use OpenAI, Anthropic, or any OpenAI-compatible endpoint (Ollama, LM Studio, Azure OpenAI, and similar) with credentials stored encrypted in Atryum.
- **On-prem or air-gapped setups** — Point Atryum at a local inference server so evaluation and summarization never leave your network.
- **Flexible model choice** — Register multiple LLMs and pick the right one per rule or feature.

AI Evaluation with a local LLM requires each agent to have a **constitution** — plain-language policy text that tells the model what the agent may do. You can define constitutions on the **Agents** page in Atryum, or sync them from ValidMind if you are connected. See **[Charters](charters.md)** for guidance on writing effective constitutions.

## Add a local LLM

1. In your browser, navigate to [`localhost:8080/ui/settings`](http://localhost:8080/ui/settings).

2. Under **Local LLM Configurations**, click **Add LLM**.

3. Fill in the configuration:

    - **Name** — A label you will recognize later, such as `Local Llama 3` or `GPT-4o Eval`.
    - **Provider** — Choose one of:
        - `OpenAI`
        - `Anthropic`
        - `OpenAI-compatible` — For Ollama, LM Studio, Azure OpenAI, and other endpoints that expose an OpenAI-style API.
    - **Model** — The model identifier your provider expects, such as `gpt-4o`, `claude-3-5-sonnet-latest`, or `llama3`.
    - **API Key** — Your provider API key. Stored encrypted. Leave blank when editing an existing configuration to keep the current key.
    - **Base URL** (OpenAI-compatible only) — The root URL of the endpoint, such as `http://localhost:11434` for Ollama.
    - **Enabled** — Turn off to keep the configuration on file without using it in rules or summaries.

4. Click **Add LLM** to save.

The new LLM appears in the table with its name, provider, model, and status.

## Edit or remove a local LLM

- **Edit** — Click a row in the Local LLM Configurations table to open the edit dialog. Update any field and click **Save Changes**.
- **Delete** — Click **Delete** on the row you want to remove.

Disabled LLMs remain listed but are not offered when creating rules or choosing a summary model.

## Use a local LLM in an AI Evaluation rule

After you add at least one enabled local LLM, the **AI Evaluation** action becomes available on the Rules page even if ValidMind is not connected.

1. In Atryum, click **Rules** in the left sidebar.

2. Click **New Rule**.

3. Under **Action**, select **AI Evaluation**.

4. Under **Evaluation Model**, open the dropdown and choose a model under **Local LLMs**.

5. Under **Agents**, **Servers / Sources**, and **Tools**, set the scope for the rule. See **[Invocations & rules](invocations-rules.md)** for details.

6. Make sure that **Enabled** is checked, then click **Create Rule**.

When a matching tool call arrives, Atryum sends the call and the agent's constitution to your local LLM. The model returns a verdict — approve, deny, escalate to a human, or defer to the next rule.

If the agent has no constitution configured, Atryum denies the tool call.

## Example: Ollama on localhost

| Field | Value |
| --- | --- |
| Name | `Ollama Llama 3` |
| Provider | OpenAI-compatible |
| Model | `llama3` |
| API Key | *(leave blank if Ollama does not require one)* |
| Base URL | `http://localhost:11434` |

## Related topics

- **[Invocation Summary Model](invocation-summary-model.md)** — Use a local LLM to generate plain-language summaries on the Invocations page.
- **[Connect ValidMind](connect-validmind.md)** — Optionally connect ValidMind to sync agent records and use ValidMind model configurations instead of local LLMs.
- **[Invocations & rules](invocations-rules.md)** — Create and manage approval rules, including AI Evaluation.
