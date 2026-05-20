#!/usr/bin/env python3
"""
fake_agent.py — pretend to be an agent talking to Atryum, so you can test
the approval UI / invocation pipeline without running a real coding harness
or MCP client.

Two modes, mirroring the two real integrations Atryum supports today:

  1. harness  — pretend to be a coding harness like Amp using the
                /api/v1/external/invocations endpoints (the amp-plugin path,
                see atryum/examples/amp-plugin/). Atryum does NOT execute
                the tool; we (the fake harness) "execute" it locally and
                PATCH the result back.

  2. mcp      — pretend to be an MCP client talking JSON-RPC to
                POST /mcp/{server}. Goes through initialize / tools/list /
                tools/call and lets Atryum proxy to the upstream + mediate
                approval.

  3. demo     — runs a small scripted scenario over the harness path so you
                can click through the UI and approve/deny things.

Examples
--------

  # Submit one tool call as a fake "amp" harness and wait for approval:
  python fake_agent.py harness --tool Bash --input '{"cmd":"ls"}'

  # Submit one and auto-report a failure once approved:
  python fake_agent.py harness --tool Bash --input '{"cmd":"boom"}' \\
      --simulate-result fail

  # Drive a scripted demo so you can play with /ui/:
  python fake_agent.py demo

  # Talk MCP JSON-RPC to the default safe server (calc-mcp):
  python fake_agent.py mcp --list-tools
  python fake_agent.py mcp --tool add --arguments '{"a":2,"b":3}'

  # Pretend to be a specific harness:
  python fake_agent.py mcp --client-name cursor --client-version 0.45.7 --list-tools

  # Aggregate /mcp/ endpoint (every server's tools merged):
  python fake_agent.py mcp '' --list-tools

Config (env or flags):
  ATRYUM_URL        base url, default http://localhost:8080
  ATRYUM_MCP_SERVER default MCP server name, default "calc-mcp"
  ATRYUM_SOURCE     pins the agent identity (harness --source / mcp clientInfo.name);
                    when unset, a random identity is picked from a small set of
                    realistic harness names (amp, cursor, claude-code, …)
  ATRYUM_POLL_MS    poll interval ms while awaiting approval, default 1000
  THREAD_ID         thread id surfaced in harness submissions
"""

from __future__ import annotations

import argparse
import json
import os
import random
import sys
import time
import uuid
from typing import Any
from urllib import error as urlerror
from urllib import request as urlrequest

DEFAULT_URL = os.environ.get("ATRYUM_URL", "http://localhost:8080")
DEFAULT_POLL_MS = int(os.environ.get("ATRYUM_POLL_MS", "1000"))
DEFAULT_THREAD_ID = os.environ.get("THREAD_ID", "")

# Default MCP server to talk to. calc-mcp is a safe local sandbox; the real
# upstreams (shortcut, github, etc.) are world-impacting so we don't want a
# bare `mcp` invocation to land there by accident.
DEFAULT_MCP_SERVER = os.environ.get("ATRYUM_MCP_SERVER", "calc-mcp")

# Reasonable real-world agent/harness identities. Used as:
#   - harness mode: --source (shows up as "upstream" column in atryum UI)
#   - mcp mode:     clientInfo.name in the initialize handshake
# Pick one at random per run so the UI shows a mix of agents instead of a
# single monoculture "fake-agent" stream.
AGENT_IDENTITIES: list[tuple[str, str]] = [
    ("amp", "0.0.1759000000-g00000a"),
    ("cursor", "0.45.7"),
    ("claude-code", "1.0.32"),
    ("cline", "3.1.0"),
    ("windsurf", "1.2.4"),
    ("continue", "0.9.250"),
    ("zed", "0.156.0"),
    ("opencode", "1.14.48"),
    ("aider", "0.85.0"),
    ("goose", "1.0.20"),
]


