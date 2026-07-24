# Atryum architecture

This document describes Atryum's internal boundaries, control flow, and durable state.
For installation, configuration examples, endpoint details, and operator workflows, see
the [README](../README.md).

## System context

Atryum is a Go service that mediates tool calls. It exposes runtime endpoints to agent
harnesses, an MCP-compatible proxy, and an admin API/UI. All ingress paths share the
same invocation service, rule evaluation, and audit store. The stock binary and programs
that embed Atryum both enter through the public `pkg/atryum` bootstrap package.

Two terms recur throughout this document. **Downstream** is the agent or harness side —
the caller waiting on Atryum's response. **Upstream** is the tool-server side — where
Atryum forwards the call to do the actual work. Atryum sits between them: a request flows
agent → Atryum → tool server, and the result (or, for a streamed call, progress updates
along the way) flows back the other direction.

```mermaid
flowchart LR
    Stock[cmd/atryum stock binary]
    Embedder[Embedding Go program]
    Hook[Agent harness or hook]
    MCP[Downstream MCP client]
    Claude[Claude Managed Agents API]
    Admin[Admin UI or API client]

    subgraph Atryum[Atryum process]
        Bootstrap[pkg/atryum bootstrap and extensions]
        API[HTTP and MCP handlers]
        Watcher[Managed Agents watcher]
        Service[Invocation service]
        Rules[Rule evaluation]
        Store[(SQLite or PostgreSQL)]
        MCPClient[Upstream MCP client]
    end

    Stock --> Bootstrap
    Embedder -->|extension options| Bootstrap
    Bootstrap -->|constructs| API
    Hook -->|submit, poll, report outcome| API
    MCP -->|MCP JSON-RPC| API
    Admin -->|review and configuration| API
    Watcher <-->|events and confirmations| Claude
    API --> Service
    Watcher --> Service
    Service --> Rules
    Rules --> Store
    Service --> Store
    Service --> MCPClient
    MCPClient -->|HTTP or stdio| Tools[Upstream MCP servers]
```

The diagram corresponds to `cmd/atryum`, `pkg/atryum`, `pkg/migrations`,
`internal/api`, `internal/managedagents`, `internal/invocation`, `internal/store`, and
`internal/mcp`.

## Runtime ingress

The entry path determines who executes an approved tool. It does not change how rules
or audit records are evaluated.

| Path | Service operation | Executor | Completion source |
|---|---|---|---|
| Pre-tool hook: `POST /api/v1/external/invocations` | `Submit` | Calling harness | Harness reports through `PATCH /api/v1/external/invocations/{id}` |
| MCP proxy: `tools/call` on `/mcp/{server}` | `Invoke` | Atryum | Atryum records the upstream MCP result |
| Direct invocation: `POST /api/v1/invocations` | `Invoke` | Atryum | Atryum records the upstream MCP result |
| Managed Agents bridge | `Submit` and `RecordExecution` | Anthropic runtime | Watcher maps Anthropic events to invocation updates |

`Submit` intentionally leaves `upstream_name` empty because it makes a decision only.
`Invoke` resolves a configured MCP server before evaluating and executing the call.

## Component boundaries

| Package | Responsibility | Must not own |
|---|---|---|
| `cmd/atryum` | Thin stock executable that supplies bundled notices and calls `pkg/atryum` | CLI, server, or business logic |
| `pkg/atryum` | Public CLI/server bootstrap and extension options for routes, migrations, database hooks, and notices | Invocation decisions or transport internals |
| `pkg/migrations` | Portable definitions for Atryum's built-in schema migrations | Repository queries or migration execution state |
| `internal/api` | HTTP/MCP transport, authentication middleware, request/response mapping, embedded UI | Rule or invocation state transitions |
| `internal/invocation` | Invocation lifecycle, rule matching, AI evaluation dispatch, approval coordination | SQL or upstream transport details |
| `internal/store` | SQLite/PostgreSQL repositories, migration execution and tracking, durable query semantics | Policy decisions |
| `internal/mcp` | Server resolution, MCP forwarding, upstream authentication and OAuth | Approval policy |
| `internal/auth` | Inbound OIDC/JWT validation and authenticated identity context | Upstream MCP credentials |
| `internal/managedagents` | Anthropic session discovery, event replay, confirmation delivery | Independent rule evaluation |

The React application in `ui/` is compiled into `internal/api/web/` for the production
binary. During development it can run separately, but it still uses the same admin API.

### Embedding and extension boundary

`cmd/atryum` is only the stock executable. The reusable application lives in
`pkg/atryum`, so another Go program can start the same server with additional options:

- `WithRoutes` mounts extra HTTP routes after built-in routes. These routes are outside
  Atryum's authentication middleware and must provide their own authentication.
- `WithMigrations` registers extension-owned, namespaced schema migrations. They run
  after all built-in migrations and are tracked separately by namespace and version.
