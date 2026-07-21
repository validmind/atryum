# Handoff — Atryum × Google Agent Gateway (GEAP) integration

**Branch:** `john/geap-agent-gateway-spike` · **Status:** live PoC, proven end-to-end, ready for someone to take over and productionize.

This document is the full handoff. It covers what was built, what is proven, the exact GCP resources left running, every wall hit and how it was solved, the open issues + suggested fixes (from a senior code review, full text in [`REVIEW.md`](./REVIEW.md)), and the next build. Read this + `REVIEW.md` + `RESULTS.md` and you have everything.

---

## TL;DR

Atryum can be the **allow/deny decision engine for AI-agent tool calls on Google's Gemini Enterprise Agent Platform**, via two complementary planes:

1. **Network plane — Agent Gateway Service Extension.** Atryum registers as a custom `ext_authz` authorization extension on the managed Agent Gateway, bound once, governing every agent routed through it (including no-code Agent Studio and marketplace agents). **This is proven live.**
2. **In-process plane — ADK plugin.** A `before_tool_callback` / Runner `Plugin` gates each tool call in-process for code-first agents (see [`examples/adk-plugin/`](../adk-plugin/)). Deeper (sees full args) but opt-in per agent.

Both call the same Atryum external-hook decision API, so rules / LLM-judge / HITL / audit are shared.

**Proven, in Google's own audit log:** a real Gemini agent on Agent Engine made a real MCP tool call → the Agent Gateway called Atryum → Atryum denied it → the gateway blocked it:

```
resource.type=networkservices.googleapis.com/Gateway  host=legacy-dms…run.app
authzPolicyInfo.policies[1].name = …/authzPolicies/atryum-policy
authzPolicyInfo.policies[1].result = "DENIED"
authzPolicyInfo.result = "DENIED"
```

Flip the Atryum rule and the same agent is allowed and returns the document.

---

## The single most important caveat

The custom `ext_authz` (REQUEST_AUTHZ) callout receives **only `method` + `path`** from the gateway — verified by dumping the raw `CheckRequest` (empty headers, empty contextExtensions, empty body). **The MCP tool name is NOT visible at REQUEST_AUTHZ** (it lives in the JSON-RPC body). So today's enforcement is at the **tool-surface / path level** (every MCP tool call is `POST /mcp` → attributed to a synthetic tool `mcp_tool_call` → allow/deny the agent's access to the tool surface).

**Per-tool-name and per-argument gating ("allow `search_documents`, deny `get_document`") requires a `CONTENT_AUTHZ` / `ext_proc` extension that streams the body.** That adapter is spec'd but not yet built — it is the #1 next task (see [The next build](#the-next-build-ext_proc-content_authz) and `REVIEW.md` §c).

---

## What is proven live (evidence)

| Claim | Evidence |
|---|---|
| The managed Agent Gateway provisions | `agentGateways/agent-gateway` created in ~3 min via the codelab Terraform |
| A real agent routes its tool calls through it | Gateway audit logs show the agent's `POST …/mcp` traffic |
| Atryum is the gateway's decision engine | `authzPolicies/atryum-policy` (REQUEST_AUTHZ, CUSTOM) → `authzExtensions/atryum-authz` |
| The gateway actually calls Atryum | nginx access log: **91×** `POST /envoy.service.auth.v3.Authorization/Check HTTP/2 200` from gateway egress `10.20.0.2` |
| Atryum's verdict is enforced | Gateway audit: `atryum-policy: DENIED; result=DENIED`. Atryum log: `decision=deny reason="matched approval rule (auto_deny)"` |
| Allow works too | With `auto_approve`, the same agent retrieves the tax return (Julian & Elena Sterling, SSN 323-45-6789) |

Three companion write-ups (claude.ai artifacts) exist: the non-ADK-governance scoping, the live-proof page, and the implementation blueprint. Canonical scoping doc: `atryum/docs/geap-atryum-integration-scoping.md`.

---

