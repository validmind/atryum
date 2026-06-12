#!/usr/bin/env python3
"""Minimal Streamable HTTP MCP server for integration tests.

Emits multi-frame text/event-stream responses for tools/call and other methods
so Atryum and mock clients can exercise SSE decoding and (eventually) live relay.

Usage:
  python streaming_mcp_server.py --port 18080
"""

from __future__ import annotations

import argparse
import json
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any


FINAL_MARKER_PREFIX = "STREAM_FINAL:"


def _sse_event(data: dict[str, Any] | str) -> bytes:
    if isinstance(data, dict):
        payload = json.dumps(data, separators=(",", ":"))
    else:
        payload = data
    return f"event: message\ndata: {payload}\n\n".encode("utf-8")


def _json_response(handler: BaseHTTPRequestHandler, status: int, body: dict[str, Any]) -> None:
    raw = json.dumps(body).encode("utf-8")
    handler.send_response(status)
    handler.send_header("Content-Type", "application/json")
    handler.send_header("Content-Length", str(len(raw)))
    handler.end_headers()
    handler.wfile.write(raw)


class StreamingMCPHandler(BaseHTTPRequestHandler):
    server_version = "StreamingMockMCP/1.0"

    def log_message(self, fmt: str, *args: Any) -> None:
        sys.stderr.write("%s - %s\n" % (self.address_string(), fmt % args))

    def do_GET(self) -> None:
        if self.path.rstrip("/") not in ("/mcp", ""):
            self.send_error(404)
            return
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Cache-Control", "no-cache")
        self.end_headers()
        self.wfile.write(b": mock streaming mcp ready\n\n")
        self.wfile.flush()

    def do_POST(self) -> None:
        if self.path.rstrip("/") not in ("/mcp", ""):
            self.send_error(404)
            return
        length = int(self.headers.get("Content-Length", "0"))
        raw = self.rfile.read(length)
        try:
            req = json.loads(raw.decode("utf-8"))
        except json.JSONDecodeError:
            self.send_error(400, "invalid json")
            return

        method = req.get("method", "")
        req_id = req.get("id")
        params = req.get("params") or {}

        if method == "initialize":
            _json_response(
                self,
                200,
                {
                    "jsonrpc": "2.0",
                    "id": req_id,
                    "result": {
                        "protocolVersion": self.headers.get("MCP-Protocol-Version", "2025-06-18"),
                        "capabilities": {"tools": {}},
                        "serverInfo": {"name": "streaming-mock-mcp", "version": "1.0.0"},
                    },
                },
            )
            return

        if method == "notifications/initialized":
            self.send_response(202)
            self.end_headers()
            return

        if method == "tools/list":
            _json_response(
                self,
                200,
                {
                    "jsonrpc": "2.0",
                    "id": req_id,
                    "result": {
                        "tools": [
                            {
                                "name": "stream_echo",
                                "description": "Echo a value after streaming progress frames",
                                "inputSchema": {
                                    "type": "object",
                                    "properties": {"value": {"type": "integer"}},
                                    "required": ["value"],
                                },
                            }
                        ]
                    },
                },
            )
            return

        if method in ("tools/call", "ping"):
            self._stream_method_response(method, req_id, params)
            return

        _json_response(
            self,
            200,
            {
                "jsonrpc": "2.0",
                "id": req_id,
                "error": {"code": -32601, "message": f"method not found: {method}"},
            },
        )

    def _stream_method_response(self, method: str, req_id: Any, params: dict[str, Any]) -> None:
        if method == "tools/call":
            tool = params.get("name", "")
            arguments = params.get("arguments") or {}
            if tool != "stream_echo":
                _json_response(
                    self,
                    200,
                    {
                        "jsonrpc": "2.0",
                        "id": req_id,
                        "error": {"code": -32000, "message": f"unknown tool: {tool}"},
                    },
                )
                return
            value = int(arguments.get("value", 0))
            final_text = f"{FINAL_MARKER_PREFIX}{value}"
        else:
            final_text = f"{FINAL_MARKER_PREFIX}ping"

        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Cache-Control", "no-cache")
        self.end_headers()

        frames = [
            {
                "jsonrpc": "2.0",
                "method": "notifications/progress",
                "params": {"progress": 0.25, "message": "frame-1"},
            },
            {
                "jsonrpc": "2.0",
                "method": "notifications/progress",
                "params": {"progress": 0.75, "message": "frame-2"},
            },
            {
                "jsonrpc": "2.0",
                "id": req_id,
                "result": {
                    "content": [{"type": "text", "text": final_text}],
                    "isError": False,
                },
            },
        ]
        for frame in frames:
            self.wfile.write(_sse_event(frame))
            self.wfile.flush()


def main() -> int:
    parser = argparse.ArgumentParser(description="Mock streaming MCP HTTP server")
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", type=int, required=True)
    args = parser.parse_args()

    httpd = ThreadingHTTPServer((args.host, args.port), StreamingMCPHandler)
    print(f"streaming mock MCP listening on http://{args.host}:{args.port}/mcp", flush=True)
    try:
        httpd.serve_forever()
    except KeyboardInterrupt:
        pass
    return 0


if __name__ == "__main__":
    raise SystemExit(main())