- `WithDatabase` runs a hook after built-in and extension migrations finish but before
  the server starts accepting requests.
- `WithThirdPartyNotices` replaces the notices text printed by the `licenses` command.

Extensions execute inside the Atryum process and share its database and HTTP server.
They are trusted application code, not isolated plugins. Route patterns must not collide
with built-in routes, and a migration namespace must remain stable across releases.

## Decision pipeline

Rules are loaded in ascending `rule_order`. Matching uses server/source, tool, and the
resolved local agent record. A local agent record is derived from the authenticated
runtime identity; on the no-auth external path, the submitted `agent_id` is used.

```mermaid
flowchart TD
    Start[Invocation created with status received]
    Match[Find enabled matching rules in rule_order]
    Any{At least one rule matches?}
    Evaluate[Evaluate next matching rule]
    Action{Rule result}
    More{Another matching rule?}
    Policy[Evaluate configured default policy]
    Approve[Auto approve]
    Deny[Auto deny]
    Human[Enter pending_approval]

    Start --> Match --> Any
    Any -->|no| Policy
    Any -->|yes| Evaluate --> Action
    Action -->|approved| Approve
    Action -->|denied| Deny
    Action -->|human approval| Human
    Action -->|next_rule| More
    More -->|yes| Evaluate
    More -->|no| Human
    Policy -->|auto| Approve
    Policy -->|never| Deny
    Policy -->|human or error| Human
```

`next_rule` is produced only by `ai_evaluation`. If every matching AI rule defers,
Atryum falls back to human approval. The global policy provider is used only when no
rule matches; it is not evaluated after a matching rule defers.

An `ai_evaluation` rule selects one configured evaluator. Standalone deployments use
a local LLM configuration stored in `llm_configs`. Evaluation errors escalate to human
review; missing charter context denies; and an unknown verdict is treated as
`next_rule`, eventually reaching human review if no later rule decides.

## Atryum-executed calls

`Invoke` owns the complete execution lifecycle. A human-gated HTTP request remains
blocked on an in-memory channel until an admin decision or until the caller's request
context is cancelled (a client disconnect or a client-side timeout). Atryum imposes no
approval deadline of its own: the configured `request_timeout_seconds` bounds upstream
tool execution, not the human wait. The durable invocation is still visible while the
request is blocked.

Human-approval coordination for Atryum-executed calls is process-local. PostgreSQL
persists the invocation but does not provide distributed signaling for the in-memory
waiter (the blocked request goroutine), so these calls require a single active Atryum
process. Concretely:

- With multiple replicas, an approval handled by a different process updates the row
  without waking the original request; when that request's context is later cancelled,
  it overwrites the approved state as failed.
- Caller cancellation marks the invocation failed. The stored error text says
  "cancelled", but the persisted status is `failed`, not `cancelled`.
- An abrupt process exit drops the waiter and caller while the durable invocation can
  remain `pending_approval`. Nothing resumes pending invocations at startup, so a
  later approval updates that row but does not execute the tool.

Multi-replica or crash-resumable execution requires durable coordination — for
example a work queue, or an outbox table that a worker drains — which is not
implemented.

```mermaid
sequenceDiagram
    autonumber
    participant Caller
    participant API as API or MCP handler
    participant Service as invocation.Service
    participant Store as Invocation and event stores
    participant Admin as Admin API or UI
    participant MCP as Upstream MCP server

    Caller->>API: Invoke tool
    API->>Service: Invoke(request)
    Service->>Store: Create invocation with status received
    Service->>Service: Evaluate rules or default policy
    Service->>Store: Append rule-evaluation and received audit events
    alt auto denied
        Service->>Store: Persist denied status and event
        Service-->>Caller: Denial
    else auto approved
        Service->>Store: Persist executing status and event
        Service->>MCP: Execute tool
        MCP-->>Service: Result or error
        Service->>Store: Persist terminal status and event
        Service-->>Caller: Result
    else human approval
        Service->>Store: Persist pending_approval status and event
        Note over Service: Wait on pendingApprovals invocation channel
        Admin->>API: Approve or deny
        API->>Service: Approve or Deny
        Service->>Service: Deliver decision to waiter
        alt approved
            Service->>Store: Persist approved and executing events
            Service->>MCP: Execute tool
            MCP-->>Service: Result or error
            Service->>Store: Persist terminal status and event
        else denied
            Service->>Store: Persist denied status and event
        end
        Service-->>Caller: Result or denial
    end
```

The admin invocation stream is a polling SSE view: the handler queries the durable
invocation list every two seconds and emits when its signature changes. Database writes
do not directly publish to the stream.

### Live SSE relay for tools/call

#### Purpose and scope

Atryum normally returns one response after an upstream tool finishes. The live relay
also lets Atryum send progress updates while the tool is still running.