## What's in this branch / PR

- **`internal/googlegateway/`** — dependency-free decision core: `types.go` (ToolCall, Decision, defaults), `service.go` (`Service.Decide`, reuses the existing external-hook invocation path), `parse.go` (`ExtractToolCall` from ext_authz attrs / MCP JSON-RPC body). *(committed in `aaf766b`)*
- **`internal/googlegateway/extauthz/server.go`** — the Envoy `ext_authz` v3 gRPC adapter. **Working-tree changes from the live build (this PR):** (1) pass-through allow for non-MCP requests; (2) a path-based `mcp_tool_call` fallback so `/mcp` tool egress can be gated at REQUEST_AUTHZ; (3) a TEMP raw-request dump used to prove what the gateway sends. **See the known-issues below — these three are exactly what the review flags (A3/A7/A8/B).**
- **`internal/config/config.go`** — `GoogleGatewayConfig` + `applyGoogleGatewayEnv`. *(committed)*
- **`cmd/atryum/main.go`** — wiring to start the callout per `[[google_gateway]]`. *(committed)*
- **`Dockerfile.callout`** — lean callout image build (skips UI/licenses; placeholder `web/index.html` for the `//go:embed`). Used for the live `callout:v5` image. *(new in this PR)*
- **`examples/adk-plugin/`** — the ADK in-process integration (module `atryum_adk`, example agent, README, unit test). *(new in this PR — authored by a sub-agent; sanity-checked for syntax, NOT run against a live ADK)*
- **`examples/google-agent-gateway/`** — `README.md`, `RESULTS.md` (local + earlier-cloud verification), `terraform/`, `local-demo/probe/`, plus **this `HANDOFF.md`**, **`REVIEW.md`** (senior review), and **`driver/`** (a scenario driver for the live agent).

**Heads-up for the reviewer:** the live-proven `server.go` diff is committed as-is (per handoff request) and **breaks `TestCheck_Unattributable_FailsClosed`** (the fail-closed default was intentionally inverted to pass-through during the live build). This is issue **A3** below — resolve the semantics + update the test as the first cleanup.

---

## Live GCP resources (the running demo — left up on purpose)

Project `singular-range-486318-b0`, region `us-central1`, authed as `john@validmind.ai`. Trial credit was ~$299.86/$300 remaining when last checked; the agent (2 min-instances) + gateway accrue a few $/day.

| Resource | Identifier |
|---|---|
| Agent Gateway | `agentGateways/agent-gateway` (AGENT_TO_ANYWHERE, DNS-peered to `atryumgw.net`) |
| Atryum authz extension | `authzExtensions/atryum-authz` (EXT_AUTHZ_GRPC, `callout.atryumgw.net`, 10s) |
| Atryum authz policy | `authzPolicies/atryum-policy` (REQUEST_AUTHZ, CUSTOM) — coexists with `agent-gateway-iap-policy` (DRY_RUN) |
| Callout VM | `atryum-callout` (10.0.0.2, no external IP) — docker: `atryum` (callout:v5, ext_authz :8090) + `nginx` (TLS :443 → :8090) |
| Reachability | Cloud DNS private zone `atryum-gw` → `callout.atryumgw.net` A `10.0.0.2`; gateway `networkConfig.dnsPeeringConfig` |
| Agent | `reasoningEngines/1876979048555479040` (Gemini 3.1, agent identity, `agent_gateway_config` set) |
| MCP tool | Cloud Run `legacy-dms` + Agent Registry mcpServer (tools `search_documents`, `get_document`) |
| Control-plane | ~55 Google-API endpoints registered in Agent Registry (global/regional/mtls variants) — the mtls fix |
| Network | VPC `gateway-vpc`, proxy-only + PSC subnets, network attachment `agent-gateway-na`, Cloud NAT, firewall `atryum-allow-gateway`/`atryum-allow-ssh` |
| Terraform | Local (Mac) worktree of `GoogleCloudPlatform/cloud-networking-solutions` demos/agent-gateway; state in GCS `singular-range-486318-b0-tfstate/agent-gateway/state`; trimmed `terraform.tfvars` |

