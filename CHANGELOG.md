# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Live SSE relay for `tools/call`: when an upstream MCP server answers a tool
  call with a Server-Sent Events stream (Streamable HTTP transport, MCP spec
  2025-11-25), Atryum relays intermediate messages (progress, logging, other
  notifications) to the connected agent as they arrive, instead of buffering
  the whole response and returning only the terminal result. Activates only
  when the agent's request sends `Accept: text/event-stream` and the
  upstream responds with one; non-streaming tool calls are unaffected.
  If an upstream closes a resumable SSE response before the terminal
  JSON-RPC message, Atryum reconnects with `Last-Event-ID` and continues
  from the last event acknowledged to the upstream.
  Streamed events are audited into `invocation_events`
  (`invocation.stream_event` / `invocation.stream_completed`). New
  `[defaults]` config knobs: `stream_relay_enabled` (kill-switch, default
  on), `stream_header_timeout_seconds`, `stream_idle_timeout_seconds`,
  `stream_max_duration_seconds`, `stream_audit_max_events`,
  `stream_audit_max_event_bytes`. See `docs/architecture.md` for the full
  design.

## [0.2.0] - 2026-07-14

### Added

- Invocation Audit section in the FOSS UI, with audit event instrumentation
  showing AI evaluation reasons, failed states, and clear handling of
  no-match and deleted-rule cases.
- Grounding eval suite for the LLM-as-judge, runnable against any
  OpenAI-compatible endpoint.
- Session context for the judge, reconstructed from Atryum-tracked tool
  calls, with trust fencing, a byte cap, and expiry.
- Pagination for the Invocations list UI.
- Auto-sync of agent charters from ValidMind, with stale-record
  reconciliation on every sync.
- `insert_before` support: rules created from "Always approve/deny" insert
  at the top of the rule list and immediately apply their disposition to the
  triggering invocation.
- `rule_evaluated` audit events for human-fallback dispositions, keyed by
  rule ID.
- Release builds run from a pristine clone of the release tag, with the
  tag's commit stamped into the binaries as VCS provenance, and GitHub
  release notes are drawn from this changelog.
- `atryum version` command (also `--version`). Release builds report the
  release tag and VCS revision; the same version is announced in MCP
  handshake metadata (`serverInfo`/`clientInfo`) instead of a hardcoded
  `0.1.0`.

### Changed

- Rule creation from "Always approve/deny" now opens a pre-filled modal
  instead of a bare form.
- Shared agent hooks moved to the session model.
- Charter sync uses a JOIN instead of a subquery when listing ValidMind
  CUIDs with bindings.
- Token harness refreshed.

### Fixed

- SSE updates no longer fail to refresh the invocation list after
  pagination.
- Rule-creation success callback branches on the saved rule's action rather
  than the original menu item.
- Disposition check matches what Atryum actually emits (`human`, not
  `human_approval`); human disposition copy corrected for direct
  human-approval rules.
- MCP server endpoints reject spaces.
- `ADD COLUMN` store migrations are idempotent.
- Charter sync guards `DeleteSyncedStaleForOrg` against an empty org CUID.

### Security

- External session routes are gated behind authentication, and
  `RecordExecution` enforces session ownership.
- Empty agent bindings are rejected on session mint and use.
- Judge session context is trust-fenced, byte-capped, and expiring;
  tool-use caches are bounded.

## [0.1.0] - 2026-06-30

## [0.0.4] - 2026-06-14

## [0.0.3] - 2026-06-05

[Unreleased]: https://github.com/validmind/atryum/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/validmind/atryum/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/validmind/atryum/compare/v0.0.4...v0.1.0
[0.0.4]: https://github.com/validmind/atryum/compare/v0.0.3...v0.0.4
[0.0.3]: https://github.com/validmind/atryum/releases/tag/v0.0.3