This behavior applies only to `tools/call` requests sent through the MCP proxy at
`/mcp/{server}`. It does not apply to the direct REST endpoint
`POST /api/v1/invocations`.

Streaming is opt-in and keeps the old behavior as its fallback:

1. The downstream MCP client includes `Accept: text/event-stream` to say it can read
   Server-Sent Events.
2. Atryum calls the upstream MCP server.
3. If the upstream returns a normal JSON response, Atryum returns one normal JSON
   response.
4. If the upstream starts a stream, Atryum relays each progress update and then the
   final result.

#### Terms used in this section

| Term | Meaning here |
|---|---|
| **MCP** | Model Context Protocol, the protocol used to call tools. MCP messages use JSON-RPC. |
| **Downstream MCP client** | The agent or agent harness calling Atryum. “Downstream” means the side receiving Atryum's response. |
| **Atryum** | The relay between the downstream client and the upstream server. |
| **Upstream MCP server** | The tool server that Atryum calls. “Upstream” means the side doing the tool work. |
| **JSON-RPC request** | A message asking for work. It has an `id`, and the response must carry the same `id`. |
| **JSON-RPC notification** | A one-way message such as a progress update. It has a `method` but no `id`, so no reply is expected. |
| **Terminal response** | The final JSON-RPC success or error for the tool call. It is the one result a non-streaming call would return. |
| **Terminal delivery** | The final write from Atryum to the downstream client. It can fail even after the upstream result was saved successfully. |
| **SSE** | Server-Sent Events, a one-way HTTP response format that lets a server send multiple events over one open response. |
| **Progress token** | A string or number used to match progress to a call. The client requests it with `_meta.progressToken`; notifications return it as `params.progressToken`. |
| **MCP session** | A group of requests recognized by the upstream server through one session ID. Atryum shares that upstream session across active calls. |
| **Call-response stream** | The response body of the upstream `tools/call` POST. It belongs to one call. |
| **Standalone stream** | A separate SSE GET connection shared by active calls in one upstream MCP session. Some MCP servers send progress here instead of on the call-response stream. |
| **Stdio upstream** | An MCP server run as a local process. Atryum exchanges messages through the process's standard input and output instead of HTTP. |

An SSE event contains one or more `data:` lines and ends with a blank line. For example:

```
data: {"jsonrpc":"2.0","method":"notifications/progress","params":{"progress":1,"total":3}}

data: {"jsonrpc":"2.0","id":"1","result":{"content":[{"type":"text","text":"done"}]}}
```

The first event is a progress notification. The second event is the terminal response.

#### The two upstream paths

Every HTTP tool call has its own POST response. Some upstream servers put both progress
and the terminal response on that response. Other servers put progress on a separate,
shared GET stream while still returning the terminal response on the POST response.
Atryum listens to both paths.

```mermaid
flowchart TB
    Downstream[Downstream MCP client]
    Relay[Atryum]
    Upstream[Upstream MCP server]
    CallPath[Path A: one POST per call<br/>Progress and terminal response]
    StandalonePath[Path B: one shared SSE GET<br/>Progress only]

    Downstream <-->|tools/call request<br/>JSON or SSE response| Relay
    Relay --- CallPath
    CallPath --- Upstream
    Relay --- StandalonePath
    StandalonePath --- Upstream
```

| | Path A: call-response stream | Path B: standalone stream |
|---|---|---|
| HTTP connection | The response to one `tools/call` POST | One SSE GET shared by active calls in an upstream session |
| Carries progress | Yes | Yes |
| Carries the terminal response | Yes | No; the terminal response still arrives on the POST response |
| How an update is matched to a call | The response already belongs to that call | Atryum matches `params.progressToken` |

For a stdio upstream there is no HTTP or SSE on the upstream side. The process sends one
JSON-RPC message per line instead. Atryum applies the same message classification,
timeout, audit, and downstream-relay rules to those messages.

#### Normal call flow

```mermaid
sequenceDiagram
    autonumber
    participant Client as Downstream MCP client
    participant Atryum
    participant Upstream as Upstream MCP server

    Client->>Atryum: tools/call, accepts SSE
    Atryum->>Atryum: Evaluate rules and approval policy
    Atryum->>Upstream: Execute the tool
    alt Upstream returns one normal response
        Upstream-->>Atryum: Terminal JSON-RPC response
        Atryum-->>Client: One normal JSON response
    else Upstream streams
        loop For each progress notification, if any
            Upstream-->>Atryum: Progress notification on Path A or B
            Atryum->>Atryum: Queue audit write
            Atryum-->>Client: Progress notification as SSE
        end
        Upstream-->>Atryum: Terminal response on Path A
        Atryum->>Atryum: Save final invocation state
        Atryum-->>Client: Terminal response as SSE, then close
        Atryum->>Atryum: Record whether terminal delivery succeeded
    end
```

