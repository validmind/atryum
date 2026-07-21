"""Transport + decision logic for the Atryum ADK integration.

This module has **no dependency on google-adk** so it can be imported (and
unit-tested) anywhere. It speaks the same external-hook contract every other
Atryum integration uses (Claude Code / Cursor / amp hooks, the managed-agents
bridge, the Google Agent Gateway callout):

    POST /api/v1/external/invocations   -> {"invocation_id", "status"}
    GET  /api/v1/external/invocations/{id} -> {"status", "approval": {"reason"}, ...}

Statuses:
    received / pending_approval          -> keep polling
    approved / executing / succeeded     -> ALLOW
    anything else (denied/failed/...)    -> DENY

The gate is a governance control, so it **fails closed** by default: any HTTP
error, malformed response, or decision that does not arrive before the deadline
is treated as a DENY. Set ``fail_open=True`` (or ``ATRYUM_FAIL_OPEN=1``) only if
you deliberately want the agent to keep running when Atryum is unreachable.
"""

from __future__ import annotations

import asyncio
import json
import logging
import os
import time
import urllib.error
import urllib.request
import uuid
from dataclasses import dataclass
from typing import Any, Mapping, Optional

logger = logging.getLogger("atryum_adk")

#: Statuses Atryum reports while a decision is still being made.
PENDING_STATUSES = frozenset({"received", "pending_approval"})
#: Statuses that mean the tool call is allowed to proceed.
ALLOW_STATUSES = frozenset({"approved", "executing", "succeeded"})

_INVOCATIONS_PATH = "/api/v1/external/invocations"


def _to_bool(value: str) -> bool:
    return value.strip().lower() in {"1", "true", "yes", "on"}


@dataclass(frozen=True)
class AtryumConfig:
    """Resolved configuration for an :class:`AtryumClient`.

    Every field has a constructor default, an environment-variable fallback, and
    can be overridden explicitly. Precedence: explicit argument > environment >
    default.
    """

    url: str = "http://localhost:8080"
    source: str = "adk"
    client_name: str = "adk"
    agent_id: Optional[str] = None
    client_version: str = ""
    #: Per-HTTP-request socket timeout (seconds).
    request_timeout: float = 15.0
    #: Delay between poll attempts while a decision is pending (seconds).
    poll_interval: float = 0.3
    #: Total wall-clock budget to reach a terminal decision before failing (seconds).
    decision_deadline: float = 20.0
    #: When True, transport/timeout errors ALLOW the call instead of denying it.
    fail_open: bool = False

    @classmethod
    def resolve(cls, **overrides: Any) -> "AtryumConfig":
        """Build a config from explicit overrides, env vars, then defaults."""

        def pick(key: str, env: str, cast: Any) -> Any:
            override = overrides.get(key)
            if override is not None:
                return override
            raw = os.environ.get(env)
            if raw is not None and raw != "":
                return cast(raw)
            return getattr(cls, key)

        return cls(
            url=str(pick("url", "ATRYUM_URL", str)).rstrip("/"),
            source=pick("source", "ATRYUM_SOURCE", str),
            client_name=pick("client_name", "ATRYUM_CLIENT_NAME", str),
            agent_id=pick("agent_id", "ATRYUM_AGENT_ID", str),
            client_version=pick("client_version", "ATRYUM_CLIENT_VERSION", str),
            request_timeout=pick("request_timeout", "ATRYUM_REQUEST_TIMEOUT", float),
            poll_interval=pick("poll_interval", "ATRYUM_POLL_INTERVAL", float),
            decision_deadline=pick("decision_deadline", "ATRYUM_DECISION_DEADLINE", float),
            fail_open=pick("fail_open", "ATRYUM_FAIL_OPEN", _to_bool),
        )


@dataclass(frozen=True)
class Decision:
    """The outcome of a single gate check."""

    allowed: bool
    status: str
    reason: Optional[str] = None
    invocation_id: Optional[str] = None

    def to_tool_response(self) -> dict:
        """The dict ADK returns to the model in place of the (blocked) tool.

        ADK short-circuits tool execution when a ``before_tool_callback`` /
        plugin hook returns a dict, handing this back to the model as the tool
        result. Keep it descriptive so the model can explain the block.
        """
        return {
            "blocked_by_atryum": True,
            "status": self.status,
            "invocation_id": self.invocation_id,
            "message": f"Atryum blocked this tool call: {self.reason or 'denied by policy'}",
        }


