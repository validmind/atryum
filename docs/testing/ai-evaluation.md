# Manual Testing: AI Evaluation (LLM-as-Judge)

This guide walks through manually testing the AI Evaluation rule type end-to-end using the Atryum UI and a live ValidMind backend.

---

## Prerequisites

- Atryum running locally (or accessible)
- A ValidMind backend with at least one org, an `ai-agents` (or equivalent) record type, and one or more Inventory Model records representing agents
- An identity provider (e.g. Auth0) configured as your `[[auth]]` issuer
- Machine-user credentials for the ValidMind backend

---

## Step 1 — Configure `atryum.toml`

`atryum.toml` only needs two sections for AI Evaluation: `[backend]` (ValidMind credentials) and `[[auth]]` (JWT validation). Agent sync and charter settings have moved to the UI — see **Step 1b** below.

```toml
# ValidMind backend connection (machine-user credentials).
[backend]
base_url       = "https://api.your-validmind-instance.ai"
machine_key    = "<your-machine-key>"
machine_secret = "<your-machine-secret>"
connection_timeout_seconds = 5
evaluate_timeout_seconds   = 120   # allow enough time for LLM evaluation

# Validates bearer tokens issued by your IDP.
# Add one [[auth]] block per issuer you want to accept.
[[auth]]
enabled        = true
issuer         = "https://your-tenant.us.auth0.com/"
audience       = "https://api.your-validmind-instance.ai"
# jwks_url is optional — omit to use OIDC discovery
# jwks_url     = "https://your-tenant.us.auth0.com/.well-known/jwks.json"
required_scope = ""
agent_id_claim = "sub"   # claim that carries the agent's unique identity
```

---

## Step 1b — Configure Agent Sync in the UI

The organization, record type, and charter field are now configured in the Atryum UI under **Settings → Agent Record Sync**. This replaces the old `[agent_sync]` and `[ai_evaluation]` TOML sections.

1. Open the Atryum UI and navigate to **Settings**.
2. Under **Agent Record Sync**, fill in the three fields:

   | Field | What to enter |
   |---|---|
   | **Organization** | Select the ValidMind organization whose agent records should be synced |
   | **Record Type** | Select the primary record type that identifies agent inventory models (e.g. `ai-agents`) |
   | **Charter Field** | Select the custom field key on inventory models that stores the charter text (e.g. `charter`) |

   The dropdowns are cascading — selecting an organization loads its record types; selecting a record type loads the custom fields available on that type.

3. Click **Save Settings**. Atryum will immediately sync agent records from the selected org and record type and confirm with a toast notification.

> **Note:** Changing the organization or record type deletes and re-syncs all agent records. Atryum will prompt you to confirm before proceeding.

---

## Step 2 — Restart Atryum

Stop and restart the Atryum server so it picks up the `atryum.toml` changes. On startup it will:

1. Verify the backend connection using your machine-user credentials.
2. Fetch all active Inventory Model records for the org and record type configured in Settings (Step 1b).
3. Log each fetched agent: `agent cuid=… name="…" description="…"`.

Confirm the agents appear in the startup log before proceeding. Agent sync settings saved in the UI persist across restarts — you do not need to re-enter them.

---

## Step 3 — Create a User Identity in the IDP

In your identity provider (e.g. Auth0):

1. Create a new **user** (e.g. via **User Management → Users → Create User**).
2. Note the user's **ID** (the `sub` claim in the issued JWT) — this is the value Atryum will receive in the `agent_id_claim` field (`sub` by default).

---

## Step 4 — Link the User to an Agent in Atryum

1. Open the Atryum UI and navigate to **Agents**.
2. Find the agent record that corresponds to the Inventory Model you want to test.
3. Click the agent to open it, then paste the **user ID** (`sub` value) from Step 3 into the **Agent IDs** field.
4. Save.

Atryum will now recognise bearer tokens issued for that user as belonging to this agent, enabling charter lookup and org cross-validation when an AI Evaluation rule fires.

---

## Step 5 — Author the Agent Charter

The charter is stored as a custom field (key = `charter_field_key`) on the agent's Inventory Model in ValidMind. It is free-form Markdown, but the LLM-as-judge recognises the following sections to decide its verdict:

### Permission tiers (tool-level)

| Tier | Verdict | When to use |
|------|---------|-------------|
| **Auto** | `approved` | Low-risk, read-only actions the agent may perform silently |
| **Approve** | `human_approval` | Risky or sensitive actions that require a human sign-off |
| **Never** | `denied` | Prohibited actions — hard block, no exceptions |

