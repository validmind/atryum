# Senior review: Atryum x GEAP Agent Gateway integration (Fable)

Scope: `internal/googlegateway/` (+`extauthz/`), `cmd/atryum/main.go`, `internal/config/config.go`, `Dockerfile.callout`, `examples/google-agent-gateway/{README,RESULTS}.md`, plus the live-build working-tree diff and the invocation engine's external Submit path. Branch `john/geap-agent-gateway-spike`. The architecture (dep-free core + isolated gRPC adapter, reuse of the external-hook invocation path) is genuinely good; the issues below are mostly at the seams between the code's assumptions and what the live gateway actually does.

Housekeeping first: the behavior that was proven live is NOT committed. `internal/googlegateway/extauthz/server.go` is modified in the working tree and `Dockerfile.callout` is untracked. The diff also breaks a committed test (see A3). Commit before this drifts.

---

## (a) Correctness issues, ranked

**A1 (high): deny-and-resubmit does not work on real gateway traffic; parked pendings accumulate unboundedly.**
The whole HITL story (types.go:50-53, README.md:52-54: "the agent's retry (same idempotency key) resolves to allow") depends on `ToolCall.CallID`. It comes from `headers["x-request-id"]` or `contextExtensions["call_id"]` (parse.go:34). The raw-dump verification showed REQUEST_AUTHZ delivers empty headers and empty contextExtensions, so CallID is always "" in the live path, `idem` is nil (service.go:37-41), and every `Check` creates a brand-new invocation. Consequences:
- A human approving the parked invocation does nothing for the agent's retry; the retry parks a NEW pending invocation and gets denied again. The advertised resume loop cannot converge.
- Even where x-request-id exists, Envoy generates it per request; an agent-level retry gets a new one. It only dedupes gateway-internal retries of the same request, which is not what the docs claim.
- Nothing expires external pendings (Submit parks at invocation/service.go:1107-1114; the expiry path in `waitForHumanApproval` is only for the MCP-proxy flow). Agent retry loops flood the pending queue with duplicates, burying the one invocation a human should act on.
Fix: derive a stable CallID (under ext_proc: `mcp-session-id` + JSON-RPC id; until then, a bounded-window hash of agent+tool+args), add a TTL/reaper for external pendings, and stop documenting resume-on-retry until it demonstrably works.

**A2 (high): default `DecisionTimeout` 30s exceeds the verified 10s gateway hard cap.**
types.go:62 (`defaultDecisionTimeout = 30 * time.Second`) and README.md:64 (`decision_timeout_seconds = 30`) both violate the empirical finding in RESULTS.md ("timeout must be between 10ms and 10000ms"). With the live config, the gateway cancels the stream at 10s, so the callout's own deadline branch and its "pending human approval, retry to resume" deny (service.go:72-76) are unreachable; the agent sees the gateway's generic failure with no reason, and `await` exits via `ctx.Done()` as "request cancelled" into a void. types.go:49 even says the value MUST be below the gateway timeout, but nothing enforces it. Fix: default to 8s, and validate/clamp in config.go (`applyGoogleGatewayEnv`/`Load`) with a hard error at >= 10.

**A3 (high): working tree breaks a committed test; the fail-closed default was silently inverted.**
`TestCheck_Unattributable_FailsClosed` (extauthz/server_test.go:95-101) asserts PERMISSION_DENIED for a request with no identifiable tool. The live diff (server.go:111-117) now returns `allowed()` for that exact input. RESULTS.md's "13/13 tests pass" describes the committed version, not the proven-live one. Whatever the resolution of the pass-through question (see B), the test and the code must agree, and the inversion of a security default deserves an explicit, reviewed commit rather than a live hotfix.

**A4 (medium): parse.go attributes non-tool JSON-RPC methods as tool calls.**
parse.go:46-53 uses `params.name` without checking `method == "tools/call"`. `prompts/get` also carries `params.name`, so a prompt fetch would be gated and audited as a tool call named after the prompt. JSON-RPC batch arrays fail unmarshal silently (no tool extracted). Harmless today because REQUEST_AUTHZ delivers no body, but this is the exact code CONTENT_AUTHZ will exercise. Gate on the method, handle arrays, and pass non-tools/call methods through deliberately.

**A5 (medium): `x-serverless-authorization` used as AgentID (parse.go:22).**
That header carries a signed identity token, i.e. a credential, not an identity label. Using it as AgentID (a) persists a bearer credential into the invocations DB and renders it in the UI, (b) rotates per request, so agent-scoped rules can never match. Drop it, or verify the JWT and extract `sub`/`email`.

**A6 (medium): callout listen failure is non-fatal (main.go:367-371).**
`callout.Serve` errors are logged and the process stays healthy serving HTTP. If the gRPC port fails to bind (or the goroutine dies) while the extension is `failOpen: true` or the policy is in DRY_RUN, traffic flows ungated with no signal. The callout is the product here; make its failure crash the process (or at minimum flip the gRPC health status and a readiness endpoint).