Approval happens before Atryum contacts the upstream server. While waiting for a human
decision, the downstream request remains open but Atryum has not started an SSE
response. A denial stays a normal JSON response. After approval, execution follows the
flow above.

#### Matching shared progress to the correct call

The standalone stream is shared, so receiving an event does not by itself identify the
call that owns it. Atryum uses this process:

1. The downstream client supplies `_meta.progressToken`.
2. Before sending the tool call upstream, Atryum replaces that token with a value unique
   to this call.
3. On the standalone path, Atryum uses its unique value to select the correct call. On
   the call-response path, the HTTP response already identifies the call.
4. Before relaying progress from either path, Atryum restores the client's original token.

Rewriting is necessary because two unrelated clients can choose the same token. Without
it, one client could receive another client's progress.

A standalone notification without a progress token cannot always be matched safely. If
exactly one call is currently using the shared stream, Atryum sends the notification to
that call. If several calls are active, Atryum drops it rather than guess and risk
cross-delivery.

#### Code responsibilities

| Layer | Responsibility for this feature |
|---|---|
| `pkg/atryum` (startup only) | Load stream configuration, inject timeout and audit limits into the invocation service, and set the relay kill switch on the HTTP handler. |
| `internal/api` | Detect downstream SSE support, write and flush SSE frames, send heartbeats, enforce downstream write deadlines, and finish the stream. |
| `internal/invocation` | Preserve rule and approval behavior, update invocation state, and audit stream events through a bounded shared dispatcher. |
| `internal/mcp` | Call the upstream, parse and classify JSON-RPC messages, merge the two upstream paths, correlate progress tokens, reconnect resumable streams, and enforce upstream timeouts. |

After startup wiring, each call crosses the runtime packages in this order:
`internal/api` → `internal/invocation` → `internal/mcp`.

#### Control flow in pseudocode

The table above says which package owns what; this section walks the same call in
the order it actually runs, in pseudocode, with the reason each step exists. Nothing
here is transport-specific until the HTTP/stdio split.

**1. Deciding whether to stream at all (`internal/api`)**

```
handle tools/call request:
    if client sent "Accept: text/event-stream" and the relay is not killed-switched:
        sink = new SSE sink wrapping this response
        # Constructing the sink does not commit the response to SSE. Only the
        # upstream actually starting a stream (below) does that. This is what
        # keeps a plain-JSON upstream response possible right up until the
        # last moment.
    else:
        sink = nil

    result = invocationService.InvokeStreaming(request, sink)

    if sink was actually started:
        # Headers are already committed as SSE. From here the only spec-legal
        # way to end the exchange is one more SSE frame carrying the result
        # (or a JSON-RPC error) — never a second status line, never a bare
        # close.
        write result as a final SSE event, stop the heartbeat, close
    else:
        # Either there was no sink, or one existed but the upstream never
        # opened a stream (a plain JSON tool). Nothing has reached the wire
        # yet, so the ordinary response is still available.
        write result as one normal JSON response
```

**2. Rule and approval evaluation stay sink-agnostic (`internal/invocation`)**

```
InvokeStreaming(request, sink):
    resolve the upstream server
    create the durable invocation row (status = received)
    evaluate rules, or the default policy if none match      # identical to the buffered call
    record the decision as an audit event

    switch decision:
        auto_denied    -> persist denied, return              # sink never touched
        auto_approved  -> finishExecution(invocation, sink)
        human_approval ->
            persist pending_approval
            block on an in-memory channel until an admin decides
            # sink stays untouched for as long as this blocks: nothing streams
            # while a call is waiting on a human, matching the plain
            # Atryum-executed-call flow above.
            if approved: finishExecution(invocation, sink)
            else: persist denied, return

finishExecution(invocation, sink):
    if sink == nil:
        finishExecutionBuffered(invocation)         # today's non-streaming path, unchanged
    else:
        finishExecutionStreaming(invocation, sink)
```

Threading `sink` this deep exists so the decision pipeline never has to know streaming
exists at all — it is the same code, gated only by whether `sink` happens to be `nil`.

**3. Wrapping the sink for audit before it reaches the transport (`internal/invocation`)**

```
finishExecutionStreaming(invocation, sink):
    audited = decorate(sink) so every relayed event is durably recorded as it passes through
              # A decorator, not a fork: it never skips forwarding to the real
              # sink to make its own recording succeed, and it records the
              # event even if forwarding then fails downstream — the audit
              # trail must reflect what the upstream actually sent, not just
              # what the agent actually received.
    result, err = mcpClient.InvokeStream(upstream, tool, input, audited, streamOptions)

    if err:
        reason = classify(err):
            the audited sink saw its own forwarding call fail  -> "the agent connection died"
            our own guard's context was the one that cancelled -> "we gave up (timeout)"
            anything else                                      -> "the transport itself failed"
        persist invocation as failed, with that reason on the audit row
    else:
        persist invocation as succeeded or failed, from the upstream's own result

    audited.finish(outcome)   # one summary audit row: events seen vs. persisted vs. dropped
    return response
```

