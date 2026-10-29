# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Security

- Any signed-in user could take over another agent's Claude Managed Agent.
  `POST /api/v1/agents` is open to every user, and Atryum resolves an
  identity by an agent's aliases (`agent_ids`) before its Claude Managed
  Agent bindings, so an alias on a Claude agent ID bound to another agent took
  that agent's calls, which then ran under the alias holder's rules and
  charter. This happened whether the alias came first or the binding did.
  `force_claude_managed_agent_connect` likewise let any user move a binding
  off another agent. Now an alias on a Claude agent ID bound to another agent,
  and a binding of a Claude agent ID that is another agent's alias, are both
  refused (409, for admins too), and only an admin may force connect (403
  otherwise). An admin's force connect also removes the Claude agent ID from
  any other agent's aliases, so an alias can't lock admins out of binding it.
  The checks run before the write, not in one transaction, so two concurrent
  requests can still both pass; the startup warning below finds those.
- A 409 for an identity another agent holds names that agent only to an
  admin. Members see only their own agents, so they're told the ID is
  claimed by another agent.
- At startup Atryum logs a warning for each identity that one agent has as an
  alias while another has it bound, made before these checks existed. Its
  calls go to the alias holder until it's removed from one of them.

### Fixed

- A tool call's arguments keep their numbers as the agent wrote them. The
  MCP proxy, `POST /api/v1/invocations`, `POST /api/v1/external/invocations`
  and the managed-agents bridge decoded arguments into `float64`, so an
  integer beyond 2^53 or a decimal with more digits than `float64` holds
  changed: a transfer to account `9007199254740993` reached the upstream
  tool, the decider, the stored invocation and its events as
  `9007199254740992`, and a hook's own check refused the call because the
  invocation no longer matched it. They are decoded with `UseNumber` now,
  so arguments hold `json.Number`. Rule conditions read an exact number as
  before (`2.50` is `2.5` for `eq` and `in`), and any other as written;
  `gt` and `lt` compare as `float64`, as before. The AI summary of an
  invocation gets the same digits, so it no longer names `…992`.
- `policy.ExactNumber` reports whether a `json.Number` survives `float64`,
  for an embedder that hashes or compares arguments: two calls that differ
  only in a digit `float64` can't hold must not count as the same call.

## [0.7.0] - 2026-10-28

### Added

- A denied agent is told why. `policy.Verdict` carries the judge's account of
  its decision — its reasoning, the charter clauses it relied on and the
  parts of the call it quoted with what each does — and `policy.Result`
  carries it to the caller as `Judge`, with `JudgeErr` set instead when the
  judge was asked and could not answer. `invocation.Explanation` puts the
  same account on a `Decision` and on an invocation's `Approval`
  (`explanation` on the API), and a `Decider` that fills it in has it
  rendered for the agent whose call it was, on a denial. The account says
  why a call was refused, never what would get it allowed: an agent works
  that out from its charter rather than being handed a route around the
  decision. Atryum's own AI evaluation has only a reason to give, which it
  already sent, so a structured account comes from a `Decider` that fills
  one in.
- An account is model-written text, drawn partly from arguments the calling
  agent chose, and it is rendered back into that agent's context, so it is
  treated as untrusted data. `Explanation.Bounded` trims it to
  `MaxEvidence`, `MaxClauses` and the length limits beside them, turns
  control characters and line breaks into spaces, and is applied to every
  decider's answer as it arrives. `invocation.DeniedText` renders it inside
  a marked block labelled information rather than instructions, so an
  account cannot pose as part of Atryum's own message, and cleans the one
  line reason above it the same way. Everything in an account reaches the
  agent, so a decider puts nothing in it the agent may not see.
- The bundled hooks (`examples/shared-agent-hook`, `examples/pi-extension`)
  print the text Atryum rendered for a refused call instead of composing
  their own, so an agent refused through a hook and one refused over the MCP
  proxy are told the same things in the same order.

### Changed

- **Breaking:** `policy.Judge` returns `(Verdict, error)` in place of
  `(Action, error)`. A judge that only decides becomes
  `return policy.Verdict{Action: a}, nil`; one that explains itself fills in
  the rest. `policy.Result.Basis` is unchanged, so anything reading the basis
  text keeps working. `Verdict` embeds `policy.Explanation`, which
  `invocation.Explanation` aliases, so an account passes from the rule
  engine to a decision without being copied field by field.