def _env_or_random_identity() -> tuple[str, str]:
    """Return (name, version). Honors ATRYUM_SOURCE if set, else picks randomly."""
    override = os.environ.get("ATRYUM_SOURCE", "").strip()
    if override:
        return override, "0.0.1"
    return random.choice(AGENT_IDENTITIES)


# ─── tiny HTTP helper (stdlib only, no requests dep) ────────────────────────


def _request(
    method: str,
    url: str,
    body: Any | None = None,
    headers: dict[str, str] | None = None,
    timeout: float = 30.0,
) -> tuple[int, dict[str, Any] | str]:
    data = None
    hdrs = dict(headers or {})
    if body is not None:
        data = json.dumps(body).encode("utf-8")
        hdrs.setdefault("Content-Type", "application/json")
    req = urlrequest.Request(url, data=data, method=method, headers=hdrs)
    try:
        with urlrequest.urlopen(req, timeout=timeout) as resp:
            raw = resp.read().decode("utf-8")
            status = resp.status
    except urlerror.HTTPError as e:
        raw = e.read().decode("utf-8", errors="replace")
        status = e.code
    try:
        return status, json.loads(raw) if raw else {}
    except json.JSONDecodeError:
        return status, raw


def _print_json(label: str, payload: Any) -> None:
    print(f"── {label} " + "─" * max(2, 60 - len(label)))
    if isinstance(payload, (dict, list)):
        print(json.dumps(payload, indent=2))
    else:
        print(payload)


# ─── harness mode (external invocations) ────────────────────────────────────


PENDING_STATES = {"received", "pending_approval", "executing"}
APPROVED_STATES = {"approved"}
REJECTED_STATES = {"denied", "expired", "cancelled"}


def harness_submit(
    base: str,
    source: str,
    tool: str,
    input_obj: dict[str, Any],
    description: str | None,
    request_id: str,
    thread_id: str,
) -> dict[str, Any]:
    body: dict[str, Any] = {
        "source": source,
        "tool": tool,
        "input": input_obj,
        "request_id": request_id,
    }
    if description:
        body["description"] = description
    if thread_id:
        body["thread_id"] = thread_id
    status, payload = _request("POST", f"{base}/api/v1/external/invocations", body)
    if status >= 300:
        raise SystemExit(f"submit failed ({status}): {payload}")
    assert isinstance(payload, dict)
    return payload


def harness_poll(base: str, invocation_id: str, poll_ms: int) -> dict[str, Any]:
    url = f"{base}/api/v1/external/invocations/{invocation_id}"
    while True:
        status, payload = _request("GET", url)
        if status >= 300:
            raise SystemExit(f"poll failed ({status}): {payload}")
        assert isinstance(payload, dict)
        cur = payload.get("status")
        if cur not in PENDING_STATES:
            return payload
        print(f"  …status={cur}, waiting {poll_ms}ms")
        time.sleep(poll_ms / 1000)


def harness_patch(
    base: str,
    invocation_id: str,
    execution_status: str,
    result: Any = None,
    error: Any = None,
    message: str | None = None,
) -> dict[str, Any]:
    body: dict[str, Any] = {"execution_status": execution_status}
    if result is not None:
        body["result"] = result
    if error is not None:
        body["error"] = error
    if message:
        body["message"] = message
    status, payload = _request(
        "PATCH", f"{base}/api/v1/external/invocations/{invocation_id}", body
    )
    if status >= 300:
        raise SystemExit(f"patch failed ({status}): {payload}")
    assert isinstance(payload, dict)
    return payload