Classifying the error exists so an operator reading the audit trail can tell "the agent
hung up" apart from "our own bound fired" apart from "the upstream broke" — otherwise
every one of those looks like the same generic transport error.

**4. Choosing a transport (`internal/mcp`)**

```
InvokeStream(upstream, tool, input, sink, opts):
    if sink == nil: return Invoke(...)          # buffered call, unchanged
    if upstream is stdio: return invokeStdioStream(...)
    else:                 return invokeHTTPStream(...)
```

**5. HTTP: the two-path merge — the core of the feature**

```
invokeHTTPStream(upstream, tool, input, sink, opts):
    ensure the upstream HTTP session is initialized

    if the caller supplied a progressToken:
        wireToken = mint a value unique to this call; remember the caller's original token
        # Atryum multiplexes every concurrent caller of one upstream onto a
        # single shared session. If two unrelated callers happened to choose
        # the same progressToken, forwarding it unchanged would let one
        # caller's progress leak into another's stream. A per-call wire token
        # makes routing on the standalone path (below) unambiguous, and the
        # original token is restored before anything is relayed downstream —
        # the caller never sees Atryum's internal value.

        standalone = acquire the shared standalone-stream connection for this upstream session
                     # Lazily created: the first concurrent caller opens it;
                     # later ones just add themselves as listeners on the one
                     # connection already running.
        register this call as a listener for wireToken
        wait for the standalone connection's first connect attempt to resolve
                     # Acquiring only starts a background connection attempt —
                     # it does not wait for it. If the request below reached
                     # the upstream and produced its first progress
                     # notification before this GET had actually subscribed,
                     # that notification would have nowhere to land and would
                     # be silently lost. This wait closes that race. It is
                     # bounded by the header timeout, so a hung upstream
                     # degrades to "no standalone progress for this call"
                     # rather than blocking it forever.

    send the tools/call request upstream, carrying wireToken in place of the caller's token

    if the response is plain JSON:
        return it directly                       # nothing below this line runs
    else:                                          # the response is SSE
        start a background reader for this call's own POST response (Path A, below)
        loop:                                       # the merge loop
            select whichever arrives first:
                a message from the standalone stream, already routed to this call (Path B)
                a message from this call's own POST-response reader (Path A)

                on a progress/notification, from either path:
                    restore the caller's original progressToken
                    hand it to the audited sink (recorded, then relayed to the agent)

                on the terminal response (only ever arrives via Path A):
                    briefly keep draining any standalone progress already in flight
                    # Progress and the terminal response travel on
                    # independent connections, so one can race past the
                    # other. A short settle window lets a near-simultaneous
                    # progress notification still arrive before the call is
                    # considered finished, instead of losing it by a few
                    # milliseconds.
                    unregister this call's standalone listener
                    return the terminal result
```

Path A — this call's own reader, the only one of the two paths that can resume:

```
Path A reader (its own goroutine, one per call):
    loop:
        read the next SSE event from the POST response
        if the connection drops:
            if no event carrying an id was ever seen: give up      # nothing to resume from
            else:
                wait a bounded, jittered backoff
                reconnect with "Last-Event-ID: <last id seen>"
                # The Streamable HTTP transport lets a server end this
                # response early and expects the client to resume from where
                # it left off, rather than losing everything already sent or
                # replaying the call from scratch.
                if the server inclusively replays that same id, skip the one duplicate
                keep reading from the resumed connection
        else:
            forward the event into the merge loop above
```

Path B — the standalone stream, shared by every concurrent call on the session:

```
Standalone reader (one shared goroutine per upstream session):
    loop:
        open a GET SSE connection to the upstream (no Last-Event-ID — this
        isn't resuming a specific call, just listening)
        signal "first connect attempt resolved"     # what invokeHTTPStream waits on above
        loop:
            read the next SSE event
            if it carries a progressToken: hand it to whichever registered call owns that token
            else if exactly one call is currently listening: hand it to that call
                # With only one candidate, attributing an untokened message is
                # safe. With several in flight, it would be a guess — and
                # guessing wrong leaks one caller's message to another, which
                # is worse than dropping it.
            else: drop it                            # ambiguous; safety over completeness
        on disconnect: reconnect with backoff          # no replay cursor on this path —
                                                        # messages sent while disconnected can be missed
```

**6. stdio: a simpler single-reader path, no shared connections**

```
invokeStdioStream(upstream, tool, input, sink, opts):
    start the upstream as a child process, wired to its stdin/stdout
    run the initialize handshake, bounded by the header timeout
    send the tools/call request as one JSON-RPC line

    loop reading newline-delimited JSON-RPC messages:
        on a notification or a server-to-client request: hand it to the sink
        on the terminal response for this call: return it

    # Always kill the whole process group on the way out, even after a
    # timeout — killing only the direct child can leave a spawned
    # grandchild process running forever.
```

