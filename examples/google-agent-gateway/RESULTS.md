# Atryum x Google Agent Gateway: verification results

What was actually built, run, and verified for the Agent Gateway authorization
extension integration. Date: 2026-07-09. Branch: `john/geap-agent-gateway-spike`.

## Summary

Atryum runs as a Google Agent Gateway **authorization extension**: a gRPC Envoy
`ext_authz` callout the gateway invokes for every agent tool call, answering
allow/deny through the normal Atryum rule engine. Verified locally (real protocol,
real engine) and on real GCP (Cloud Run + the full Service Extensions backend
chain). The one piece that could not complete is the managed Agent Gateway resource
itself, which returns a Google-side internal error in a fresh trial project (see
Boundary).

## Code (all green)

- New package `internal/googlegateway/` (dep-free core) + `internal/googlegateway/extauthz/`
  (the Envoy ext_authz v3 gRPC adapter, isolated so the core builds without gRPC).
- Config `[[google_gateway]]`, `main.go` wiring, gRPC health service.
- `go build ./...` clean, `go vet` clean, **13/13 tests pass** (9 core + 4 ext_authz
  handler). Reused Atryum's `invocation.Service` external-hook path unchanged.
- Required fix: `internal/store/rules.go` wrote NULL into the `NOT NULL`
  `atryum_llm_config_id` column via `emptyToNil`, breaking rule creation. Changed to
  persist `""`. (This is the same bug fixed on `fix/vm-path-rule-not-null-...`.)

## Local end-to-end (real Envoy ext_authz -> real Atryum engine)

Ran the real `atryum` binary with the callout on `:8090`, seeded two rules via the
real admin API, and drove it with a synthetic Agent Gateway (`.../local-demo/probe`)
sending real `envoy.service.auth.v3.CheckRequest`s:

| tool | result | Atryum invocation |
|---|---|---|
| `read_file` | **ALLOW** (auto_approve rule) | `approved` |
| `bash` | **DENY** 403 "matched approval rule (auto_deny)" | `denied` |
| `transfer_funds` | **DENY** 403 (no rule, fail-closed at timeout) | `pending_approval` |

## Cloud end-to-end (real GCP, project singular-range-486318-b0, us-central1)

- Cross-compiled a static linux/amd64 binary (modernc sqlite is pure Go, CGO off),
  baked a 2-rule seed DB + config into a distroless image, pushed to Artifact
  Registry.
- Deployed to **Cloud Run** (`atryum-callout`) with HTTP/2 for gRPC. Startup logs:
  `google agent gateway ext_authz callout listening addr=:8080`.
- Probed the Cloud Run callout over TLS with a real impersonated identity token:

| tool | result (in the cloud) |
|---|---|
| `read_file` | **ALLOW** |
| `bash` | **DENY** 403 |
| `transfer_funds` | **DENY** (fail-closed) |

- Built the full Service Extensions backend chain:
  - serverless NEG `atryum-callout-neg` -> Cloud Run
  - regional backend service `atryum-callout-be` (INTERNAL_MANAGED, HTTP2)
  - **authz-extension `atryum-authz`** registered: `EXT_AUTHZ_GRPC`, `failOpen: false`,
    `timeout: 10s`, pointing at the backend service.

## Empirical findings

1. **Callout timeout ceiling is 10s (hard).** Importing the authz-extension with
   `timeout: 60s` was rejected: *"The timeout must be between 10ms and 10000ms
   inclusive."* This definitively answers the HITL question: auto-rules and a fast
   LLM-as-judge fit inline; human approval beyond 10s **must** use deny-and-resubmit
   (which the callout does). Set to the 10s max for maximum LLM-judge headroom.

2. **The external-submit path parks unmatched calls as `pending_approval`** rather
   than auto-resolving via the configured default policy provider (observed with both
   `always_approve` and `always_deny` defaults). The callout fails closed on the
   pending state, so this is safe, but rules (not the default provider) are what
   produce inline allow/deny. Author explicit rules for allow paths.

## Boundary reached

The managed Agent Gateway resource would not provision in this fresh trial project:

- `agentGateways import` (googleManaged, MCP) in `us-central1` -> `code: 13, "an
  internal error has occurred"` (opaque, no detail in the operation).
- Same in `global` -> `UNIMPLEMENTED`.
- Not a schema problem (the YAML validates) and not a missing registry (Agent
  Registry is implicit per project/location, confirmed via the agentregistry
  discovery doc). This is a Google-side onboarding/preview limitation of a brand-new
  project, outside client control.

Everything up to and including the registered authz-extension is real and verified.
The only missing hop is binding an `authz-policy` (action=CUSTOM ->
customProvider.authzExtension) to a live gateway, which requires the gateway to exist.
When the platform provisions the managed gateway (or you use a self-managed
ALB/Secure Web Proxy), the bind is one `gcloud network-security authz-policies import`.

## Resources created (teardown)

```bash
REGION=us-central1
gcloud service-extensions authz-extensions delete atryum-authz --location=$REGION -q
gcloud compute backend-services delete atryum-callout-be --region=$REGION -q
gcloud compute network-endpoint-groups delete atryum-callout-neg --region=$REGION -q
gcloud run services delete atryum-callout --region=$REGION -q
gcloud artifacts repositories delete atryum --location=$REGION -q
gcloud iam service-accounts delete atryum-caller@singular-range-486318-b0.iam.gserviceaccount.com -q
```

(The Cloud Run service scales to zero; the NEG / backend service / authz-extension
are free while idle, so they can be left in place for inspection.)