- Every path that denies a tool call renders its message through
  `invocation.DeniedText`, so every agent is told the same things in the
  same order. A call denied by an `auto_deny` approval rule now carries the
  reason it already recorded but previously kept to itself.
- **Breaking:** a `Judge` that answers `""`, or anything other than allow,
  deny, human or the new `policy.ActionNext`, now sends the call to a person
  ("… → judge gave no verdict, a person decides") instead of moving on to the
  level's next matching rule. A judge that means to defer returns
  `ActionNext`; an embedder whose judge returned `""` to defer must return
  `ActionNext` instead.

### Security

- A judge reply that couldn't be read no longer reaches the next matching
  rule, which is often a broad allow: behind `payments over 10000 → judge`
  then `payments → allow`, a large payment whose judge reply was cut off or
  malformed was allowed. The agent writes the arguments the judge reads, so
  it could steer the reply there. In `pkg/policy` (above) and in the
  standalone service, where a verdict from the ValidMind backend's judge
  other than `next_rule` that Atryum doesn't recognise now goes to human
  approval instead of the next rule.

## [0.6.0] - 2026-10-23

### Fixed

- `policy.Evaluate` now also decides two cleaned-up copies of each call (one
  with spelling settled, one with paths cleaned too) and keeps the strictest
  answer, so a deny or hold rule on an argument is no longer evaded by
  another spelling of the same value: `//etc/passwd`, `/tmp/../etc/passwd`,
  `/ETC/passwd`, `%2Fetc%2Fpasswd`, `file:///etc/passwd`, extra spaces,
  zero-width characters or full-width letters. Windows paths are cleaned the
  way Windows opens them: `C:\..\Windows`, `\\?\C:\Windows`, `C:\Windows.\`,
  `SAM::$DATA` and `SAM:stream` all match a rule on `C:\Windows\`. A shell
  command is never only read as a path, so `rm  -rf ../x` still matches a
  rule on `rm -rf`. A second key that only differs in case (`"PATH"` beside
  `"path"`) can't hide the argument, two rules with the same ID are still
  two rules, and facts are compared as the embedder set them. Cleaning can only make a
  decision stricter, never more permissive; the call that runs is the one the
  agent sent, and a judge is asked at most once per rule. Short names
  (`PROGRA~1`), shares, environment variables and links still name a file
  in ways text alone can't tell: prefer allow rules on paths, and see
  `policy.AmbiguousPath` below.

## [0.5.0] - 2026-10-23

### Added

- `policy.AmbiguousPath`, for `facts.ambiguous_path`: true when a call's
  arguments hold a path only the machine running the tool can resolve: a
  variable (`%SystemRoot%`, `$HOME`), a leading `~`, a Windows short name
  (`PROGRA~1`), a share or device (`\\host\C$`, `\\.\pipe`), a drive-relative
  path, a relative path that climbs out (`../../etc`) or a `/proc` link. A firm
  rule on it sends such calls to a person. The `pkg/policy` package doc and
  the new "Path arguments" section of `docs/architecture.md` describe allow
  rules on paths and what upstream servers must check themselves.
- Per-user access views in the Users UI: clicking a user (or the new
  **Agents** / **Keys** links on their row) opens `/ui/users/{id}/agents`
  and `/ui/users/{id}/keys`, listing the agents that user is a member of
  and every API key they have issued across agents, with membership
  removal and key revocation in place. Backed by two new admin endpoints,
  `GET /api/v1/users/{id}/agents` and `GET /api/v1/users/{id}/keys`; the
  latter returns the usual key rows plus an `agent_name` field.
- Several Claude Codes on one machine can now act as different Atryum
  agents. `ATRYUM_HOME` is the single root for everything the Claude hook
  touches: `atryum setup claude` and `atryum hooks install claude-code`
  install the hook script under `$ATRYUM_HOME/hooks/`, reference it by
  absolute path, and bake `ATRYUM_STATE_DIR=$ATRYUM_HOME/agent-hook-state`
  into the hook commands whenever the home is not the stock `~/.atryum`.
  The `claude-code` hook target honours `CLAUDE_CONFIG_DIR` for the
  `settings.json` it writes, and without `--agent` the default agent and
  key names gain the home's basename so two instances do not share an agent
  record. One env pair per instance is enough:
  `ATRYUM_HOME=~/.atryum-b CLAUDE_CONFIG_DIR=~/.claude-b atryum setup claude`,
  then launch Claude Code with the same two variables. The shared hook also
  falls back to `$ATRYUM_HOME/agent-hook-state` when only `ATRYUM_HOME` is
  set. Default installs are unchanged.
- `pkg/atryum.WithDecider`: an embedding program decides every tool call on
  every path (MCP proxy, direct invocation, harness hook, Managed Agents
  bridge) as allow, notify, deny, or pending with a review ID. Atryum's rules,
  plans, default policy, operator API and UI are then not used; a decider error
  denies the call.
- `pkg/atryum.WithAgentAPIKeys(enabled)`: turns agent API keys (`atr_…`) on
  or off for the agent routes. They stay on by default, and are off by
  default under `WithDecider`, whose embedder keeps its own agent registry;
  when off, an `atr_` bearer gets 401 on `/mcp/`, `/api/v1/invocations` and
  `/api/v1/external/*`.
- `pkg/atryum.WithEventHook`: called inside the transaction that changes an
  invocation, with that transaction, so an embedding program can append its
  own records atomically.
- `pkg/atryum.Migrate` applies the built-in and extension migrations without
  starting the server.
- `pkg/atryum.WithDatabaseURL` sets the database URL for an embedding
  program, overriding `server.database_url`.
- `pkg/atryum.WithRequireAgentAuth`: the server refuses to start without an
  enabled `[[auth]]` issuer or with `auth_debug.skip_verify` (or
  `ATRYUM_AUTH_DEBUG_SKIP_VERIFY`) set, or with an issuer or JWKS URL that
  is not HTTPS (plain HTTP is allowed only on loopback), and agent routes
  reject any request without a verified agent identity.
- `pkg/policy`: a two-level rule engine (firm and agent rules, first match per
  level, strictest level wins, judge rules that fall back to a person; a level
  whose matching rules are all judges with no verdict is `human`, never a
  pass to the other level) and a secret-literal detector, `ContainsSecret`,
  for `facts.contains_secret`.
- `pkg/atryum.WithStuckReports`: the MCP proxy offers `atryum_report_stuck`
  (`task`, `boundary`, optional `detail`, capped at 1, 1 and 4 KB). It
  authorizes nothing; it records an invocation of that tool in the new status
  `reported`, which the event hook sees once, as `invocation.stuck_reported`,
  and acknowledges the agent. The server in the MCP path must be configured.
- `pkg/atryum.WithDBPool` and `[server.database_pool]` size the PostgreSQL
  connection pool, which now defaults to 20 open and 10 idle connections,
  idle ones closed after 5 minutes and each replaced after 30 (it was
  unbounded).
- `server.database_listen = false` turns off the PostgreSQL decision listener
  (for PgBouncer in transaction mode, which never delivers notifications);
  held calls then re-read their row every 250 ms.
- `pkg/atryum.WithResolvePending` hands an embedding program a
  `ResolvePending(ctx, tx, invocationID, approve, actor, reason)` that approves
  or denies a held call through Atryum's own compare-and-set path, inside the
  embedder's transaction (or Atryum's own when `tx` is nil): the event row,
  the event hook and the Postgres wake-up all commit with it. A call no longer
  pending returns an error wrapping `atryum.ErrNotPending`.

### Fixed

- The shared agent hook (Claude Code, Codex, Cursor) no longer waits
  without limit for a held call. Claude Code and Codex kill a hook after
  600 seconds by default and then let the call run, so a call left
  pending past that ran unapproved. The hook now denies it after
  `ATRYUM_HOLD_WAIT_MS` (default 540000, nine minutes), counted from its
  start.

- `GET /api/v1/agent/rules` and the MCP `tools/list` policy annotations
  now resolve API-key-authenticated agents by their `agents.id`, the same
  lookup order the enforcement path uses. Before, a key-authenticated
  agent's UUID never matched the `agent_ids` alias list, so the advisory
  response silently fell back to the sync default agent's rules (or none)
  while enforcement used the right ones.
- `atryum hooks install` now always replaces previously installed Atryum
  hook entries. Running it after `atryum setup claude` used to leave the
  env-prefixed entries in place and add bare copies beside them, firing the
  hook twice per event with the second copy hitting Atryum unauthenticated.
- Roles set in the Users UI (or via `PATCH /api/v1/users/{id}`) are kept
  across logins. A new `users.role_source` column (migration 031) records
  `manual` once an operator sets a role; `UpsertLogin` only refreshes role
  from the IdP admin claim while it is still `idp`. The API exposes
  `role_source` on user rows.
- `/api/v1/agents/{id}/members` authorizes before probing the agents table,
  so a non-member gets 403 for unknown and existing ids alike instead of an
  enumeration oracle (the keys route already did this).
- `atryum setup claude --help` prints the `setup claude` usage with its
  `--url`/`--agent`/`-y` options; it used to be caught by the generic
  `setup` help scan and print the parent usage instead.
- `docker-compose.yml` publishes Postgres on `127.0.0.1:5432` rather than
  all interfaces; the credentials in that file are static, so a `0.0.0.0`
  bind exposed the whole database to anyone who could reach the host.
- Upstream OAuth discovery now tries the RFC 8414 / RFC 9728 *path-inserted*
  well-known URLs (`https://host/.well-known/oauth-authorization-server/<path>`
  and `.../oauth-protected-resource/<path>`) before the path-appended and
  host-root forms, and the `WWW-Authenticate` challenge probe falls back to
  a POST when the server answers GET with 405. Hosted MCP servers that only
  publish metadata at the path-inserted URL (for example LaunchDarkly's
  `https://mcp.launchdarkly.com/mcp/launchdarkly`) previously failed with
  "could not determine an OAuth strategy for this server" and can now be
  connected via Dynamic Client Registration without a manual authorize URL
  or client id.
- The upstream OAuth callback now marks the connect session **failed** (and
  logs `[mcp-auth] complete_connect failed …`) on every error path, not
  only a rejected token exchange. Previously a failure while loading the
  server, resolving the auth provider, or storing the credential returned a
  bare error page that closed itself, and the Servers UI kept polling a
  session stuck on `pending`, showing "Connecting…" forever. OAuth token
  exchange and refresh requests also get a 30 s timeout instead of sharing
  the untimed upstream HTTP client.
- The MCP proxy now answers JSON-RPC `ping` itself whenever the request
  cannot be forwarded to the upstream (stdio servers, no forwarder, or an
  upstream resolve/transport failure), returning the empty result the MCP
  spec requires instead of `-32601 method not found`. Harnesses that use
  `ping` as a health check (for example the Orchestrator) no longer show
  the Atryum integration as "error" while tools sync and invoke fine.
  HTTP upstreams still see `ping` passed through unchanged.

- The **you** badge on the Users page now sits beside the user's email
  instead of stacking under their name.

### Security

- The shared agent hook no longer writes Atryum API keys (`atr_…`) to its
  on-disk token cache. The key already lives wherever `ATRYUM_TOKEN_COMMAND`
  reads it from, so the cache was a second plaintext copy for no gain. Other
  token kinds are still cached as before.
- `atryum setup claude` and the shared hook refuse to send a bearer token
  over plain `http://` to a non-loopback host. Loopback http stays allowed
  for local development; pass `--allow-insecure-http` to `setup claude`
  (which bakes `ATRYUM_ALLOW_INSECURE_HTTP=1` into the hook commands) or set
  that variable yourself to accept a trusted private network.

### Changed

- How the caller authenticated is recorded on each invocation (migration
  033: `auth_issuer`, `auth_method`, `auth_key_id`) and passed to an
  embedder's decider as `ToolCall.Auth` and event hook as
  `Change.Invocation.Auth`: the issuer, the method (`jwt`, `api_key`,
  `managed_agent` for a session the Managed Agents bridge registered, or
  `no_auth` for a self-declared agent ID) and the API key's or session's id.
  Stuck reports record the reporting caller's.
- The migration runner checks each recorded migration's name as well as its
  number, and refuses to start on a database whose `schema_migrations`
  records a version under another name (one migrated by a build whose
  migrations were numbered differently, such as a development branch
  rebased onto new migrations), with an error that says to recreate it.
  Every released version kept its name, so released databases are
  unaffected.
- **Security:** idempotency keys are now unique per agent (migration 032) and
  bound to the request. Before, a key returned whatever invocation held it,
  so another agent sending the same key received that invocation's status,
  approval and result, and a retry with a different tool or input silently
  got the old call. Now another agent's key never finds that invocation; the
  same agent reusing a key for a different request (path, server, tool or
  input) gets `409 Conflict` (`invocation.ErrIdempotencyConflict`); an
  identical retry still returns the original invocation. Numbers in the
  input compare exactly, and concurrent identical requests make one
  invocation. On PostgreSQL migration 032 builds its index without
  `CONCURRENTLY`, so writes to `invocations` wait during the build; on a
  large table, create `idx_invocations_agent_idempotency_key` concurrently by
  hand first (see the migration).

- Each invocation state change and its `invocation_events` row are now written
  in one transaction, closing the gap where either could commit without the
  other.
- A human-gated MCP call now waits on its database row instead of an in-process
  channel, so an approval or denial recorded by another Atryum process releases
  it. Decisions and the move to executing are compare-and-set, so a call is
  decided and run once.
- With PostgreSQL, a held MCP call wakes as soon as any Atryum process records
  its decision (`LISTEN`/`NOTIFY` on `atryum_invocation_decided`) and polls its
  row every two seconds only as a fallback while the listener is connected
  (250 ms otherwise, and always on SQLite). The listener pings its connection
  after ten quiet seconds and reconnects after errors. It needs a session-mode
  connection: PgBouncer in transaction mode never delivers notifications.
- An executor cancelling a call (`RecordExecution` with `cancelled`) moves the
  row compare-and-set from the status it read, so a decision recorded in
  between is not overwritten.
- The Managed Agents bridge records an approved call as running before it
  tells Claude to run it. If recording is refused (for example by an
  embedder's event hook) or fails, Claude gets a deny with the reason, the
  call is recorded as cancelled, and a warning is logged; before, Claude was
  allowed first and the refusal only debug-logged, so the call ran anyway.

## [0.4.0] - 2026-07-27

### Added

- Agents can now carry free-form tags: editable for every agent (including
  ValidMind-synced ones) from the Agents UI or the `tags` field on the
  agent create/update API, and preserved across re-syncs (tags are
  Atryum-native and are never reset by a sync). Tags are forwarded on every
  AI-judged evaluation as `agent_tags`, laying the groundwork for the
  commercial Global Charters feature; the FOSS charter-preview call
  intentionally continues to omit tags, since it hits a hierarchy-only
  backend endpoint with no concept of organization-wide charters.

### Changed

- **Breaking:** privileged HTTP API paths have been renamed and reorganized
  under a flatter, non-"admin" prefix. Existing integrations, scripts, or
  reverse-proxy rules that reference the old paths must be updated:
  - `/api/v1/admin/invocations...` → `/api/v1/review/invocations...`
    (also gains a `/{id}/summarize` action)
  - `/api/v1/admin/servers...` → `/api/v1/servers...` (also gains a
    `/{name}/tools` action)
  - `/api/v1/admin/rules...` → `/api/v1/rules...`
  - `/api/v1/admin/agents...` → `/api/v1/agents...` (also gains `/sync` and
    `/{id}/charter-preview`)
  - `/api/v1/admin/settings`, `/api/v1/admin/policy` → `/api/v1/settings`,
    `/api/v1/policy`
  - `/api/v1/admin/managed-agents/...` → `/api/v1/managed-agents/...`
    (session management also gains list/clear/delete, not just register)
  - `/api/v1/admin-auth/config` → `/api/v1/auth/config`
  - Plan approval now has its own top-level surface:
    `/api/v1/plans`, `/{id}`, `/{id}/events`, `/{id}/approve`, `/{id}/deny`,
    `/{id}/revise`, `/{id}/expire`, `/stream`
  - New `/api/v1/model-configs`, `/api/v1/llm-configs`,
    `/api/v1/llm-configs/{id}`, and `/api/v1/vm/organizations`,
    `/api/v1/vm/record-types`, `/api/v1/vm/custom-fields` surfaces
  - The "Admin authentication" docs section and terminology are now
    "UI and privileged API authentication"; behavior is unchanged.

### Fixed

- Generated integration docs (`connect-agents.html`) are back in sync with
  their markdown source, which had documented post-logout URL setup and the
  sign-out flow without the HTML being rebuilt.

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

[Unreleased]: https://github.com/validmind/atryum/compare/v0.7.0...HEAD
[0.7.0]: https://github.com/validmind/atryum/compare/v0.6.0...v0.7.0
[0.6.0]: https://github.com/validmind/atryum/compare/v0.5.0...v0.6.0
[0.5.0]: https://github.com/validmind/atryum/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/validmind/atryum/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/validmind/atryum/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/validmind/atryum/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/validmind/atryum/compare/v0.0.4...v0.1.0
[0.0.4]: https://github.com/validmind/atryum/compare/v0.0.3...v0.0.4
[0.0.3]: https://github.com/validmind/atryum/releases/tag/v0.0.3