**7. Enforcing timeouts across both transports (`internal/mcp`)**

```
callTimeoutGuard:
    one cancellable context shared by the whole call

    arm a setup timer covering "waiting on response headers" / "the initialize handshake"
    once headers or the handshake are in: disarm the setup timer, arm two more:
        idle timer   — reset on every relayed message, including bare SSE
                       keepalive/comment lines
                       # A tool that is slow but alive must not be punished
                       # for going quiet between progress updates, as long as
                       # something keeps proving the connection is alive.
        max-duration timer — never reset, a hard ceiling regardless of activity

    whichever timer fires first cancels the shared context and records why,
    so the caller can tell "we gave up" apart from "the transport failed on its own"
```

The idle timer specifically re-derives elapsed time from a recorded timestamp rather
than trusting that its callback firing means the call is genuinely idle: resetting a
timer concurrently with its own callback is a documented race, so the callback instead
checks real elapsed time and re-arms itself if it turns out to have fired early.

**8. Auditing without blocking delivery (`internal/invocation`)**

```
recording one relayed event:
    hand it to a fixed pool of shared background workers, sharded by call
    # Not one goroutine per call: that design lets a stalled audit store leak
    # one goroutine per concurrent streaming call. A fixed shared pool bounds
    # that no matter how many calls are in flight — at the cost of the queue
    # filling under sustained overload, which is reported, not hidden.
    if the shard's queue is full: count it as dropped, do not block the relay
    # Audit persistence must never slow down or stall live delivery to the
    # agent. A full queue means "keep serving live traffic; this one row will
    # not be written," and that trade is made visible, not silent.

when the call ends:
    wait briefly for this call's in-flight audit writes to finish
    write one summary row: events seen / persisted / dropped / whether the wait itself timed out
    # Per-event rows can be capped or dropped under load or by configuration;
    # the summary row is the one place an operator can trust to see the true
    # totals even when individual event rows are missing.
```

**9. Delivering to the agent, and auditing that delivery separately (`internal/api`)**

```
sink.StreamStarted():
    commit to SSE: write status 200 and SSE headers, flush
    start a background heartbeat loop
    # Intermediaries (proxies, load balancers) commonly kill connections
    # after ~60s of silence. A tool that is busy but has nothing to report
    # yet would otherwise have its downstream leg cut for reasons that have
    # nothing to do with the tool call itself.

sink.Event(evt):
    if it is a server-to-client request (sampling, elicitation, roots): drop it
        # Atryum doesn't advertise those capabilities in initialize, and the
        # agent has no channel to answer a request arriving on what it
        # expects to be a tools/call response stream. Audited, never relayed.
    else: write one SSE frame, one "data:" line per line of the payload
        # A raw newline embedded in a single "data:" line breaks SSE framing
        # for any compliant parser.

sink.finishStream(terminal):
    stop the heartbeat first
    # Otherwise the heartbeat goroutine could still be writing to the
    # response after the handler has already returned.
    write the terminal frame as the last SSE event, using the agent's own
        request id — never the fixed id Atryum sends upstream on the wire

after the handler returns:
    record whether that terminal write actually succeeded, as its own audit row,
    separate from the invocation's succeeded/failed outcome
    # The upstream tool call can succeed even though the agent never received
    # the terminal frame (e.g. it disconnected moments earlier). Conflating
    # the two would misreport a delivery failure as a tool failure, or the
    # reverse.
```

#### Resource limits

A single timeout is not enough for a stream. A long-running tool can be healthy as long
as it continues to send progress. The relay therefore separates setup, inactivity,
total-duration, and message-size limits:

| Limit | Configuration | What it measures |
|---|---|---|
| Header timeout | `stream_header_timeout_seconds` | How long Atryum waits for HTTP session initialization and response headers, or stdio session initialization. |
| Idle timeout | `stream_idle_timeout_seconds` | The longest allowed gap in upstream activity. It resets after every message, and after SSE keepalive/comment lines, so a busy-but-quiet upstream that heartbeats is not cut off. |
| Maximum duration | `stream_max_duration_seconds` | A hard limit for the upstream execution phase, even if progress continues. |
| Message size | `stream_max_message_bytes` | The largest accepted SSE event, stdio JSON-RPC line, or plain JSON response body. The default is 4 MiB. |

The downstream connection has a separate per-write deadline. If the downstream client
disconnects or stops reading, Atryum aborts that call instead of leaving a goroutine
blocked forever. While the upstream is quiet, Atryum sends SSE comment heartbeats so
proxies and load balancers do not mistake the downstream connection for an abandoned
one. The audit trail distinguishes an upstream timeout (`stream_timeout`), a proven
downstream disconnect (`stream_aborted_downstream`, set only when a write to the agent
actually failed), and other transport failures. A bare request-context cancellation is
recorded as `stream_canceled`: with no failed downstream write, a quiet agent
disconnect and a server shutdown are indistinguishable, so the audit does not guess.