**A7 (medium): the TEMP raw dump (server.go:78-91) logs headers and body at Info.**
Fine for the spike where everything was empty. The moment CONTENT_AUTHZ or richer attributes land, this logs tool arguments and authorization headers verbatim. Remove it before merge, or gate behind a debug flag with redaction. Note it currently also fails to log the two fields that would answer open questions: `httpReq.GetHost()` and `req.GetAttributes().GetDestination()`. Log those once before deleting (see B2).

**A8 (low): path fallback misses variants (server.go:104).**
`HasSuffix(p, "/mcp") || Contains(p, "/mcp?")` misses `/mcp/`, `/v1/mcp/stream`, or any backend mounted elsewhere, and each miss becomes a silent allow via A3's pass-through. The path convention should be configuration (per-gateway route prefixes), not a hardcoded string.

**A9 (low):** `googlegateway.Config.ListenAddr` is set (main.go:359) but never used by the service; the address is passed separately to `extauthz.New` (main.go:366). Duplicate source of truth. Also, `grpc.NewServer()` (server.go:57) sets no max message size, keepalive, or connection limits.

**On known-limitation #2 (default policy provider ignored):** confirmed at code level. `Submit` (invocation/service.go:919-1115) evaluates rules but has no `s.policy` fallback; `Invoke` does (service.go:~325). So `[policy] provider=always_approve` is dead config for the callout path, with `always_deny` equally ignored. The resulting fail-closed park is safe, but the config is a lie on this path and the divergence is documented only in RESULTS.md. Either wire the policy provider into `Submit` (consistent, and gives operators a working global default), or fail/warn loudly at startup when `[[google_gateway]]` is enabled alongside a non-manual policy provider. Current UX without rules is: every tool call hangs the full 10s, then a generic 403, plus one more orphaned pending row (A1). That is the worst of both worlds: slow AND opaque.

## (b) REQUEST_AUTHZ path-level gating + `mcp_tool_call` fallback: sound or hack?

Verdict: sound as an explicitly-scoped coarse gate, currently framed as more than it is. Three specific problems:

- **B1: it gates the whole JSON-RPC surface as one synthetic tool.** `initialize`, `notifications/*`, `tools/list`, and `tools/call` are all POSTs to `/mcp`, so with `manual_approval` and no rule, the agent cannot even complete the MCP handshake (each attempt parks 10s and dies). An allow rule on `mcp_tool_call` is really "this agent may use this MCP surface at all", which is a legitimate and useful policy primitive, but the invocations UI will show piles of `mcp_tool_call` entries with empty input, which weakens the audit narrative. Name and document it as surface-level egress gating, not tool gating.
- **B2: `ServerName = "mcp"` constant (server.go:107) throws away which backend was targeted.** The CheckRequest almost certainly carries `httpReq.Host`/`:authority` and `Attributes.Destination` even at REQUEST_AUTHZ (the dump never logged them). If populated, key ServerName off the host so rules distinguish MCP backend A from backend B. That single change makes path-level gating meaningfully useful in multi-backend gateways. Verify before removing the dump (A7).
- **B3: the pass-through (allow when no tool identified) is the actual hack.** Its safety rests on two unenforced assumptions: that only MCP destinations traverse the authz policy, and that every MCP path matches the hardcoded suffix (A8). The committed code made the opposite (deny) choice, and the flip happened live without a test update (A3). If control-plane/LLM egress genuinely routes through this policy, pass-through is necessary; if it does not (likely: the authz policy binds to the agent gateway forwarding rule, which fronts tool traffic), pass-through is pure bypass surface. Make it explicit config, `non_mcp_egress = "deny" | "allow"`, default deny, log at Info with a counter either way (server.go:116 currently logs at Debug, so bypasses are invisible at default log level).

## (c) The CONTENT_AUTHZ / ext_proc gap and how to build it

Per-tool-name and per-argument gating requires the body, which REQUEST_AUTHZ verifiably does not deliver. Build a sibling adapter `internal/googlegateway/extproc/` implementing `envoy.service.ext_proc.v3.ExternalProcessor` and reusing `googlegateway.Service.Decide` unchanged (the core abstraction already supports this cleanly; that is the payoff of the dep-free split).

