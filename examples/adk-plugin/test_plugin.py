"""Unit tests for the Atryum gate decision mapping.

Runs standalone with **no google-adk and no real Atryum**: a tiny stdlib HTTP
server plays the role of Atryum's external-hook API and is scripted per test to
return allow / deny / forever-pending responses. Exercises the real urllib
transport and poll loop.

    python -m unittest test_plugin      # or:  pytest test_plugin.py
"""

from __future__ import annotations

import asyncio
import json
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

from atryum_adk import AtryumClient, make_atryum_gate
from atryum_adk.client import Decision


class _StubAtryum:
    """Scripted stand-in for Atryum's external-hook API.

    mode:
        "allow"           -> POST returns status=approved immediately
        "allow_after"     -> POST pending, GET approves after `flip_after` polls
        "deny"            -> POST returns status=denied + approval.reason
        "pending"         -> always pending (drives the timeout path)
    """

    def __init__(self, mode: str = "allow", reason: str = "blocked by rule", flip_after: int = 2):
        self.mode = mode
        self.reason = reason
        self.flip_after = flip_after
        self.posts: list[dict] = []
        self._get_count = 0

    def on_post(self, body: dict) -> dict:
        self.posts.append(body)
        status = "approved" if self.mode == "allow" else (
            "denied" if self.mode == "deny" else "pending_approval"
        )
        return {"invocation_id": "inv_test_1", "status": status}

    def on_get(self) -> dict:
        self._get_count += 1
        if self.mode == "deny":
            return {"status": "denied", "approval": {"reason": self.reason}}
        if self.mode == "allow_after" and self._get_count >= self.flip_after:
            return {"status": "approved"}
        if self.mode in ("pending", "allow_after"):
            return {"status": "pending_approval"}
        return {"status": "approved"}


class _Handler(BaseHTTPRequestHandler):
    def log_message(self, *args):  # silence
        pass

    def _send(self, payload: dict):
        data = json.dumps(payload).encode()
        self.send_response(200)
        self.send_header("content-type", "application/json")
        self.send_header("content-length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_POST(self):
        length = int(self.headers.get("content-length", 0))
        body = json.loads(self.rfile.read(length) or b"{}")
        self._send(self.server.stub.on_post(body))

    def do_GET(self):
        self._send(self.server.stub.on_get())


class _StubServer:
    def __init__(self, stub: _StubAtryum):
        self.httpd = ThreadingHTTPServer(("127.0.0.1", 0), _Handler)
        self.httpd.stub = stub  # type: ignore[attr-defined]
        self.thread = threading.Thread(target=self.httpd.serve_forever, daemon=True)

    def __enter__(self) -> str:
        self.thread.start()
        host, port = self.httpd.server_address
        return f"http://{host}:{port}"

    def __exit__(self, *exc):
        self.httpd.shutdown()
        self.httpd.server_close()


def _client(url: str, **kw) -> AtryumClient:
    kw.setdefault("poll_interval", 0.02)
    kw.setdefault("decision_deadline", 0.3)
    kw.setdefault("request_timeout", 2.0)
    return AtryumClient(url, source="test", **kw)


class DecideTests(unittest.TestCase):
    def test_allow_immediately(self):
        with _StubServer(_StubAtryum("allow")) as url:
            d = _client(url).decide("read_file", {"path": "/etc/hostname"})
        self.assertTrue(d.allowed)
        self.assertEqual(d.status, "approved")
        self.assertIsNone(d.reason)

    def test_allow_after_polling(self):
        stub = _StubAtryum("allow_after", flip_after=2)
        with _StubServer(stub) as url:
            d = _client(url).decide("read_file", {"path": "/x"})
        self.assertTrue(d.allowed)
        self.assertGreaterEqual(stub._get_count, 2)

    def test_deny_with_reason(self):
        with _StubServer(_StubAtryum("deny", reason="rm is destructive")) as url:
            d = _client(url).decide("run_shell_command", {"command": "rm -rf /"})
        self.assertFalse(d.allowed)
        self.assertEqual(d.status, "denied")
        self.assertEqual(d.reason, "rm is destructive")
        self.assertIn("rm is destructive", d.to_tool_response()["message"])
        self.assertTrue(d.to_tool_response()["blocked_by_atryum"])

    def test_timeout_fails_closed(self):
        with _StubServer(_StubAtryum("pending")) as url:
            d = _client(url).decide("run_shell_command", {"command": "sleep 999"})
        self.assertFalse(d.allowed)  # fail-closed on no decision
        self.assertIn("timed out", d.reason)

    def test_connection_error_fails_closed(self):
        # Nothing listening on this port -> transport error -> deny.
        d = _client("http://127.0.0.1:1").decide("read_file", {"path": "/x"})
        self.assertFalse(d.allowed)
        self.assertEqual(d.status, "error")

    def test_connection_error_fail_open(self):
        d = _client("http://127.0.0.1:1", fail_open=True).decide("read_file", {"path": "/x"})
        self.assertTrue(d.allowed)
        self.assertEqual(d.status, "error_fail_open")

    def test_submitted_payload_shape(self):
        stub = _StubAtryum("allow")
        with _StubServer(stub) as url:
            _client(url, agent_id="agent-42").decide("read_file", {"path": "/etc/hostname"})
        (posted,) = stub.posts
        self.assertEqual(posted["tool"], "read_file")
        self.assertEqual(posted["source"], "test")
        self.assertEqual(posted["input"], {"path": "/etc/hostname"})
        self.assertEqual(posted["agent_id"], "agent-42")
        self.assertTrue(posted["idempotency_key"])


class _StubTool:
    """Stand-in for an ADK BaseTool (only ``.name`` is used by the gate)."""

    def __init__(self, name: str):
        self.name = name


class GateCallbackTests(unittest.TestCase):
    def test_callback_allows_returns_none(self):
        with _StubServer(_StubAtryum("allow")) as url:
            gate = make_atryum_gate(url=url, source="test", poll_interval=0.02, decision_deadline=0.3)
            result = asyncio.run(gate(_StubTool("read_file"), {"path": "/x"}, None))
        self.assertIsNone(result)  # None => ADK runs the tool

    def test_callback_denies_returns_dict(self):
        with _StubServer(_StubAtryum("deny", reason="nope")) as url:
            gate = make_atryum_gate(url=url, source="test", poll_interval=0.02, decision_deadline=0.3)
            result = asyncio.run(gate(_StubTool("run_shell_command"), {"command": "rm -rf /"}, None))
        self.assertIsInstance(result, dict)
        self.assertTrue(result["blocked_by_atryum"])
        self.assertIn("nope", result["message"])


class DecisionMappingTests(unittest.TestCase):
    def test_tool_response_default_reason(self):
        self.assertIn("denied by policy", Decision(False, "denied").to_tool_response()["message"])


if __name__ == "__main__":
    unittest.main()
