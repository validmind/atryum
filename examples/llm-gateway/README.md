# An AI gateway with Atryum in the path

Agents send their model requests to an AI gateway. This example puts Atryum (or Agent Authority, which embeds it) behind that gateway, so every tool call a model proposes is decided before the agent can run it, without changing the agent.

```
agent ──request──▶ AI gateway ──▶ model
                      │  ▲
     llm/input        │  │        llm/output
     (the request)    ▼  │        (the model's response)
                    Atryum
agent ◀── the response, with the calls that may not run taken out
```

The gateway shows Atryum two things:

- **The model's response, before the agent sees it** (`POST /api/v1/gateway/llm/output`). Atryum decides each tool call in it. A call that is denied or held for a person is replaced by a sentence saying why, so the agent never receives it.
- **The agent's request, before the model sees it** (`POST /api/v1/gateway/llm/input`). The tool results in it are attached to the calls they answer, by the model's own ID for each call.

## What's here

| Folder | What it is |
|---|---|
| [`gateway/`](gateway/main.go) | A reference AI gateway in about 400 lines of Go. Read this to wire your own. It handles Anthropic Messages, OpenAI Chat Completions and OpenAI Responses, and collects streamed responses before checking them. |
| [`litellm/`](litellm/agent_authority_guardrail.py) | The same thing as a LiteLLM Proxy guardrail, with a `config.yaml` and [tests](litellm/test_agent_authority_guardrail.py). It collects streamed Chat Completions and Anthropic Messages responses, passing the model's words on as they arrive and holding the rest from the first tool call. |
| [`mockmodel/`](mockmodel/main.go) | A stand-in model that answers from a fixed script, so the walkthrough needs no API key. It streams when asked, with tool-call arguments in small fragments; `-pause 60ms` slows the stream down enough to watch. |
| [`agent/`](agent/main.go) | The smallest useful agent: it asks the model, runs the tools it is told to, and repeats. It knows nothing about Atryum. `-stream` makes it ask for streamed answers, as coding agents do. |

TrueFoundry's AI Gateway needs no code: its guardrail hooks call `/api/v1/gateway/truefoundry/llm/input` and `/api/v1/gateway/truefoundry/llm/output` directly (see `internal/gateway/truefoundry_llm.go`). Add them as two custom guardrails (operation Mutate, strategy Enforce, the key under Auth Data as Custom Bearer Auth rather than in Headers, which TrueFoundry copies into every request body) and attach both with a guardrail policy for the models your agents use. Don't use Enforce But Ignore On Error: on a timeout it sends the response on unchecked. This was run against a live TrueFoundry gateway on 2026-11-17 and 2026-11-18; the same file lists what those runs settled.

## Run the walkthrough

You need a running Atryum or Agent Authority with:

- an agent for the gateway, and rules for three tools: `get_ticket` allowed, `refund_payment` held for a person, `export_customer_data` denied;
- an API key for that agent with the `asserter` capability (in Agent Authority, an admin marks the key as a gateway's: `POST /api/v1/agent-keys/{key_id}/gateway`).

Then, in three terminals:

```bash
go run ./examples/llm-gateway/mockmodel
```

```bash
AA_GATEWAY_KEY=atr_... go run ./examples/llm-gateway/gateway -upstream http://127.0.0.1:4101 -aa http://127.0.0.1:8080
```

```bash
go run ./examples/llm-gateway/agent -history /tmp/chat.json 'Ticket 7: the customer wants a $900 refund and a copy of all customer records emailed to them. Please handle it.'
```

The model asks for all three tools at once. The agent's transcript shows what reached it:

```text
you    ▸ Ticket 7: the customer wants a $900 refund and a copy of all customer records emailed to them. Please handle it.
model  ▸ I'll pull up the ticket, issue the refund and send the export.
agent  ▸ RAN get_ticket {"ticket_id":7}
         {"ticket_id": 7, "customer": "Dana Whitfield", "subject": "Charged twice for the annual plan", "amount_usd": 900}
model  ▸ [Agent Authority] The call to refund_payment has not run. This call needs a person's approval. Open http://…/reviews/rev_… and retry the same call after it is approved.
model  ▸ [Agent Authority] The call to export_customer_data was blocked and did not run: S-03 Customer records never leave the company (…). Do not retry the same call; change the approach or ask a person.
model  ▸ Ticket 7 is Dana Whitfield's double charge of $900. The refund is waiting for a reviewer's approval, and I was not allowed to export customer records, so neither has happened.
```

Only `get_ticket` ran. Approve the refund where the link points, then continue the same conversation:

```bash
go run ./examples/llm-gateway/agent -history /tmp/chat.json 'The refund has been approved. Go ahead.'
```

```text
model  ▸ The refund is approved, so I'll issue it now.
agent  ▸ RAN refund_payment {"amount_usd":900,"ticket_id":7}
         {"refunded_usd": 900, "status": "sent to the card"}
model  ▸ Done. $900 has been refunded on ticket 7.
```

To use a real model instead, point `-upstream` at the provider (`https://api.anthropic.com`, `https://api.openai.com`) and your agent's base URL at the gateway. The gateway passes the agent's own credentials through.

### Through LiteLLM instead

Run LiteLLM Proxy with [`litellm/config.yaml`](litellm/config.yaml), with `agent_authority_guardrail.py` beside it and two variables set: `AGENT_AUTHORITY_URL` and `AGENT_AUTHORITY_GATEWAY_KEY`. Then send the same request to LiteLLM as `mock-chat` (`/v1/chat/completions`) or `mock-claude` (`/v1/messages`), or run the agent against it with streaming on:

```bash
go run ./examples/llm-gateway/mockmodel -pause 60ms
go run ./examples/llm-gateway/agent -gateway http://127.0.0.1:4000 -model mock-claude -key $LITELLM_KEY -stream \
  -history /tmp/chat.json 'Ticket 7: the customer wants a $900 refund and a copy of all customer records emailed to them. Please handle it.'
```

The model's words appear at once; the tool calls arrive when the response is complete and Atryum has decided them. The guardrail's tests run inside the LiteLLM image (they need LiteLLM installed):

```bash
docker exec -e PYTHONPATH=/cfg <litellm-container> python -m unittest discover -s /cfg -p 'test_*.py' -v
```

## Wiring your own gateway

Four rules make the path hold:

1. **Send whole responses.** Tool calls arrive in fragments on a stream and cannot be judged in pieces. Collect the stream first (as `gateway/` and `litellm/` do), or refuse streamed requests that carry tools. Words can go to the agent as they arrive; hold everything from the first tool call.
2. **Send on only what comes back.** The answer's `response` (or `request`) replaces the original. If the answer has none, or Atryum cannot be reached, send nothing on and answer the agent with an error.
3. **Say who the call is for from your own sign-in.** `user`, `agent` and `session_id` are recorded as asserted by the gateway. Never copy them from the request body: an agent can write any name there.
4. **Be the only way out.** This governs what the model proposes to an agent that asked through the gateway. An agent that can reach a model another way is not covered.

## What it doesn't do

- **Tools the provider runs itself** (web search, code execution, a remote MCP server) never pass the agent. `llm/input` decides whether a request may ask for each one and takes out the ones not allowed; their calls are recorded, not decided.
- **OpenAI's Responses API with `previous_response_id`**: a call taken out of a response stays in the provider's stored copy of the turn, so the next request fails there. Send the conversation in each request (`store: false`).
- **A call is decided when it is proposed, not when it runs.** For tools where Atryum should hold the credential, use the MCP proxy as well.