The `Process` bidirectional-stream handler must:
1. **On RequestHeaders:** capture method, path, `:authority`, and any tool-metadata attributes. If not an MCP route, respond CONTINUE and set `ModeOverride` to skip everything else. If MCP, request the body (`request_body_mode: BUFFERED`) and skip response-side processing (SSE responses must stream back untouched; never buffer the response of a streamable-HTTP MCP server).
2. **On RequestBody (end_of_stream):** parse JSON-RPC. Only `tools/call` goes to `Decide` (with real name, arguments, and `mcp-session-id`); `initialize`, `tools/list`, notifications pass through; handle batch arrays; enforce an explicit body-size cap and fail closed above it (Envoy's per-message limits will truncate large bodies; a truncated body must not silently become an allow).
3. **On deny:** return `ImmediateResponse` with 403 and a well-formed JSON-RPC error object as the body, so MCP clients surface the reason to the model instead of choking on a bare 403 (the current ext_authz deny body, server.go:135-145, is plain text; same improvement applies there).
4. **CallID:** `mcp-session-id` + JSON-RPC `id`. This is what fixes A1 properly: dedupe becomes real, and deny-and-resubmit can genuinely resolve on retry.
5. **Budget:** the whole header+body exchange must finish inside the extension timeout; keep `DecisionTimeout` at ~8s and the LLM-judge under that.

One thing to verify before building: whether GEAP's CONTENT_AUTHZ profile is actually ext_proc wire format or ext_authz-with-body (`with_request_body`). README.md:125 assumes a `policyProfile` switch; the managed gateway never provisioned, so this is untested. Design for ext_proc (strictly more capable) but confirm on a project where the gateway provisions.

## (d) Security hardening: demo-acceptable vs must-change

Demo-acceptable as-is, must change for anything customer-facing:

| Item | Demo | Production requirement |
|---|---|---|
| Self-signed TLS on callout, gateway does not validate | OK, documented | Private CA-issued cert; enable validation when the extension supports it; until then, treat gateway-to-callout trust as equal to VPC trust and say so |
| nginx h2c sidecar terminating :443 | OK | Terminate TLS in the Go server (`grpc/credentials`) or keep nginx with a real cert; add mTLS client-cert verification so only the gateway can reach `Check`. An open Check port is a policy-decision oracle AND a DB-write flood vector (every unmatched call inserts invocation+event rows; there is no rate limit) |
| `auth_debug.skip_verify = true` | Barely OK on an isolated VM | **Most urgent item.** With SkipVerify, `AdminMiddleware` passes every request (internal/auth/middleware.go:33, 94): anyone with VPC reach can mint an `auto_approve` rule for `server=mcp, tool=mcp_tool_call` and silently neuter the gate. Must be false; admin API behind OIDC; static X-API-Key into a secret manager with rotation |
| Container as root, docker host networking | OK | `gcr.io/distroless/static-debian12:nonroot` (Dockerfile.callout:16 uses the root variant), bridge network with one published port, read-only rootfs |
| TEMP raw-dump logging | OK (payloads were empty) | Remove (A7); mandatory before CONTENT_AUTHZ |
| `x-serverless-authorization` persisted as agent_id | Latent | Fix per A5; it is credential material in the audit DB |
| SQLite decision-audit DB on VM disk | OK | Encrypted disk + backups, or use `database_url` (Postgres) which config.go:146 already supports |
| failOpen / DRY_RUN | Terraform pins `fail_open = false` (terraform/main.tf:87), good | Also alert on callout health and on the policy sitting in DRY_RUN; those are the two switches that silently un-gate everything. Consider a periodic self-probe: a canary request that must be denied, alarming if it succeeds |

On the failOpen story generally (known-limitation #3): the repo makes the safe choice easy to copy (README yaml line 103 and Terraform both pin false with comments), which is the right posture. The residual gap is that nothing in Atryum itself can detect it is being failed-open around; the canary probe above is the cheap mitigation, and A6 (crash on callout death) is the complement on the Atryum side.

## (e) Top 5 recommendations

1. **Fix identity and idempotency end-to-end** (A1, A5): stable CallID (session id + JSON-RPC id once ext_proc exists; bounded-window content hash until then), TTL/reaper for external pendings, drop the auth-header-as-AgentID. Until then, remove "retry to resume" from README/types docs; it is currently untrue on live traffic.
2. **Clamp the decision timeout** (A2): default 8s; config validation rejects >= 10s; update README.md:64. One-line change that makes every deny reason actually reach the gateway.
3. **Make the no-tool pass-through explicit, default-deny config** (A3, A8, B3): `non_mcp_egress` setting, Info-level logging with a counter, configurable MCP path prefixes, ServerName from `:authority`/Host (B2). Update `TestCheck_Unattributable_FailsClosed` to match the chosen semantics and commit the working tree plus Dockerfile.callout.
4. **Reconcile Submit with the policy provider** (limitation #2): wire `s.policy` into the external Submit fallback like `Invoke`, or fail startup when `[[google_gateway]]` is combined with a provider it will ignore. Ship a starter rule pack (server=mcp, tool=mcp_tool_call) so the out-of-box experience is not "hang 10s, generic 403, orphaned pending".
5. **Build the ext_proc CONTENT_AUTHZ adapter** per (c) for real per-tool/per-arg gating, keeping the REQUEST_AUTHZ callout as the fail-closed outer gate. Before writing it, spend one deploy logging `Host` and `Attributes.Destination` from the existing callout to settle what REQUEST_AUTHZ actually carries beyond method+path.

Immediately before any shared-infra demo: `skip_verify=false` and firewall/mTLS on both :443 and the admin port. That one config flag currently undoes everything the gateway integration proves.

Files: `/Users/jwalz/Code/ValidMind/atryum-geap-spike/internal/googlegateway/{types.go,service.go,parse.go}`, `internal/googlegateway/extauthz/{server.go,server_test.go}`, `cmd/atryum/main.go:349-373`, `internal/config/config.go:85-113`, `internal/invocation/service.go:919-1115`, `internal/auth/middleware.go:23-96`, `Dockerfile.callout`, `examples/google-agent-gateway/{README.md,RESULTS.md,terraform/main.tf}`.