#### Reliability guarantees and limits

- **Plain JSON remains the fallback.** Atryum does not start the downstream SSE response
  unless the client accepts SSE and the upstream sends an SSE response or a stdio
  intermediate message.
- **Approval still comes first.** No upstream call or downstream stream starts while a
  tool call is waiting for approval.
- **Retry is safe only before delivery.** Atryum may retry session setup before it has
  relayed anything. After the first relayed event, retrying could duplicate visible
  progress, so Atryum returns a terminal stream error instead.
- **A started stream gets a terminal frame.** If execution fails after streaming has
  begun, Atryum sends a final JSON-RPC error event instead of silently closing the
  connection or trying to change the HTTP status.
- **The per-call upstream stream can resume.** If it closes after providing an SSE event
  ID but before the terminal response, Atryum reconnects with a `Last-Event-ID` header
  naming the last processed event. If the upstream inclusively replays that event,
  Atryum skips the duplicate. Reconnects use bounded exponential backoff with jitter;
  the upstream cannot cause a zero-delay retry loop.
- **The standalone stream reconnects but does not replay.** A transient connection
  failure is retried with backoff. Session renewal opens a new standalone GET carrying
  the new session ID. Because this path has no replay cursor, messages sent while it was
  disconnected may still be missed.
- **The downstream stream does not resume.** Atryum intentionally sends no downstream
  SSE event IDs because it does not store each client's last processed position across
  restarts.
- **Audit storage cannot stall delivery.** Each intermediate event is offered to a
  process-wide bounded, sharded dispatcher served by a fixed worker pool. A call does
  not create its own audit goroutine, and one call's events remain ordered. Slow or
  failed writes are counted in the completion audit row; they do not delay the live
  event.
- **Audit volume is bounded.** `stream_audit_max_events` limits rows per call, and
  `stream_audit_max_event_bytes` limits the retained bytes in each row. Events beyond
  those storage limits are still relayed.
- **A slow call cannot block the shared standalone stream.** Each call has a bounded
  progress buffer. If it fills, Atryum drops that call's standalone progress event so
  other calls can continue. `standalone_events_dropped` in
  `invocation.stream_completed` makes that loss visible.
- **Unsupported standalone GET is not fatal.** If an upstream does not provide this
  optional path, Atryum stops trying while the current group of calls remains active.
  The tool call and its POST response continue normally.
- **Input memory is bounded.** Atryum rejects an upstream message above
  `stream_max_message_bytes` instead of accumulating an arbitrarily large SSE event,
  stdio line, or plain JSON body in memory.
- **Execution and delivery are audited separately.** A durable succeeded/failed
  invocation describes the upstream outcome. `invocation.stream_delivery` separately
  records whether the terminal SSE frame reached the downstream connection. A
  persistence failure is recorded as `persistence_failed`, never as a successful stream.
- **Multi-line SSE data stays valid.** Atryum writes one `data:` field for every payload
  line and terminates the event with a blank line.
- **Upstream requests are not forwarded as notifications.** Server-to-client requests
  such as sampling or elicitation require a response channel Atryum does not broker.
  Atryum audits and drops them instead.
- **The relay has a kill switch.** Setting `stream_relay_enabled = false` restores
  buffered, single-response behavior for every `tools/call`.

#### Advanced implementation notes

These details matter when changing the implementation but are not needed to understand
the normal flow:

- An HTTP response becomes committed when Atryum writes its headers or first bytes.
  Before that point Atryum can still return plain JSON. After that point every success or
  error must be a final SSE frame; the handler cannot switch response formats.
- Heartbeats and tool events can be produced by different Go lightweight threads
  (goroutines). A lock serializes downstream writes so two frames can never be
  interleaved and corrupted.
- Resetting an idle timer races with the timer callback if implemented naively. The
  timeout guard checks the actual elapsed idle time before ending the call.
- A timed-out stdio tool can leave child processes behind. On operating systems that
  support process groups, Atryum terminates the entire group rather than only the direct
  child.

## Decision-only calls

`Submit` persists and returns a decision without contacting an MCP server. An external
executor polls a pending invocation, runs the tool only after approval, and reports its
execution state.

```mermaid
stateDiagram-v2
    [*] --> received: Submit
    received --> approved: automatic approval
    received --> denied: automatic denial
    received --> pending_approval: human decision required
    pending_approval --> approved: admin approves
    pending_approval --> denied: admin denies
    approved --> executing: PATCH running
    executing --> executing: PATCH running
    approved --> succeeded: PATCH completed
    approved --> failed: PATCH failed
    approved --> cancelled: PATCH cancelled
    executing --> succeeded: PATCH completed
    executing --> failed: PATCH failed
    executing --> cancelled: PATCH cancelled
```