def run_harness_once(
    base: str,
    source: str,
    tool: str,
    input_obj: dict[str, Any],
    description: str | None,
    thread_id: str,
    poll_ms: int,
    simulate: str,
) -> None:
    request_id = f"fake-{uuid.uuid4()}"
    print(f"submitting tool={tool} source={source} request_id={request_id}")
    submitted = harness_submit(
        base, source, tool, input_obj, description, request_id, thread_id
    )
    _print_json("submit response", submitted)
    inv_id = submitted["invocation_id"]
    decided = submitted
    if submitted.get("status") in PENDING_STATES:
        print(f"awaiting approval at {base}/ui/  (invocation {inv_id})")
        decided = harness_poll(base, inv_id, poll_ms)
        _print_json("post-approval", decided)

    final = decided.get("status")
    if final not in APPROVED_STATES:
        print(f"not approved (status={final}); nothing to execute.")
        return

    print("→ running PATCH execution_status=running")
    harness_patch(base, inv_id, "running")

    # Pretend to do work.
    time.sleep(0.4)

    if simulate == "ok":
        out = {
            "content": [
                {
                    "type": "text",
                    "text": f"(fake-agent) ran '{tool}' with {input_obj!r} — pretend success",
                }
            ]
        }
        final_resp = harness_patch(base, inv_id, "completed", result=out)
    elif simulate == "fail":
        err = {"message": f"(fake-agent) pretend failure executing '{tool}'"}
        final_resp = harness_patch(
            base, inv_id, "failed", error=err, message="pretend failure"
        )
    elif simulate == "cancel":
        final_resp = harness_patch(
            base, inv_id, "cancelled", message="pretend cancellation"
        )
    else:
        raise SystemExit(f"unknown --simulate-result value: {simulate}")
    _print_json("final invocation", final_resp)


# ─── demo: scripted multi-call scenario over harness path ───────────────────


HARNESS_DEMO_CALLS: list[dict[str, Any]] = [
    {
        "tool": "Bash",
        "input": {"cmd": "ls -la /tmp"},
        "description": "list /tmp",
        "simulate": "ok",
    },
    {
        "tool": "edit_file",
        "input": {"path": "/etc/hosts", "content": "127.0.0.1 evil.example"},
        "description": "edit /etc/hosts (you should DENY this one)",
        "simulate": "ok",
    },
    {
        "tool": "Read",
        "input": {"path": "/src/AGENTS.md"},
        "description": "read AGENTS.md",
        "simulate": "ok",
    },
    {
        "tool": "Bash",
        "input": {"cmd": "exit 1"},
        "description": "command that 'fails'",
        "simulate": "fail",
    },
]

# Safe, side-effect-free calc-mcp tools so the MCP demo can spam invocations
# against a real upstream and populate the UI with varied agent_client_name
# values (clientInfo.name is only recorded for MCP-proxied calls, not for
# external/harness submissions).
MCP_DEMO_CALLS: list[dict[str, Any]] = [
    {"tool": "math", "arguments": {"action": "eval", "expression": "2+2"}},
    {"tool": "math", "arguments": {"action": "eval", "expression": "10*7"}},
    {"tool": "random", "arguments": {"type": "uuid"}},
    {"tool": "random", "arguments": {"type": "ulid"}},
    {"tool": "hash", "arguments": {"algorithm": "sha256", "input": "hello"}},
    {"tool": "base64", "arguments": {"action": "encode", "input": "atryum"}},
    {"tool": "datetime", "arguments": {"action": "now"}},
    {"tool": "semver", "arguments": {"action": "valid", "version": "1.2.3"}},
]


