# Atryum invocations

Invocations are durable audit records for every tool call that passes through Atryum.

## Invocations

The **Invocations** page is where you review those calls, approve or deny requests that need human input, and turn one-off decisions into reusable rules Invocations are displayed in a table:

- **<span style="font-variant: small-caps;">agent</span>** — The client that made the tool call.
- **<span style="font-variant: small-caps;">id</span>** — The unique invocation identifier. Hover to see the full ID.
- **<span style="font-variant: small-caps;">server</span>** — The server or agent source the tool call targeted.
- **<span style="font-variant: small-caps;">tool</span>** — The tool name the agent requested to run.
- **<span style="font-variant: small-caps;">agent record</span>** — The ValidMind agent record linked to this invocation, when Atryum is synced to ValidMind. ([Connect ValidMind](connect-validmind.md))
- **<span style="font-variant: small-caps;">status</span>** — The current state of the invocation: `Pending Approval`, `Approved`, `Denied`, `Executing`, `Succeeded`, `Failed`, `Expired`, `Cancelled`, or `Received`.
- **<span style="font-variant: small-caps;">decided by</span>** — Who or what made the approval decision: a <span style="font-variant: small-caps;">human</span>, a matching <span style="font-variant: small-caps;">rule</span>, or <span style="font-variant: small-caps;">ai evaluation</span>.
- **<span style="font-variant: small-caps;">submitted</span>** — When the agent submitted the tool call.

### View or filter invocations

#### View invocation details

1. In Atryum, click **Invocations** in the left sidebar.

2. To view an invocation's details, click on it.

The detail panel shows:

- Server and tool name, submission time, and full invocation ID.
- Current status badge and who or what made the decision. AI-evaluated invocations may also show a confidence score.
- The rule ID that handled the invocation, when a rule matched.
- Agent type and version (for example, `opencode 1.14.31`).
- A plain-language summary of the call — Click **Summarize** or **Re-summarize** to generate or regenerate one. Requires invocation summarization to be set up.*
- Tool input as a key/value arguments table — Click **Show raw JSON** for the full payload. File-edit tools may show a diff instead.
- If a invocation is `Pending Approval`, options to approve or deny the call. ([Approve or deny invocations](#approve-or-deny-invocations))
- Option to create a rule from this invocation's logic. ([Rules](rules.md#create-rules-from-existing-invocations))
- Tool output as JSON result when the call succeeds, or error details as JSON result when the call fails or is denied.
- Chronological event log. Expand a row to see event payload details.

#### Filter invocation list

1. In Atryum, click **Invocations** in the left sidebar.

2. To narrow down the list of invocations, click **Filter**.

3. Select your filters:
    - **Server** — Server or agent sources the tool call targeted.
    - **Tool** — Tool names the agent requested to run.
    - **Agent** — Clients that made the tool call.
    - **Status** — State of the invocation: `Pending Approval`, `Approved`, `Denied`, `Executing`, `Succeeded`, `Failed`, `Expired`, `Cancelled`, or `Received`.

4. Click **Apply Filter** to apply your selections.

### Approve or deny invocations

Human approvals have a default timeout of 30 seconds. To adjust the timeout, update your local Atryum config file (`atryum.toml`), then restart Atryum. For example:

```toml
[defaults]
request_timeout_seconds = 120
```

#### Approve invocations

1. In Atryum, click **Invocations** in the left sidebar.

2. Click on the invocation you want to approve.

3. On the invocations detail panel and select **Deny** under Approval Required.

4. (Optional) Enter a reason for denial — this reason is returned to the agent so you can steer what it does next.

5. Click **Deny** to confirm the denial.

#### Deny invocations

1. In Atryum, click **Invocations** in the left sidebar.

2. Click on the invocation you want to deny.

3. On the invocations detail panel and select **Deny** under Approval Required.

4. (Optional) Enter a reason for denial — this reason is returned to the agent so you can steer what it does next.

5. Click **Deny** to confirm the denial.

