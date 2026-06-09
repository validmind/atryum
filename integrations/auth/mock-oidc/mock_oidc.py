#!/usr/bin/env python3
"""Lightweight OIDC mock for Atryum integration tests.

Implements enough of Keycloak's atryum realm surface for local harness testing:
  - OpenID Provider discovery
  - JWKS
  - client_credentials token endpoint
  - Dynamic Client Registration (RFC 7591)

Usage:
  python3 mock_oidc.py --port 9090 --realm atryum

Environment (written to .env by the server on startup):
  MOCK_OIDC_ISSUER, MOCK_OIDC_TOKEN_URL, MOCK_OIDC_DCR_URL,
  MOCK_OIDC_CLIENT_ID, MOCK_OIDC_CLIENT_SECRET
"""

from __future__ import annotations

import argparse
import json
import secrets
import time
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any
from urllib.parse import parse_qs, urlparse

import jwt
from cryptography.hazmat.primitives import serialization
from cryptography.hazmat.primitives.asymmetric import rsa


def _b64url_uint(val: int) -> str:
    import base64

    raw = val.to_bytes((val.bit_length() + 7) // 8, "big")
    return base64.urlsafe_b64encode(raw).rstrip(b"=").decode("ascii")


class MockOIDC:
    def __init__(self, host: str, port: int, realm: str, env_file: Path | None) -> None:
        self.host = host
        self.port = port
        self.realm = realm
        self.base = f"http://{host}:{port}"
        self.issuer = f"{self.base}/realms/{realm}"
        self.token_url = f"{self.issuer}/protocol/openid-connect/token"
        self.dcr_url = f"{self.issuer}/clients-registrations/openid-connect"
        self.jwks_url = f"{self.issuer}/protocol/openid-connect/certs"
        self.discovery_url = f"{self.issuer}/.well-known/openid-configuration"

        self.static_client_id = "test-client"
        self.static_client_secret = "test-secret"
        self.clients: dict[str, dict[str, Any]] = {
            self.static_client_id: {
                "client_id": self.static_client_id,
                "client_secret": self.static_client_secret,
                "grant_types": ["client_credentials"],
            }
        }

        key = rsa.generate_private_key(public_exponent=65537, key_size=2048)
        self.private_pem = key.private_bytes(
            encoding=serialization.Encoding.PEM,
            format=serialization.PrivateFormat.PKCS8,
            encryption_algorithm=serialization.NoEncryption(),
        )
        pub = key.public_key().public_numbers()
        self.kid = "mock-oidc-1"
        self.jwks = {
            "keys": [
                {
                    "kty": "RSA",
                    "kid": self.kid,
                    "use": "sig",
                    "alg": "RS256",
                    "n": _b64url_uint(pub.n),
                    "e": _b64url_uint(pub.e),
                }
            ]
        }

        if env_file is not None:
            env_file.write_text(
                "\n".join(
                    [
                        f"MOCK_OIDC_ISSUER={self.issuer}",
                        f"MOCK_OIDC_TOKEN_URL={self.token_url}",
                        f"MOCK_OIDC_DCR_URL={self.dcr_url}",
                        f"MOCK_OIDC_JWKS_URL={self.jwks_url}",
                        f"MOCK_OIDC_CLIENT_ID={self.static_client_id}",
                        f"MOCK_OIDC_CLIENT_SECRET={self.static_client_secret}",
                        "",
                    ]
                ),
                encoding="utf-8",
            )

    def discovery(self) -> dict[str, Any]:
        return {
            "issuer": self.issuer,
            "authorization_endpoint": f"{self.issuer}/protocol/openid-connect/auth",
            "token_endpoint": self.token_url,
            "jwks_uri": self.jwks_url,
            "registration_endpoint": self.dcr_url,
            "response_types_supported": ["code", "token"],
            "grant_types_supported": ["client_credentials", "authorization_code"],
            "token_endpoint_auth_methods_supported": [
                "client_secret_post",
                "client_secret_basic",
                "none",
            ],
            "scopes_supported": ["openid", "atryum:mcp"],
        }

    def issue_token(self, client_id: str, scope: str) -> dict[str, Any]:
        now = int(time.time())
        payload = {
            "iss": self.issuer,
            "sub": client_id,
            "aud": "atryum",
            "azp": client_id,
            "client_id": client_id,
            "scope": scope or "atryum:mcp",
            "iat": now,
            "exp": now + 3600,
            "jti": str(uuid.uuid4()),
        }
        token = jwt.encode(payload, self.private_pem, algorithm="RS256", headers={"kid": self.kid})
        return {
            "access_token": token,
            "token_type": "Bearer",
            "expires_in": 3600,
            "scope": payload["scope"],
        }

    def register_client(self, body: dict[str, Any]) -> dict[str, Any]:
        client_id = body.get("client_id") or f"dcr-{secrets.token_hex(8)}"
        client_secret = body.get("client_secret") or secrets.token_urlsafe(24)
        record = {
            "client_id": client_id,
            "client_secret": client_secret,
            "client_name": body.get("client_name", "integration-dcr-client"),
            "grant_types": body.get("grant_types", ["client_credentials"]),
            "token_endpoint_auth_method": body.get(
                "token_endpoint_auth_method", "client_secret_post"
            ),
            "scope": body.get("scope", "atryum:mcp"),
        }
        self.clients[client_id] = record
        return {
            **record,
            "client_id_issued_at": int(time.time()),
            "client_secret_expires_at": 0,
        }


class Handler(BaseHTTPRequestHandler):
    @property
    def mock(self) -> MockOIDC:
        return self.server.mock  # type: ignore[attr-defined]

    def log_message(self, fmt: str, *args: Any) -> None:
        return

    def _json(self, status: int, payload: dict[str, Any]) -> None:
        body = json.dumps(payload).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _read_json(self) -> dict[str, Any]:
        length = int(self.headers.get("Content-Length", "0"))
        if length == 0:
            return {}
        raw = self.rfile.read(length)
        if not raw:
            return {}
        return json.loads(raw.decode("utf-8"))

    def _read_form(self) -> dict[str, list[str]]:
        length = int(self.headers.get("Content-Length", "0"))
        raw = self.rfile.read(length).decode("utf-8") if length else ""
        return parse_qs(raw, keep_blank_values=True)

    def do_GET(self) -> None:  # noqa: N802
        path = urlparse(self.path).path
        if path == f"/realms/{self.mock.realm}/.well-known/openid-configuration":
            self._json(200, self.mock.discovery())
            return
        if path == f"/realms/{self.mock.realm}/protocol/openid-connect/certs":
            self._json(200, self.mock.jwks)
            return
        if path == "/healthz":
            self._json(200, {"status": "ok"})
            return
        self._json(404, {"error": "not_found", "path": path})

    def do_POST(self) -> None:  # noqa: N802
        path = urlparse(self.path).path
        if path == f"/realms/{self.mock.realm}/protocol/openid-connect/token":
            form = self._read_form()
            grant = (form.get("grant_type") or [""])[0]
            if grant != "client_credentials":
                self._json(400, {"error": "unsupported_grant_type"})
                return
            client_id = (form.get("client_id") or [""])[0]
            client_secret = (form.get("client_secret") or [""])[0]
            scope = (form.get("scope") or ["atryum:mcp"])[0]
            record = self.mock.clients.get(client_id)
            if record is None or record.get("client_secret") != client_secret:
                self._json(401, {"error": "invalid_client"})
                return
            self._json(200, self.mock.issue_token(client_id, scope))
            return

        if path == f"/realms/{self.mock.realm}/clients-registrations/openid-connect":
            body = self._read_json()
            self._json(201, self.mock.register_client(body))
            return

        self._json(404, {"error": "not_found", "path": path})


def main() -> None:
    parser = argparse.ArgumentParser(description="Mock OIDC for Atryum integration tests")
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", type=int, default=9090)
    parser.add_argument("--realm", default="atryum")
    parser.add_argument("--env-file", default="")
    args = parser.parse_args()

    env_path = Path(args.env_file) if args.env_file else None
    mock = MockOIDC(args.host, args.port, args.realm, env_path)

    httpd = ThreadingHTTPServer((args.host, args.port), Handler)
    httpd.mock = mock  # type: ignore[attr-defined]
    print(f"mock-oidc listening on {mock.base} (issuer={mock.issuer})", flush=True)
    httpd.serve_forever()


if __name__ == "__main__":
    main()