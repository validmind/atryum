# Atryum harness integration tests

End-to-end tests that wire real AI agent harnesses through Atryum to upstream MCP
servers. Exercises harness MCP quirks, hook integrations from `examples/`, and
harness→Atryum inbound auth protocols.

Approval uses **rules only** — each test run seeds a catch-all `auto_approve` rule
via `POST /api/v1/admin/rules` (no `[policy]` provider).

## Quick start

```bash
# From repo root
just integration-test                              # defaults: fake-agent / no-auth / calculator
just integration-test codex no-auth calculator     # positional arguments
just integration-test-matrix

# Or directly
cd integrations
./scripts/agent_harness_integration_tests.sh list
./scripts/agent_harness_integration_tests.sh run --harness fake-agent --auth no-auth
./scripts/agent_harness_integration_tests.sh matrix --only-passing
```

## Commands

| Command | Purpose |
|---------|---------|
| `list` | Print harnesses, auth protocols, MCP targets |
| `run` | Single case: `--harness`, `--auth`, optional `--target` |
| `matrix` | Full cross-product; filters via `--harnesses`, `--auth`, `--targets`, `--only-passing` |

## LLM credentials vs Atryum auth

Harness LLM billing credentials (never committed):

| Harness | Env var | Notes |
|---------|---------|-------|
| Codex, Pi | `OPENAI_API_KEY` | API billing; no Codex subscription OAuth needed |
| Claude Code | `ANTHROPIC_API_KEY` | API billing |
| Amp | `AMP_API_KEY` | Or existing `amp login` session |
| Grok | `XAI_API_KEY` | |
| Cursor | `CURSOR_API_KEY` | Optional if `cursor-agent status` shows a subscription login |

Harness→**Atryum** auth is configured separately (`no-auth`, `oauth-client-credentials`, `oauth-dcr`, …).

## Token refresh harness testing

For the OAuth protocols (`oauth-client-credentials`, `oauth-dcr`), harness setup
writes a token-minting script against the mock OIDC server and exports it as
`ATRYUM_TOKEN_COMMAND` (`lib/auth.sh`, `harness_token_command_env`), so hook and
extension integrations from `examples/` exercise the full refresh path: mint on
first request, cache to `token-cache.json`, re-mint near expiry, and retry once
after a `401`. The `static-bearer` protocol exports `ATRYUM_ACCESS_TOKEN`
instead, which is used as-is and never refreshed.

To run a single refresh-exercising case, e.g. Pi against client credentials:

```bash
cd integrations
./scripts/agent_harness_integration_tests.sh run --harness pi --auth oauth-client-credentials
```

## Registries

| File | Contents |
|------|----------|
| `config/harnesses.yaml` | CLIs, invoke templates, hooks, MCP wiring |
| `config/auth-protocols.yaml` | Inbound Atryum auth modes |
| `config/mcp-targets.yaml` | Upstream MCP servers + verification prompts |

Default MCP target: [mcp-server-calculator](https://github.com/githejie/mcp-server-calculator) (pip-installed in `.venv`).

## Mock OIDC

`auth/mock-oidc/` — lightweight alternative to Keycloak for `oauth-client-credentials` and `oauth-dcr` cases.

## CI

`.github/workflows/agent-harness-integration-tests.yml` runs the `fake-agent` baseline on every PR.
Optional live-harness jobs run when repository secrets (`OPENAI_API_KEY`, etc.) are configured.

## Local Nix (optional, not committed)

`shell.nix` is gitignored for NixOS developers who want isolated toolchains.

## Layout

```
integrations/
├── scripts/agent_harness_integration_tests.sh   # entrypoint
├── lib/                                         # atryum, auth, harness helpers
├── config/                                      # YAML registries
├── fixtures/atryum/                             # per-auth TOML templates
├── auth/mock-oidc/                              # lightweight OIDC
└── requirements.txt                             # PyYAML, calculator MCP, mock OIDC deps
```