def run_demo(base: str, source: str | None, thread_id: str, poll_ms: int) -> None:
    print(f"demo: {len(HARNESS_DEMO_CALLS)} harness calls + {len(MCP_DEMO_CALLS)} MCP calls -> {base}")
    if source is None:
        print("demo: randomizing agent identity per call (pass --source to pin one)")
    print(f"open {base}/ui/ and approve/deny harness ones as they come in.\n")

    # MCP calls first — they're auto-approved and record clientInfo, so the
    # UI fills with varied agents immediately while you're still clicking
    # through harness approvals.
    print("── MCP phase (calc-mcp, varied clientInfo) ──")
    for i, call in enumerate(MCP_DEMO_CALLS, 1):
        name, version = (
            (source, "0.0.1") if source is not None else random.choice(AGENT_IDENTITIES)
        )
        print(f"\n── mcp {i}/{len(MCP_DEMO_CALLS)}: {call['tool']} as {name}/{version} ──")
        try:
            run_mcp(
                base=base,
                server=DEFAULT_MCP_SERVER,
                tool=call["tool"],
                arguments=call["arguments"],
                list_tools=False,
                bearer=None,
                client_name=name,
                client_version=version,
            )
        except SystemExit as e:
            print(f"  (mcp call failed: {e})")

    # Harness calls.
    print("\n── harness phase (external invocations, varied --source) ──")
    for i, call in enumerate(HARNESS_DEMO_CALLS, 1):
        agent = source if source is not None else random.choice(AGENT_IDENTITIES)[0]
        print(f"\n══ harness {i}/{len(HARNESS_DEMO_CALLS)}: {call['tool']} as {agent} ══")
        run_harness_once(
            base=base,
            source=agent,
            tool=call["tool"],
            input_obj=call["input"],
            description=call["description"],
            thread_id=thread_id,
            poll_ms=poll_ms,
            simulate=call["simulate"],
        )
    print("\ndemo complete.")


# ─── mcp client mode (JSON-RPC over /mcp/{server}) ──────────────────────────


def mcp_call(
    base: str,
    server: str | None,
    method: str,
    params: dict[str, Any] | None,
    req_id: int,
    protocol_version: str = "2025-06-18",
    extra_headers: dict[str, str] | None = None,
) -> dict[str, Any]:
    path = "/mcp/" + (server or "")
    body: dict[str, Any] = {"jsonrpc": "2.0", "id": req_id, "method": method}
    if params is not None:
        body["params"] = params
    headers = {"MCP-Protocol-Version": protocol_version}
    if extra_headers:
        headers.update(extra_headers)
    status, payload = _request("POST", base + path, body, headers=headers)
    if status >= 300:
        raise SystemExit(f"mcp {method} failed ({status}): {payload}")
    if not isinstance(payload, dict):
        raise SystemExit(f"mcp {method} returned non-json: {payload!r}")
    return payload


def run_mcp(
    base: str,
    server: str | None,
    tool: str | None,
    arguments: dict[str, Any] | None,
    list_tools: bool,
    bearer: str | None,
    client_name: str,
    client_version: str,
) -> None:
    headers: dict[str, str] = {}
    if bearer:
        headers["Authorization"] = f"Bearer {bearer}"

    print(f"mcp: server={server or '(aggregate)'} client={client_name}/{client_version}")

    # 1. initialize
    init = mcp_call(
        base,
        server,
        "initialize",
        {
            "protocolVersion": "2025-06-18",
            "clientInfo": {"name": client_name, "version": client_version},
            "capabilities": {},
        },
        req_id=1,
        extra_headers=headers,
    )
    _print_json("initialize", init)

    # 2. notifications/initialized (no response expected, server returns 202)
    mcp_call(
        base,
        server,
        "notifications/initialized",
        {},
        req_id=2,
        extra_headers=headers,
    )

    # 3. tools/list (optional)
    if list_tools or tool is None:
        tools = mcp_call(base, server, "tools/list", {}, req_id=3, extra_headers=headers)
        _print_json("tools/list", tools)
        if tool is None:
            return

    # 4. tools/call
    call = mcp_call(
        base,
        server,
        "tools/call",
        {"name": tool, "arguments": arguments or {}},
        req_id=4,
        extra_headers=headers,
    )
    _print_json("tools/call", call)


# ─── argparse ───────────────────────────────────────────────────────────────


