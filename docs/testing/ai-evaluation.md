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

You need three sections populated: `[[auth]]`, `[agent_sync]`, and `[ai_evaluation]`.

```toml
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

# Fetches active Inventory Model records from the backend at startup.
[agent_sync]
org_cuid               = "<your-org-cuid>"          # e.g. cl1gr2aiu000309ih76g9cvix
agent_record_type_slug = "ai-agents"                # primary record type slug in VM

# Tells Atryum which custom field on the Inventory Model holds the constitution.
[ai_evaluation]
constitution_field_key = "constitution"
```
---

## Step 2 — Restart Atryum

Stop and restart the Atryum server. On startup it will:

1. Verify the backend connection using your machine-user credentials.
2. Fetch all active Inventory Model records for the configured org and record type slug.
3. Log each fetched agent: `agent cuid=… name="…" description="…"`.

Confirm the agents appear in the startup log before proceeding.

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

Atryum will now recognise bearer tokens issued for that user as belonging to this agent, enabling constitution lookup and org cross-validation when an AI Evaluation rule fires.

---

## Step 5 — Create an AI Evaluation Approval Rule

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

---

## Step 6 — Trigger an Invocation and Observe the Evaluation

Send a tool call through Atryum using a bearer token issued for the agent identity created in Step 3. The flow is:

1. Atryum receives the invocation and matches it to the AI Evaluation rule.
2. It resolves the agent's Inventory Model CUID and org CUID from the stored record.
3. It calls `POST /internal/v1/atryum/evaluate` on the ValidMind backend with the agent details, constitution field key, and tool call context.
4. The backend fetches the constitution from the Inventory Model's custom fields, builds a prompt, and asks the configured LLM to approve or deny.
5. Atryum receives `{ approved, reason }` and either auto-approves (executes the tool) or auto-denies (returns an error to the caller).

In the Atryum UI, open **Invocations** to see the outcome and the disposition reason logged against the invocation.

---

## Troubleshooting

| Symptom | Likely cause |
|---|---|
| No agents in startup log | `org_cuid` or `agent_record_type_slug` is wrong, or backend credentials are incorrect |
| Rule never matches | Agent IDs field is empty, the user's `sub` doesn't match `agent_id_claim`, or the bearer token is not being sent by the agent |
| Evaluation falls back to `human_approval` | `constitution_field_key` doesn't match the custom field name in VM, or the LLM call failed (check logs) |
| 403 from `/evaluate` | `org_cuid` in the request doesn't match the agent's organization in ValidMind |