class AtryumClient:
    """Submits tool calls to Atryum and resolves an allow/deny :class:`Decision`.

    The client is synchronous under the hood (stdlib ``urllib``) but exposes an
    async wrapper, :meth:`decide_async`, that runs the blocking poll loop off the
    event loop via ``asyncio.to_thread`` — the form ADK callbacks/plugins use.
    """

    def __init__(
        self,
        url: Optional[str] = None,
        *,
        source: Optional[str] = None,
        client_name: Optional[str] = None,
        agent_id: Optional[str] = None,
        client_version: Optional[str] = None,
        request_timeout: Optional[float] = None,
        poll_interval: Optional[float] = None,
        decision_deadline: Optional[float] = None,
        fail_open: Optional[bool] = None,
    ) -> None:
        self.config = AtryumConfig.resolve(
            url=url,
            source=source,
            client_name=client_name,
            agent_id=agent_id,
            client_version=client_version,
            request_timeout=request_timeout,
            poll_interval=poll_interval,
            decision_deadline=decision_deadline,
            fail_open=fail_open,
        )

    # -- transport -----------------------------------------------------------
    def _http(self, method: str, path: str, body: Optional[dict] = None) -> dict:
        data = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(
            self.config.url + path,
            data=data,
            headers={"content-type": "application/json"},
            method=method,
        )
        with urllib.request.urlopen(req, timeout=self.config.request_timeout) as resp:
            return json.load(resp)

    # -- decision ------------------------------------------------------------
    def decide(
        self,
        tool: str,
        args: Mapping[str, Any],
        *,
        agent_id: Optional[str] = None,
        description: Optional[str] = None,
        idempotency_key: Optional[str] = None,
    ) -> Decision:
        """Submit one tool call and block until Atryum returns a terminal state.

        Never raises: on any error it returns a fail-closed (or, if configured,
        fail-open) :class:`Decision`.
        """
        try:
            return self._decide(tool, dict(args), agent_id, description, idempotency_key)
        except Exception as exc:  # noqa: BLE001 - governance gate must not raise
            if self.config.fail_open:
                logger.warning("Atryum gate error, failing OPEN tool=%s: %s", tool, exc)
                return Decision(True, "error_fail_open", str(exc), None)
            logger.warning("Atryum gate error, failing CLOSED tool=%s: %s", tool, exc)
            return Decision(False, "error", f"Atryum unreachable ({exc})", None)

    def _decide(
        self,
        tool: str,
        args: dict,
        agent_id: Optional[str],
        description: Optional[str],
        idempotency_key: Optional[str],
    ) -> Decision:
        key = idempotency_key or f"{self.config.source}-{tool}-{uuid.uuid4().hex}"
        detail = self._http(
            "POST",
            _INVOCATIONS_PATH,
            {
                "source": self.config.source,
                "tool": tool,
                "input": args,
                "idempotency_key": key,
                "client_name": self.config.client_name,
                "agent_id": agent_id or self.config.agent_id,
                "description": description or f"ADK tool call: {tool}",
            },
        )
        inv_id = detail.get("invocation_id")
        status = detail.get("status")

        deadline = time.monotonic() + self.config.decision_deadline
        while status in PENDING_STATUSES and time.monotonic() < deadline:
            time.sleep(self.config.poll_interval)
            detail = self._http("GET", f"{_INVOCATIONS_PATH}/{inv_id}")
            status = detail.get("status")

        if status in ALLOW_STATUSES:
            logger.info("Atryum ALLOW tool=%s status=%s", tool, status)
            return Decision(True, status, None, inv_id)

        if status in PENDING_STATUSES:
            reason = f"no decision within {self.config.decision_deadline:g}s (timed out pending approval)"
            logger.warning("Atryum DENY (timeout) tool=%s: %s", tool, reason)
            return Decision(False, status or "pending", reason, inv_id)

        # Terminal deny. Fetch the reason if the last body we have lacks it.
        reason = (detail.get("approval") or {}).get("reason")
        if not reason and inv_id:
            try:
                reason = (self._http("GET", f"{_INVOCATIONS_PATH}/{inv_id}").get("approval") or {}).get("reason")
            except Exception:  # noqa: BLE001 - best-effort reason lookup
                pass
        reason = reason or "denied by Atryum policy"
        logger.warning("Atryum DENY tool=%s status=%s reason=%s", tool, status, reason)
        return Decision(False, status or "denied", reason, inv_id)

    async def decide_async(
        self,
        tool: str,
        args: Mapping[str, Any],
        *,
        agent_id: Optional[str] = None,
        description: Optional[str] = None,
        idempotency_key: Optional[str] = None,
    ) -> Decision:
        """Async wrapper around :meth:`decide` (runs the poll loop in a thread)."""
        return await asyncio.to_thread(
            self.decide,
            tool,
            dict(args),
            agent_id=agent_id,
            description=description,
            idempotency_key=idempotency_key,
        )