def _parse_json_arg(value: str, label: str) -> dict[str, Any]:
    try:
        parsed = json.loads(value)
    except json.JSONDecodeError as e:
        raise SystemExit(f"--{label} must be valid JSON: {e}")
    if not isinstance(parsed, dict):
        raise SystemExit(f"--{label} must be a JSON object")
    return parsed


def main(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(
        prog="fake_agent.py",
        description="Pretend to be an agent talking to Atryum.",
    )
    p.add_argument("--base", default=DEFAULT_URL, help=f"Atryum base URL (default {DEFAULT_URL})")
    sub = p.add_subparsers(dest="mode", required=True)

    default_name, default_version = _env_or_random_identity()

    # harness
    ph = sub.add_parser("harness", help="pretend to be a coding harness like amp")
    ph.add_argument(
        "--source",
        default=default_name,
        help=f"agent label shown as 'upstream' in atryum UI (default this run: {default_name})",
    )
    ph.add_argument("--tool", required=True)
    ph.add_argument("--input", default="{}", help="JSON object of tool input")
    ph.add_argument("--description", default=None)
    ph.add_argument("--thread-id", default=DEFAULT_THREAD_ID)
    ph.add_argument("--poll-ms", type=int, default=DEFAULT_POLL_MS)
    ph.add_argument(
        "--simulate-result",
        choices=["ok", "fail", "cancel"],
        default="ok",
        help="how to PATCH the execution result after approval",
    )

    # demo
    pd = sub.add_parser("demo", help="scripted multi-call demo over harness path")
    pd.add_argument(
        "--source",
        default=None,
        help="pin all demo calls to one agent label; default: pick a new random agent per call",
    )
    pd.add_argument("--thread-id", default=DEFAULT_THREAD_ID)
    pd.add_argument("--poll-ms", type=int, default=DEFAULT_POLL_MS)

    # mcp
    pm = sub.add_parser("mcp", help="pretend to be an MCP client (JSON-RPC)")
    pm.add_argument(
        "server",
        nargs="?",
        default=DEFAULT_MCP_SERVER,
        help=(
            "MCP server name to talk to "
            f"(default {DEFAULT_MCP_SERVER!r}; pass empty string '' for aggregate /mcp/)"
        ),
    )
    pm.add_argument("--tool", default=None, help="tool name for tools/call")
    pm.add_argument(
        "--arguments", default="{}", help="JSON object of tool arguments"
    )
    pm.add_argument(
        "--list-tools",
        action="store_true",
        help="call tools/list (default if --tool omitted)",
    )
    pm.add_argument(
        "--bearer",
        default=None,
        help="bearer token for the Authorization header (optional)",
    )
    pm.add_argument(
        "--client-name",
        default=default_name,
        help=f"clientInfo.name sent in initialize (default this run: {default_name})",
    )
    pm.add_argument(
        "--client-version",
        default=default_version,
        help=f"clientInfo.version sent in initialize (default this run: {default_version})",
    )

    args = p.parse_args(argv)

    if args.mode == "harness":
        run_harness_once(
            base=args.base,
            source=args.source,
            tool=args.tool,
            input_obj=_parse_json_arg(args.input, "input"),
            description=args.description,
            thread_id=args.thread_id,
            poll_ms=args.poll_ms,
            simulate=args.simulate_result,
        )
    elif args.mode == "demo":
        run_demo(
            base=args.base,
            source=args.source,  # may be None → demo will randomize per call
            thread_id=args.thread_id,
            poll_ms=args.poll_ms,
        )
    elif args.mode == "mcp":
        # Empty string explicitly opts into the aggregate /mcp/ route.
        server = args.server if args.server != "" else None
        run_mcp(
            base=args.base,
            server=server,
            tool=args.tool,
            arguments=_parse_json_arg(args.arguments, "arguments"),
            list_tools=args.list_tools,
            bearer=args.bearer,
            client_name=args.client_name,
            client_version=args.client_version,
        )
    else:
        p.error(f"unknown mode {args.mode!r}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
