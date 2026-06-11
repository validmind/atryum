# Invocation Summary Model

Choose the LLM Atryum uses to generate plain-language summaries for tool invocations on the **Invocations** page.

## Use cases

- **Faster triage** — Read a one- to three-sentence summary instead of parsing raw tool arguments and results.
- **Shared context for reviewers** — Give teammates a quick, consistent description of what an agent tried to do and what happened.
- **On-demand explanation** — Generate or refresh a summary for any invocation from the detail panel.
- **ValidMind or local models** — Use a ValidMind model configuration when connected, or a **Local LLM** you configured in Settings when you are not.

Summaries describe what was requested, what action was taken, and the outcome. They are stored on the invocation record so you can return to them later without calling the model again.

## Prerequisites

You need at least one model source:

- A **ValidMind** connection with at least one model configuration available in your organization, or
- At least one enabled **Local LLM** on the Settings page.

If neither is available, the Invocation Summary Model section shows *No models available. Connect to ValidMind or add a local LLM above.*

To add a local provider, see **[Local LLM configurations](llm-configurations.md)**.

## Set the Invocation Summary Model

1. In your browser, navigate to [`localhost:8080/ui/settings`](http://localhost:8080/ui/settings).

2. Scroll to **Invocation Summary Model**.

3. Open the **Select a model…** dropdown and choose:

    - A model under **ValidMind Models** — Uses your connected ValidMind workspace and the selected model configuration.
    - A model under **Local LLMs** — Uses the local LLM configuration you added in Settings.

4. Click **Save**.

The selection is stored in Atryum and used whenever you request a summary from the Invocations page.

## Generate a summary on the Invocations page

1. In Atryum, click **Invocations** in the left sidebar.

2. Click the invocation you want to summarize.

3. In the detail panel, find the **Summary** section.

4. Click **Summarize** to generate a summary, or **Re-summarize** to replace an existing one.

If no Invocation Summary Model is configured, the button is disabled and the panel prompts you to set one in Settings.

When summarization succeeds, the plain-language text appears in the Summary section and is saved on the invocation.

## ValidMind vs. local LLM

| | ValidMind model | Local LLM |
| --- | --- | --- |
| Requires ValidMind connection | Yes | No |
| Model list source | ValidMind model configurations | Local LLM Configurations on Settings |
| API credentials | ValidMind backend | Provider key or endpoint configured per local LLM |
| Best for | Teams already using ValidMind model governance | Standalone, on-prem, or bring-your-own-key setups |

You can switch between a ValidMind model and a local LLM at any time by choosing a different option on Settings and clicking **Save**.

## Related topics

- **[Local LLM configurations](llm-configurations.md)** — Add OpenAI, Anthropic, or OpenAI-compatible providers for native LLM features.
- **[Invocations & rules](invocations-rules.md)** — Review, approve, deny, and create rules from tool invocations.
- **[Connect ValidMind](connect-validmind.md)** — Connect Atryum to ValidMind for agent sync and ValidMind model configurations.