**Admin API / rules:** the callout serves the Atryum admin API on the VM at `:8080` (in-VPC only). Rules were seeded via `POST /api/v1/admin/rules` with a static `X-API-Key`/`X-API-Secret` (values in the VM's `/opt/atryum/atryum.toml`). **`auth_debug.skip_verify=true` on the VM — see security must-fixes; do not carry this to any shared/prod deployment.**

Access the VM: `gcloud compute ssh atryum-callout --zone=us-central1-a --project=singular-range-486318-b0 --tunnel-through-iap`.

---

## How to reproduce / verify the live gate

1. Set an allow rule: `POST http://localhost:8080/api/v1/admin/rules` (on the VM) `{action:"auto_approve", server_patterns:["*"], tool_patterns:["mcp_tool_call"], enabled:true}`. Query the agent (`vertexai.agent_engines.get(RESOURCE).stream_query(...)`, see `driver/run_scenarios.py`) → the agent retrieves the document.
2. Flip to `auto_deny` for `mcp_tool_call`, re-query → the agent's tool call is blocked.
3. Confirm in the gateway audit log: Logs Explorer, `resource.type="networkservices.googleapis.com/Gateway" httpRequest.status=403` → expand `authzPolicyInfo` → `atryum-policy: DENIED`.

**Note the parking behavior:** with NO matching rule, an identified tool parks as `pending_approval` and fail-closes at the ~10s timeout (see issue on the policy provider). You must seed an explicit `auto_approve` rule to allow; the `always_approve` policy provider does **not** auto-approve this path.

---

## Walls hit and how they were resolved

1. **Gateway wouldn't provision ("internal error"/UNIMPLEMENTED).** Cause: missing network prerequisites, not an allowlist. Fix: the codelab Terraform (proxy-only + PSC subnets, network attachment, DNS peering, NAT). Needs Terraform ≥1.12.2 + a GCS state bucket.
2. **Agent discovered 0 tools — `CERTIFICATE_VERIFY_FAILED`.** The gateway **TLS-inspects the agent's own egress** and presents its inspection CA; ADK's registry client (httpx/certifi) rejected it. Fix: inject the gateway root CA (`agentGatewayCard.rootCertificates`) into the agent's trust store (prepend to the certifi bundle + monkeypatch `certifi.where`, imported first). **The reference codelab does not do this — a real gotcha for any non-Google agent client.**
3. **DNS peering "Config validation failed".** `targetNetwork` must **exactly string-match** the network-attachment's network format: `projects/P/global/networks/NAME` (NOT the `https://…` self-link, NOT the short name). Also the peered domain can't be under reserved `.internal`; used `atryumgw.net.`.
4. **Callout received 0 calls but gateway logged ALLOWED (fail-open).** The extension failed open on an unreachable callout. Real cause: reachability config. Fixed via the nginx TLS terminator + DNS peering (verified 91 real Check calls). Terraform pins `fail_open=false` for the real gate.
5. **ext_authz gives only method+path** (the caveat above) → path-level gating today, ext_proc for tool-level.
6. **Org constraints (do NOT change org policy):** `compute.vmExternalIpAccess` blocks VM external IPs (used `--no-address` + IAP SSH); Cloud Shell GCS writes trip an org trust-boundary (ran Terraform locally on the Mac instead).

---

## Known issues & suggested fixes

From a senior review (Fable) — **full text with file:line refs in [`REVIEW.md`](./REVIEW.md)**. The architecture (dep-free core + isolated gRPC adapter, reuse of the invocation path) is sound; the issues are at the seams between the code's assumptions and what the live gateway does.

**Ranked correctness issues**
- **A1 (high) — deny-and-resubmit is broken on live traffic.** `CallID` comes from `x-request-id`/`contextExtensions` (`parse.go:34`), both empty at REQUEST_AUTHZ, so every Check mints a new invocation; human approvals never reattach to the retry; parked pendings accumulate with no TTL. *Fix:* stable CallID (`mcp-session-id`+JSON-RPC id under ext_proc; bounded-window content hash until then) + a TTL/reaper for external pendings; stop documenting resume-on-retry until it works.
- **A2 (high) — default `DecisionTimeout` 30s exceeds the 10s gateway cap** (`types.go:62`, `README.md:64`), so the callout's deny-with-reason branch is unreachable. *Fix:* default 8s; validate/clamp `>=10s` as a hard error in config.
- **A3 (high) — the working tree breaks `TestCheck_Unattributable_FailsClosed`; the fail-closed default was inverted to pass-through** (`server.go:111-117`) without a test update. *Fix:* make the semantics explicit config + update the test (see B3).
- **A4 (med) — `parse.go` attributes non-`tools/call` JSON-RPC methods as tool calls** (uses `params.name` without checking `method`); batch arrays silently fail to parse. Harmless today (no body) but this is exactly the code CONTENT_AUTHZ will exercise.
- **A5 (med) — `x-serverless-authorization` used as AgentID** (`parse.go:22`) — that's a credential, not a label; it rotates per request and gets persisted into the audit DB. Drop it or verify the JWT and use `sub`/`email`.
- **A6 (med) — callout listen failure is non-fatal** (`main.go:367-371`) — if the gRPC port dies while the extension is failOpen/DRY_RUN, traffic flows ungated silently. Crash the process (or flip health + add readiness).
- **A7 (med) — the TEMP raw dump** (`server.go:78-91`) logs headers/body at Info; harmless while empty, leaks args/auth headers once CONTENT_AUTHZ lands. Remove before merge (log `Host` + `Attributes.Destination` once first — see B2).
- **A8 / A9 (low) — path fallback misses `/mcp/` variants** (should be config, not a hardcoded suffix); duplicate `ListenAddr` source of truth; no gRPC message-size/keepalive limits.
- **Policy provider is dead config on the external path** — confirmed: `Submit` (`invocation/service.go:919-1115`) has no policy fallback (unlike `Invoke`), so `[policy] provider=always_approve` is ignored for the callout path. The fail-closed park is safe but the config is a lie here. *Fix:* wire `s.policy` into Submit, or fail/warn at startup; ship a starter rule pack so the out-of-box UX isn't "hang 10s → generic 403 → orphaned pending".

**On the `mcp_tool_call` fallback (sound or hack?):** sound as an explicitly-scoped *surface-level egress gate*, currently framed as more than it is. (B1) it gates the whole JSON-RPC surface as one synthetic tool — name/document it as surface-level, not tool-level. (B2) `ServerName="mcp"` throws away the target backend — key it off `Host`/`:authority` so rules distinguish backends. (B3) the pass-through is the real hack — make it explicit `non_mcp_egress = deny|allow` config (default deny), log at Info with a counter.

**Top 5 recommendations (do in this order):**
1. Fix identity + idempotency end-to-end (A1, A5); until then remove "retry to resume" from docs.
2. Clamp the decision timeout to 8s + validate (A2) — one line, makes every deny reason reach the gateway.
3. Make the no-tool pass-through explicit default-deny config (A3/A8/B3), ServerName from Host (B2), and **commit + update the test**.
4. Reconcile Submit with the policy provider; ship a starter rule pack.
5. Build the ext_proc CONTENT_AUTHZ adapter (below) for real per-tool/arg gating, keeping REQUEST_AUTHZ as the fail-closed outer gate.

---

## The next build: ext_proc CONTENT_AUTHZ

Per-tool-name / per-argument gating (and LLM-judge on the payload) requires the body. Build a sibling adapter `internal/googlegateway/extproc/` implementing `envoy.service.ext_proc.v3.ExternalProcessor`, reusing `googlegateway.Service.Decide` unchanged (the dep-free split pays off here). The `Process` bidi-stream handler must:
1. **RequestHeaders:** capture method/path/`:authority`; if not MCP, CONTINUE + `ModeOverride` to skip the rest; if MCP, request `request_body_mode: BUFFERED` and **skip response-side processing** (never buffer a streamable-HTTP/SSE MCP response).
2. **RequestBody (end_of_stream):** parse JSON-RPC; only `tools/call` → `Decide` (real name + args + `mcp-session-id`); `initialize`/`tools/list`/notifications pass through; handle batch arrays; explicit body-size cap, fail closed above it (a truncated body must not become a silent allow).
3. **Deny:** `ImmediateResponse` 403 with a well-formed JSON-RPC error body (so MCP clients surface the reason). Same improvement applies to the ext_authz plain-text deny.
4. **CallID:** `mcp-session-id` + JSON-RPC `id` — this is what fixes A1 properly.

**Verify first:** whether GEAP's CONTENT_AUTHZ profile is ext_proc wire format or ext_authz-with-body. Design for ext_proc (strictly more capable) but confirm on a project where the gateway provisions. Before writing it, do one deploy that logs `Host` + `Attributes.Destination` from the existing callout to settle exactly what REQUEST_AUTHZ carries beyond method+path.

---

## The ADK plugin (second plane) — `examples/adk-plugin/`

In-process gating for code-first ADK agents: `atryum_gate` (a `before_tool_callback`) and `AtryumPlugin` (a Runner Plugin to cover all agents under a runner). Config via env/args (`ATRYUM_URL`, source, agent_id), fail-closed. Sees the full tool args (unlike the network plane) and returns a denial to the model. **Status:** authored by a sub-agent, syntax-checked, unit test included; **not yet run against a live `google-adk`** — validate imports/Plugin API against the installed ADK version and run `test_plugin.py` before relying on it. This is the "deep per-agent coverage" complement to the gateway's "enforced floor for all agents (incl. no-code)".

---

## Security must-fixes before any shared/prod demo

(Full matrix in `REVIEW.md` §d.) The urgent one: **`auth_debug.skip_verify=true`** on the callout VM makes `AdminMiddleware` pass every request — anyone with VPC reach can mint an `auto_approve` rule and neuter the gate. Set it `false`, put the admin API behind OIDC, and firewall/mTLS both `:443` and the admin port. Also: distroless `:nonroot` image, private-CA cert (or mTLS client-cert so only the gateway can reach `Check` — the open Check port is a policy-decision oracle and a DB-write flood vector), remove the TEMP dump, fix the credential-as-AgentID, and alert on callout health + on the policy sitting in DRY_RUN (a canary "must-be-denied" probe is the cheap mitigation).

---

## Teardown (when done)

```bash
# local Terraform worktree of cloud-networking-solutions demos/agent-gateway
terraform destroy   # (with GOOGLE_OAUTH_ACCESS_TOKEN set)
# then remove the manually-created bits:
gcloud beta network-security authz-policies delete atryum-policy --location=us-central1
gcloud beta service-extensions authz-extensions delete atryum-authz --location=us-central1
gcloud compute instances delete atryum-callout --zone=us-central1-a
gcloud dns record-sets delete callout.atryumgw.net. --zone=atryum-gw --type=A
gcloud dns managed-zones delete atryum-gw
# + the ~55 registry endpoints and the reasoning engine if not covered by destroy
```

---

## Not included / TODO

- The ext_proc CONTENT_AUTHZ adapter (spec above) — **the big one**.
- The 2 extra MCP tools (income-verification, corporate-email) and a scenario catalog were being authored when work was stopped; `driver/run_scenarios.py` is the runner but its `scenarios.json` is TODO.
- The `server.go` cleanups from the review (A2/A3/A7/B2/B3) + the failing test.
- Productionizing the callout (TLS/mTLS, distroless nonroot, admin auth, Postgres audit DB).
- Packaging the install as a Terraform module; org-policy mandate rollout playbook.
