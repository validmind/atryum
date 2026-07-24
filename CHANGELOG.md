# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.3.0] - 2026-07-24

### Added

- Atryum can now be embedded as a Go library by downstream programs: import
  `github.com/validmind/atryum/pkg/atryum` and call `atryum.Main(...)` from
  another binary's `main` package, with `WithRoutes`, `WithMigrations`, and
  `WithDatabase` extension points.
- Agent plan preapproval: agents can submit an entire plan for approval
  before executing its individual tool calls. Each planned action is
  evaluated against the existing invocation rules, and a shared LLM judge
  reviews the whole plan for charter compliance once.
- Server-side session get-or-create, keyed by agent binding and the
  caller's own client session ID. Harnesses (the shared Claude Code/Cursor/
  Codex hook, amp, pi) now just send their own session/thread ID on every
  tool call — Atryum resolves or creates the matching session itself, so
  no harness needs to mint, cache, or retry session creation anymore.
- Harnesses can poll a rules endpoint every 5 minutes to fetch their
  current approval rules, so agents can read their own rules and reduce
  denied calls; the MCP rules tool is available again as well.
- Charter preview for agents in the admin UI: synced agents show the
  charter hierarchy assembled from the ValidMind backend, local agents
  show their own stored charter.
- Logout button in the UI.
- Copy-to-clipboard button on the MCP Endpoint field.
- CI workflow publishing the production Atryum image to Docker Hub.
- Initial architecture documentation.

### Fixed

- `ClearSessions` now stops each session's background watcher immediately
  after that session's own delete succeeds, instead of deleting everything
  first and cancelling watchers in a second pass — a delete failing partway
  through could previously leave already-deleted sessions with a watcher
  still running (and still able to approve/deny tool calls) until the
  process restarted. The reported cleared-count is also now the honest
  partial count rather than always `0` on error.
- A rule whose stored server/tool/agent scope had become corrupted (bad
  manual edit, partial write, disk corruption) could silently start
  matching everything it wasn't scoped to; corrupted rule data now blocks
  the rule load and falls back to human review instead.
- A database error while loading rules during an external tool-call
  submission is now logged and recorded in the invocation's audit trail,
  instead of failing silently.
- `Invoke` no longer falls through to the permissive global policy when
  approval-rule loading fails (e.g. a brief database hiccup) — a rule-load
  failure now safely requires human approval and is logged, matching how
  `Submit` already behaved.
- AI-decided invocations (hard denials and auto-approvals) now persist
  their `matched_rule_id`, so the invocation audit view no longer mislabels
  a still-present rule as "Deleted Rule". The UI also distinguishes a rule
  that is simply not in the loaded list ("Unknown rule") from one that is
  genuinely unrecorded or deleted.
- Doc generation (`just docs`) no longer chops off the first character of
  3-space-indented numbered-list continuation lines.

### Security

- Fixed an issue where an external executor could mark a tool invocation
  as completed, failed, or cancelled before it was approved — bypassing
  human approval and forging the audit record. Execution outcomes can now
  only be reported for approved invocations, executors may only report on
  their own invocations, and recorded outcomes can no longer be
  overwritten; retrying an already-recorded outcome is a safe no-op.

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

[Unreleased]: https://github.com/validmind/atryum/compare/v0.3.0...HEAD
[0.3.0]: https://github.com/validmind/atryum/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/validmind/atryum/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/validmind/atryum/compare/v0.0.4...v0.1.0
[0.0.4]: https://github.com/validmind/atryum/compare/v0.0.3...v0.0.4
[0.0.3]: https://github.com/validmind/atryum/releases/tag/v0.0.3
