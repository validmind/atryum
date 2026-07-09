# Atryum + Google Agent Gateway (Gemini Enterprise Agent Platform)

> **Verified end to end** (see [RESULTS.md](RESULTS.md), Terraform in
> [terraform/](terraform/)). The callout was run locally against real Envoy
> `ext_authz` requests and deployed to real GCP (Cloud Run + serverless NEG +
> backend service + a registered authz-extension), gating `allow`/`deny`/fail-closed
> in both. **Verified facts:** the authz-extension callout **timeout is capped at
> 10s** (so auto-rules and a fast LLM-as-judge run inline; longer human approval uses
> deny-and-resubmit), and it **fails closed**. The one unverified hop is binding the
> `authz-policy` to a live managed Agent Gateway, which did not provision in a fresh
> trial project (Google-side internal error — a platform onboarding limitation).

Google's **Gemini Enterprise Agent Platform (GEAP)** runs agents (built in Agent
Studio no-code, ADK, or pulled from the marketplace) and routes their traffic
through **Agent Gateway** — "a central policy enforcement point to govern all
agent tool calls." Agent Gateway can
[delegate authorization](https://docs.cloud.google.com/gemini-enterprise-agent-platform/govern/gateways/delegate-authorization)
to a custom external engine via a Service Extensions **authorization extension**:
a gRPC Envoy `ext_authz` callout to a service *you* run.

Atryum runs that callout. Bound once to the gateway resource, it gates **every**
tool call from **every** routed agent through the normal Atryum rules
(auto-approve / auto-deny / LLM-as-judge / human approval) — **no per-agent
code**. This is the same slot Google's ISV partners (Okta, Palo Alto, Zscaler, …)
occupy; Atryum does not need partner status — the authz extension is a
customer-configurable primitive.

```
 ┌──────────────────────────┐  gRPC ext_authz Check()   ┌────────────────────────┐
 │ any agent (Studio/ADK/…) │        (per tool call)     │ Atryum ext_authz       │
 │        │ tool call        │─────────────────────────▶ │  callout (this)        │
 │        ▼                   │                            │  • Submit → rules      │
 │ ┌──────────────────────┐  │◀───── allow / deny ────────│  • LLM-judge / HITL    │
 │ │   Agent Gateway      │  │                            │  • fail-closed         │
 │ └──────────────────────┘  │──▶ tool proceeds / blocked └────────────────────────┘
 └──────────────────────────┘
```

## How it works

1. Agent Gateway calls Atryum's gRPC `ext_authz` `Check()` for each tool call.
2. Atryum extracts the tool name, args, agent identity, and MCP server from the
   request (context extensions / CEL attributes, headers, and — for a
   `CONTENT_AUTHZ` policy — the MCP request body) and **submits it as an
   invocation** through the same external-hook path the Claude Code hook uses.
3. The approval engine decides (auto rule, LLM-as-judge, or a parked human
   approval). Atryum blocks up to `decision_timeout_seconds`.
4. Atryum returns **OK** (the gateway forwards the tool call) or
   **PERMISSION_DENIED** (403 + reason). It **fails closed** on any error or
   timeout. The call and decision appear live in the invocations UI.

Human approvals can't be *awaited* synchronously (the gateway callout has a short
timeout). A pending approval returns a deny with "retry to resume"; once a human
approves in Atryum, the agent's retry (same idempotency key) resolves to allow.

## 1. Enable the callout in `atryum.toml`

```toml
[[google_gateway]]
listen_addr = ":8090"        # gRPC address Agent Gateway's authz extension dials
# Optional (defaults shown):
# source                  = "google-agent-gateway"  # rule "server" key for non-MCP tools
# poll_interval_millis    = 200
# decision_timeout_seconds= 30    # keep <= the gateway authz-extension timeout
# client_name             = "google-agent-gateway"
# client_version          = ""
```

Env override (single-callout convenience): `ATRYUM_GOOGLE_GATEWAY_LISTEN_ADDR=:8090`.
Entries without a `listen_addr` are skipped.

Build/run needs two modules Atryum doesn't otherwise use:

```bash
go get google.golang.org/grpc \
       github.com/envoyproxy/go-control-plane/envoy/service/auth/v3 \
       github.com/envoyproxy/go-control-plane/envoy/type/v3
go mod tidy
go build ./...
```