The state diagram shows the supported caller contract, not enforced transitions.
`RecordExecution` currently validates only the transition to `running`; the terminal
reports (`completed`, `failed`, `cancelled`) are applied without checking the prior
status, so callers must not report a terminal outcome before approval. When inbound auth supplies an agent identity, the service also checks that
the invocation belongs to that agent. In no-auth mode, ownership cannot be verified.

## Managed Agents bridge

Each configured account can run watchers for linked Anthropic sessions. A watcher
replays events from its persisted cursor, follows the live SSE stream, and reconnects
after failures. Tool calls still use `Submit`, so this bridge does not introduce a
second policy engine.

```mermaid
sequenceDiagram
    autonumber
    participant Watcher
    participant Anthropic as Managed Agents API
    participant Service as invocation.Service
    participant Store

    Watcher->>Anthropic: List events after persisted cursor
    Anthropic-->>Watcher: Backlog
    Watcher->>Anthropic: Open live event stream
    Anthropic-->>Watcher: tool_use
    Watcher->>Store: Append raw session audit event
    Watcher->>Service: Submit external invocation
    Service->>Store: Persist decision
    Anthropic-->>Watcher: status_idle with requires_action
    loop until decided
        Watcher->>Service: Get invocation
        Service-->>Watcher: Current status
    end
    Watcher->>Anthropic: Send allow or deny event
    opt allowed
        Watcher->>Service: RecordExecution running
        Anthropic-->>Watcher: tool_result
        Watcher->>Service: RecordExecution completed or failed
    end
    Note over Watcher,Store: Each successfully handled event advances the persisted cursor
```

Tool-use submission is idempotent across replay because the Anthropic event ID is used
as the invocation idempotency key. Cursor advancement waits until the event handler has
completed successfully enough to avoid skipping a confirmation that must be retried.

## Durable model

The core tables are:

| Table | Architectural role |
|---|---|
| `invocations` | Current state and request/result material for each governed call |
| `invocation_events` | Ordered, best-effort event history for an invocation |
| `approval_rules` | Ordered match criteria and decision action |
| `agents` | Mapping from runtime identities to named governance records |
| `mcp_servers` | Runtime upstream definitions and connection state |
| `oauth_credentials` and `oauth_connect_sessions` | Upstream OAuth credentials and browser-flow state |
| `llm_configs` | Local AI-evaluation providers |
| `managed_agent_bindings`, `managed_agent_sessions` | Anthropic agent/session ownership and replay state |
| `external_sessions` | Atryum-minted harness sessions linking external invocations for cross-call evaluation context |

Built-in schema definitions are ordered migrations under `pkg/migrations/`.
`internal/store` applies them at startup and records their versions for both SQLite and
PostgreSQL. Embedding programs can register separately tracked, namespaced migrations
through `pkg/atryum.WithMigrations`; these run after all built-in migrations.

An invocation's row is the authoritative record of current state;
`invocation_events` is best-effort event history. The two writes are separate
statements, not one transaction, so parity can fail in either direction: a status
update can commit while its event append fails, and — where code appends the event
before updating the row — an event can exist for a status update that never
committed. Event-append errors are currently discarded without a log line. Code that
adds a transition must attempt to update both representations, but consumers must not
reconstruct current state solely from events or assume complete parity. Deployments
that require a transactionally complete compliance audit need to wrap both writes in
one transaction (or adopt an outbox design) before treating the event history as
such.

## Configuration and ownership

`atryum.toml` owns process bootstrap concerns: listening, database selection, inbound
auth, optional external service credentials, default policy, and initial upstreams.
After the first successful bootstrap, MCP server definitions, rules, agents, evaluator
settings, and connection state are database-owned and managed through the admin API.

This separation prevents runtime changes from being overwritten by a restart. In
particular, `[[upstreams]]` seeds an empty `mcp_servers` table; it is not continuous
configuration reconciliation.

## Authentication boundaries

Inbound and upstream authentication are separate trust boundaries:

- Agent runtime auth validates bearer tokens and places the configured agent identity
  claim in request context. The invocation service uses that trusted identity for rule
  targeting and ownership checks.
- Admin auth protects the UI and admin API when an auth provider has
  `admin_enabled = true` and the configured admin claim is present.
- Upstream MCP authentication is owned by `internal/mcp/auth_provider`; credentials and
  OAuth tokens are never returned to the agent caller.
- Routes registered through `pkg/atryum.WithRoutes` are deliberately outside Atryum's
  built-in authentication middleware. The embedding program must authenticate and
  authorize those routes itself.
- No-auth mode is a local deployment option. Identity supplied by a caller in this mode
  is attribution, not a cryptographic ownership guarantee.