### `### Human approval required when`

List conditions under which the LLM should route the invocation to the human approval queue instead of deciding automatically. The judge returns `human_approval` when any condition is met.

```markdown
### Human approval required when
- The SQL query modifies data (INSERT, UPDATE, DELETE, DROP, …)
- The request targets a table not explicitly listed in the charter
- The query returns more than 10,000 rows
```

### `### Defer to next rule when`

List conditions under which the LLM should pass evaluation to the next matching approval rule rather than making a final decision. The judge returns `next_rule` when any condition is met. Use sparingly — prefer explicit tiers above.

```markdown
### Defer to next rule when
- The tool is not a database tool (let downstream rules handle it)
```

### Charter chain and precedence

If the agent's Inventory Model has upstream dependencies, Atryum collects charters from the entire chain (most-upstream ancestor first, this agent last) and presents them all to the LLM. The judge applies these precedence rules:

1. **Downstream wins**: later charters in the chain are more specific and override earlier ones when they conflict.
2. **Explicit delegation is honoured**: if an upstream says "downstream charters may override this", a downstream grant (even conditional) replaces the upstream rule.
3. **Most specific match first**: apply the innermost rule that covers the action; fall back to upstream only when no downstream charter addresses it.
4. **Grants override blanket denials**: a downstream "allow with human approval" beats an upstream "deny all".

**Example:**

```
[Upstream agent]
Deny all Postgres queries. Downstream charters may override this.

---

[This agent]
### Human approval required when
- The query is a read-only SELECT on the inventory_models table
```

Result: `human_approval` (downstream grant overrides upstream blanket denial).

---

## Step 6 — Create an AI Evaluation Approval Rule

1. In the Atryum UI, go to **Rules** and click **New Rule**.
2. Fill in the rule:

   | Field | Value |
   |---|---|
   | **Action** | `AI Evaluation` |
   | **Agent** | Select the agent you linked in Step 4 |
   | **Model Configuration** | Select the LLM model config to use for evaluation |
   | **Server** (optional) | Restrict to a specific MCP server, or leave blank for all |
   | **Tool** (optional) | Restrict to a specific tool name, or leave blank for all |

3. Save the rule.

### Chaining rules with `next_rule`

You can stack multiple AI Evaluation rules in priority order. When the LLM returns `next_rule`, Atryum advances to the next matching rule rather than making a final decision. If all matching rules defer, the invocation falls back to human approval.

---

## Step 7 — Trigger an Invocation and Observe the Evaluation

Send a tool call through Atryum using a bearer token issued for the agent identity created in Step 3. The flow is:

1. Atryum receives the invocation and matches it to the AI Evaluation rule.
2. It resolves the agent's Inventory Model CUID and org CUID from the stored record.
3. It calls `POST /api/atryum/unstable/evaluate` on the ValidMind backend with the agent details, charter field key, and tool call context.
4. The backend fetches the charter chain from the Inventory Model's custom fields, builds a prompt with precedence rules, and asks the configured LLM for a verdict.
5. Atryum receives `{ verdict, reason }` and routes the invocation accordingly:

   | Verdict | Disposition | UI outcome |
   |---------|-------------|------------|
   | `approved` | Auto-execute | `Succeeded` · **AI Evaluation** badge (purple) |
   | `denied` | Hard deny | `Denied` · **AI Evaluation** badge (purple) |
   | `human_approval` | Human queue | `Pending` · **AI Escalated** badge (yellow); after decision: **AI Escalated** + **Human** |
   | `next_rule` | Continue to next rule | *(internal — not a final state)* |

In the Atryum UI, open **Invocations** to see the outcome and the disposition reason logged against the invocation.

---

## Troubleshooting

| Symptom | Likely cause |
|---|---|
| No agents in startup log | `org_cuid` or `agent_record_type_slug` is wrong, or backend credentials are incorrect |
| Rule never matches | Agent IDs field is empty, the user's `sub` doesn't match `agent_id_claim`, or the bearer token is not being sent by the agent |
| Evaluation falls back to `human_approval` | `charter_field_key` doesn't match the custom field name in VM, or the LLM call failed (check logs) |
| 403 from `/evaluate` | `org_cuid` in the request doesn't match the agent's organization in ValidMind |
| Upstream deny overrides downstream grant | Backend not yet redeployed with the precedence-rules prompt update; check that `verdict` (not `approved`) is present in the `/evaluate` response |
| `next_rule` never advances | Rules are not stacked in priority order, or the charter does not contain a `### Defer to next rule when` section |