## 2. Expose Atryum to the gateway

Run Atryum where Agent Gateway can reach the gRPC port as a Service Extensions
callout backend — a **Cloud Run** service (serverless NEG) in the project, or an
off-Google Atryum reached via a **hybrid-connectivity** or **Private Service
Connect** NEG (not a raw public URL).

## 3. Register the authorization extension

```bash
gcloud service-extensions authz-extensions import atryum-authz \
  --source=atryum-authz.yaml --location=LOCATION
```

```yaml
# atryum-authz.yaml
name: atryum-authz
loadBalancingScheme: INTERNAL_MANAGED
authority: atryum.internal
service: projects/PROJECT/regions/LOCATION/backendServices/atryum-callout  # your NEG-backed service
wireFormat: EXT_AUTHZ_GRPC
failOpen: false        # fail CLOSED — deny on timeout/error
timeout: 1s            # <= your decision_timeout_seconds budget
```

## 4. Bind it to the Agent Gateway resource

```bash
gcloud network-security authz-policies import atryum-policy \
  --source=atryum-policy.yaml --location=LOCATION
```

```yaml
# atryum-policy.yaml — governs ALL traffic through the gateway, not per-agent
name: atryum-policy
target:
  resources:
    - projects/PROJECT/locations/LOCATION/agentGateways/AGENT_GATEWAY_NAME
action: CUSTOM
customProvider:
  authzExtension:
    resources:
      - projects/PROJECT/locations/LOCATION/authzExtensions/atryum-authz
# policyProfile: REQUEST_AUTHZ  (headers/attrs)  or  CONTENT_AUTHZ  (MCP bodies)
```

Roll out with `iamEnforcementMode: DRY_RUN` (audit-only — decisions recorded in
Atryum, nothing blocked) first, then flip to `ENFORCE`.

## 5. Make it org-wide (mandate routing)

Agent Gateway is project + region scoped, so repeat 3–4 per project/region. To
force every agent through a sanctioned gateway (and block bypass), set the org
policy and VPC Service Controls:

```bash
gcloud org-policies set-policy allowlist-egress-gateways.yaml   # org / folder / project
# custom.allowlistedEgressAgentGatewaysForAgentEngine + VPC-SC
```

Managed runtimes (Agent Runtime, Gemini Enterprise / Agent Studio no-code)
auto-route through Agent Gateway; **self-hosted ADK agents must be forced** onto
it via the org policy above, or they escape gating.

## Approval rules

Rules match `(server, tool, agent)` exactly as for any other invocation:

- **Tool name** comes from the gateway's `mcp.toolName` attribute (or the MCP
  request body).
- **Server** is the MCP server name when present, else the configured `source`
  (default `google-agent-gateway`).
- **Agent** is the authenticated agent identity the gateway forwards.

With the default `manual_approval` policy, anything without a matching rule parks
for human approval — but see the HITL note above: on the gateway that surfaces as
a deny-and-resubmit, not a synchronous park.

## Notes & limits

- **One authorization extension per forwarding rule** (max 4 per Agent-to-Anywhere
  gateway) — Atryum owns the slot and must subsume/chain the default IAP+IAM
  check; it can't coexist with another vendor's authz-extension on the same rule.
- **HITL is engineered, not native** — bounded by the callout timeout
  (deny-and-resubmit). Google's native gateway HITL (Unified Access Policy) is
  "coming soon."
- **`ext_authz` wireFormat is in Preview.**
- Only agents/tools **registered in Agent Registry** and actually routed through
  the gateway are governed.

## Open questions (verify against your GEAP project)

- Does the callout receive the **full tool-call args** (via `CONTENT_AUTHZ` /
  request body) for arbitrary tools, or only the IAP-exposed CEL attributes? This
  determines how rich LLM-as-judge and rule matching can be. `parse.go` reads
  defensively from context extensions, headers, and body — confirm which your
  gateway populates and tighten it.
- Can Atryum's extension **preserve** the default IAP+IAM check, or does adopting
  a custom authz-extension replace it?
- Exact max callout **timeout** — sets the ceiling for a synchronous LLM-judge.
