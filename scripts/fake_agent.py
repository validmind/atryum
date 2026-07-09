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

Config (env or flags):
  ATRYUM_URL        base url, default http://localhost:8080
  ATRYUM_MCP_SERVER default MCP server name, default "calc-mcp"
  ATRYUM_SOURCE     pins the agent identity (harness --source / mcp clientInfo.name);
                    when unset, a random identity is picked from a small set of
                    realistic harness names (amp, cursor, claude-code, …)
  ATRYUM_POLL_MS    poll interval ms while awaiting approval, default 1000
  THREAD_ID         thread id surfaced in harness submissions

Auth (env; both unset = no-auth mode):
  ATRYUM_ACCESS_TOKEN   static OAuth bearer token, used as-is and never refreshed
  ATRYUM_TOKEN_COMMAND  shell command that mints a token (raw, or OAuth token
                        JSON with access_token); wins over ATRYUM_ACCESS_TOKEN
                        when both are set
  ATRYUM_TOKEN_REFRESH_SKEW_MS     re-mint this long before expiry, default 60000
  ATRYUM_TOKEN_COMMAND_TIMEOUT_MS  token command timeout, default 10000
  ATRYUM_STATE_DIR      token-cache.json location,
                        default ~/.atryum/fake-agent-state
"""

from __future__ import annotations

import argparse
import hashlib
import json
import math
import os
import random
import subprocess
import sys
import threading
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
ACCESS_TOKEN = os.environ.get("ATRYUM_ACCESS_TOKEN", "").strip()
TOKEN_COMMAND = os.environ.get("ATRYUM_TOKEN_COMMAND", "").strip()


def _env_ms(name: str, fallback: float) -> float:
    """Read a millisecond env var, falling back on malformed or negative
    values (mirrors the Node integrations)."""
    try:
        n = float(os.environ.get(name, "") or fallback)
    except ValueError:
        return fallback
    return n if math.isfinite(n) and n >= 0 else fallback


TOKEN_REFRESH_SKEW_SECONDS = _env_ms("ATRYUM_TOKEN_REFRESH_SKEW_MS", 60000) / 1000
TOKEN_COMMAND_TIMEOUT_SECONDS = _env_ms("ATRYUM_TOKEN_COMMAND_TIMEOUT_MS", 10000) / 1000
STATE_DIR = os.environ.get("ATRYUM_STATE_DIR", "") or os.path.join(
    os.path.expanduser("~"), ".atryum", "fake-agent-state"
)
TOKEN_CACHE_FILE = os.path.join(STATE_DIR, "token-cache.json") if TOKEN_COMMAND else ""


def _set_token_cache_key(base: str) -> None:
    """Tie the cached token to the command and server that produced it, so
    switching ATRYUM_TOKEN_COMMAND or the resolved base URL invalidates the
    cache instead of sending a token minted for a different identity or
    target. Trailing slashes are stripped so equivalent URL spellings (and
    the Node integrations' keys) match. main() re-keys with the resolved
    --base once args are parsed."""
    global TOKEN_CACHE_KEY
    TOKEN_CACHE_KEY = (
        hashlib.sha256(f"{TOKEN_COMMAND}\n{base.rstrip('/')}".encode()).hexdigest()
        if TOKEN_COMMAND
        else ""
    )


_set_token_cache_key(DEFAULT_URL)
_cached_token = "" if TOKEN_COMMAND else ACCESS_TOKEN
_cached_token_expires_at = float("inf") if (ACCESS_TOKEN and not TOKEN_COMMAND) else 0.0
_token_refresh_lock = threading.Lock()

# Real-world agent/harness identities. Used as:
#   - harness mode: --source (drives the Agent column via inv.client_name)
#   - mcp mode:     clientInfo.name in the initialize handshake
# Pick one at random per run so the UI shows a mix of agents instead of a
# single monoculture "fake-agent" stream.
#
# The first block is what each harness *actually* sends in
# `initialize.clientInfo` — captured by pointing each one at a stdio
# MCP sniffer (see Atryum SC-16270 ticket). Keep the exact strings;
# AgentIcon.tsx's `detectAgentKind` regex normalizes them to icons.
#
# The second block is best-guess for harnesses we haven't sniffed. Strings
# may not match what these tools actually send — fine for populating a
# demo, but don't read anything into them.
AGENT_IDENTITIES: list[tuple[str, str]] = [
    # Empirically verified.
    ("amp-mcp-client", "0.0.0-dev"),
    ("codex-mcp-client", "0.128.0"),
    ("Claude Code", "2.1.123"),
    ("cursor-vscode", "0.45.7"),
    ("opencode", "1.14.31"),
    # Best-guess; not yet verified by a real sniff.
    ("cline", "3.1.0"),
    ("windsurf", "1.2.4"),
    ("continue", "0.9.250"),
    ("zed", "0.156.0"),
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


def _parse_token_response(raw: str) -> tuple[str, float]:
    text = raw.strip()
    if not text:
        raise RuntimeError("token command returned no token")
    if not text.startswith("{"):
        if any(c.isspace() for c in text):
            raise RuntimeError("raw token command output must not contain whitespace")
        return text, time.time() + 55 * 60
    parsed = json.loads(text)
    token = next(
        (
            value
            for value in (
                parsed.get("access_token"),
                parsed.get("accessToken"),
                parsed.get("token"),
            )
            if isinstance(value, str)
        ),
        "",
    )
    if not token:
        raise RuntimeError("token command response did not include access_token")
    if any(c.isspace() for c in token):
        raise RuntimeError("token command response token must not contain whitespace")

    def _to_epoch_s(v: float) -> float:
        return v / 1000 if v > 1e11 else v

    def _expiry(value: Any) -> float:
        # Providers send expiry fields as numbers or numeric strings; coerce,
        # and treat non-numeric or non-positive values as absent (mirrors the
        # Node integrations).
        if isinstance(value, bool) or not isinstance(value, (int, float, str)):
            return 0.0
        try:
            n = float(value)
        except ValueError:
            return 0.0
        return n if math.isfinite(n) and n > 0 else 0.0

    expires_at_value = _expiry(parsed.get("expires_at")) or _expiry(
        parsed.get("expiresAt")
    )
    expires_in = _expiry(parsed.get("expires_in"))
    if expires_at_value:
        expires_at = _to_epoch_s(expires_at_value)
    elif expires_in:
        expires_at = time.time() + expires_in
    else:
        expires_at = time.time() + 55 * 60
    return token, expires_at


def _read_token_cache() -> tuple[str, float] | None:
    if not TOKEN_CACHE_FILE:
        return None
    try:
        with open(TOKEN_CACHE_FILE, encoding="utf-8") as f:
            cached = json.load(f)
        token = cached.get("token")
        expires_at = float(cached.get("expiresAt")) / 1000  # ms, shared with Node caches
        if (
            isinstance(token, str)
            and token
            and cached.get("key") == TOKEN_CACHE_KEY
            and time.time() < expires_at - TOKEN_REFRESH_SKEW_SECONDS
        ):
            return token, expires_at
    except (OSError, ValueError, TypeError):
        pass  # cache miss or unreadable
    return None


def _write_token_cache(token: str, expires_at: float) -> None:
    if not TOKEN_CACHE_FILE:
        return
    try:
        os.makedirs(os.path.dirname(TOKEN_CACHE_FILE), exist_ok=True)
        # Write to a fresh temp file so 0o600 applies (an existing file keeps
        # its old mode), then rename into place atomically.
        tmp = f"{TOKEN_CACHE_FILE}.{os.getpid()}.tmp"
        fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
        with os.fdopen(fd, "w", encoding="utf-8") as f:
            json.dump(
                {"token": token, "expiresAt": int(expires_at * 1000), "key": TOKEN_CACHE_KEY},
                f,
            )
        os.replace(tmp, TOKEN_CACHE_FILE)
    except OSError:
        pass  # ignore — in-memory cache still works


def _access_token(force_refresh: bool = False) -> str:
    global _cached_token, _cached_token_expires_at
    if not TOKEN_COMMAND:
        return ACCESS_TOKEN
    if (
        not force_refresh
        and _cached_token
        and time.time() < _cached_token_expires_at - TOKEN_REFRESH_SKEW_SECONDS
    ):
        return _cached_token
    with _token_refresh_lock:
        if (
            not force_refresh
            and _cached_token
            and time.time() < _cached_token_expires_at - TOKEN_REFRESH_SKEW_SECONDS
        ):
            return _cached_token
        if not force_refresh:
            file_cache = _read_token_cache()
            if file_cache:
                _cached_token, _cached_token_expires_at = file_cache
                return _cached_token
        proc = subprocess.run(
            TOKEN_COMMAND,
            shell=True,
            check=True,
            capture_output=True,
            text=True,
            timeout=TOKEN_COMMAND_TIMEOUT_SECONDS,
        )
        _cached_token, _cached_token_expires_at = _parse_token_response(proc.stdout)
        _write_token_cache(_cached_token, _cached_token_expires_at)
        return _cached_token


def _authorization_headers(force_refresh: bool = False) -> dict[str, str]:
    token = _access_token(force_refresh)
    return {"Authorization": f"Bearer {token}"} if token else {}


def _request(
    method: str,
    url: str,
    body: Any | None = None,
    headers: dict[str, str] | None = None,
    timeout: float = 30.0,
) -> tuple[int, dict[str, Any] | str]:
    return _request_once(method, url, body, headers, timeout, False)


def _request_once(
    method: str,
    url: str,
    body: Any | None,
    headers: dict[str, str] | None,
    timeout: float,
    force_refresh: bool,
) -> tuple[int, dict[str, Any] | str]:
    data = None
    hdrs = dict(headers or {})
    if "Authorization" not in hdrs:
        hdrs.update(_authorization_headers(force_refresh))
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
        payload: dict[str, Any] | str = json.loads(raw) if raw else {}
    except json.JSONDecodeError:
        payload = raw
    if status == 401 and TOKEN_COMMAND and not force_refresh:
        return _request_once(method, url, body, headers, timeout, True)
    return status, payload


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

    print(f"mcp: server={server} client={client_name}/{client_version}")

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
        help=f"MCP server name to talk to (default {DEFAULT_MCP_SERVER!r})",
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
    _set_token_cache_key(args.base)

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
        if args.server == "":
            raise SystemExit("mcp server name is required; use /mcp/{server}")
        run_mcp(
            base=args.base,
            server=args.server,
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
