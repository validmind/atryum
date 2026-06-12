# Invocations

Invocations are durable audit records for every tool call that passes through Atryum.

## What are invocations?

The **Invocations** page is where you review those calls, approve or deny requests that need human input, and turn one-off decisions into reusable rules ([Rules](3_rules.md)). Invocations are displayed in a table:

- **<span style="font-variant: small-caps;">agent</span>** — The client that made the tool call.
- **<span style="font-variant: small-caps;">id</span>** — The unique invocation identifier. Hover to view the full ID.
- **<span style="font-variant: small-caps;">server</span>** — The server or agent source the tool call targeted.
- **<span style="font-variant: small-caps;">tool</span>** — The tool name the agent requested to run.
- **<span style="font-variant: small-caps;">agent record</span>** — The agent record linked to this invocation — a local record manually registered or a record synced from ValidMind. Populated when the call's agent identity matches an **Agent IDs** entry on that record; empty otherwise. ([Connect agents](1_integrations/2_connect-agents.md#tag-invocations-to-agent-records), [Connect ValidMind](1_integrations/1_connect-validmind.md))
- **<span style="font-variant: small-caps;">status</span>** — The current state of the invocation: `Pending Approval`, `Approved`, `Denied`, `Executing`, `Succeeded`, `Failed`, `Expired`, `Cancelled`, or `Received`.
- **<span style="font-variant: small-caps;">decided by</span>** — Who or what made the approval decision: a <span style="font-variant: small-caps;">human</span>, a matching <span style="font-variant: small-caps;">rule</span>, or <span style="font-variant: small-caps;">ai evaluation</span>.
- **<span style="font-variant: small-caps;">submitted</span>** — When the agent submitted the tool call.

## View or filter invocations

### View invocation details

1. In Atryum, click **Invocations** in the left sidebar.

2. To view an invocation's details, click on it.

The detail panel shows:

- Server and tool name, submission time, and full invocation ID.
- Current status badge and who or what made the decision. AI-evaluated invocations may also show a confidence score.
- The rule ID that handled the invocation, when a rule matched.
- Agent type and version (for example, `opencode 1.14.31`).
- A plain-language summary of the call when invocation summarization is configured. ([Use invocation summarization](#use-invocation-summarization))
- Tool input as a key/value arguments table — Click **Show raw JSON** for the full payload. File-edit tools may show a diff instead.
- If an invocation is `Pending Approval`, options to approve or deny the call. ([Approve or deny invocations](#approve-or-deny-invocations))
- Option to create a rule from this invocation's logic. ([Rules](3_rules.md#create-rules-from-existing-invocations))
- Tool output as JSON result when the call succeeds, or error details as JSON result when the call fails or is denied.
- Chronological event log. Expand a row to view event payload details.

### Filter invocation list

1. In Atryum, click **Invocations** in the left sidebar.

2. To narrow down the list of invocations, click **Filter**.

3. Select your filters:
    - **Server** — Server or agent sources the tool call targeted.
    - **Tool** — Tool names the agent requested to run.
    - **Agent** — Clients that made the tool call.
    - **Status** — State of the invocation: `Pending Approval`, `Approved`, `Denied`, `Executing`, `Succeeded`, `Failed`, `Expired`, `Cancelled`, or `Received`.

4. Click **Apply Filter** to apply your selections.

## Approve or deny invocations

:::
Human approvals have a default timeout of 30 seconds. To adjust the timeout, update your local Atryum config file (`atryum.toml`), then restart Atryum. For example:

```toml
[defaults]
request_timeout_seconds = 120
```
:::

### Approve invocations

1. In Atryum, click **Invocations** in the left sidebar.

2. Click on the invocation you want to approve:

    - On the invocation detail panel, select **Approve** under Approval Required to approve that single invocation.
    - To approve all invocations for calls matching similar criteria going forward:

        a. Click **&#9660; Customize rule scope** to edit the criteria the rule should match against. ([Rules](3_rules.md#create-rules-from-existing-invocations))

        b. Click **&#8744;** next to Approve and select **Always approve**.

### Deny invocations

1. In Atryum, click **Invocations** in the left sidebar.

2. Click on the invocation you want to deny:

    - On the invocation detail panel, select **Deny** under Approval Required to deny that single invocation.
    - To deny all invocations for calls matching similar criteria going forward:

        a. Click **&#9660; Customize rule scope** to edit the criteria the rule should match against. ([Rules](3_rules.md#create-rules-from-existing-invocations))

        b. Click **&#8744;** next to Deny and select **Always deny**.

3. (Optional) Enter a reason for denial — this reason is returned to the agent so you can steer what it does next.

## Use invocation summarization

Generate plain-language summaries for tool invocations so reviewers can triage calls quickly without parsing raw tool arguments and results. Summaries describe what was requested, what action was taken, and the outcome, and are stored on the invocation record so you can return to them later without calling the model again.

To use invocation summarization, you need at least one model source:

- A **ValidMind** connection with at least one model configuration available in your organization ([Connect ValidMind](1_integrations/1_connect-validmind.md))
- Or, at least one enabled **Local LLM Configuration** ([Configure LLM providers](1_integrations/4_configure-llm-providers.md))

### Set invocation summary model

1. In Atryum, click **Settings** in the left sidebar.

2. Under Invocation Summary Model, **Select a model ...** from the drop-down menu.

3. Click **Save** to use that model.

### Generate invocation summaries

1. In Atryum, click **Invocations** in the left sidebar.

2. Click the invocation you want to summarize.

3. Under <span style="font-variant: small-caps;">summary</span>, click **Summarize** to generate a summary, or **Re-summarize** to replace an existing one.







