"""ADK wiring for the Atryum gate — the two integration styles ADK supports.

1. ``atryum_gate`` / :func:`make_atryum_gate` — a ``before_tool_callback``.
   Wire it on any agent::

       from atryum_adk import atryum_gate
       Agent(..., before_tool_callback=atryum_gate)

   Runs per-agent, sees the full resolved tool args, and (on deny) returns a
   message the model reads back.

2. :class:`AtryumPlugin` — an ADK ``BasePlugin`` registered once on a Runner to
   gate **every** tool of **every** agent under that runner::

       from atryum_adk import AtryumPlugin
       InMemoryRunner(agent=agent, plugins=[AtryumPlugin(source="adk-agent")])

Both paths funnel through :class:`~atryum_adk.client.AtryumClient` and share its
fail-closed semantics. This module does not import google-adk at import time: the
callback needs no ADK types, and :class:`AtryumPlugin` is built lazily (see
:func:`__getattr__`) so ``atryum_adk`` stays importable without google-adk
installed.

Plugin API verified against **google-adk 2.3.0**:
``google.adk.plugins.base_plugin.BasePlugin(name: str)`` with
``async def before_tool_callback(self, *, tool, tool_args, tool_context) -> Optional[dict]``
(return None to allow, a dict to short-circuit and hand that dict to the model).
The plugin manager stops at the first non-None result and turns any *raised*
exception into a RuntimeError — so the gate returns a deny dict, it never raises.
"""

from __future__ import annotations

from typing import Any, Awaitable, Callable, Mapping, Optional

from .client import AtryumClient, AtryumConfig, Decision

__all__ = ["atryum_gate", "make_atryum_gate", "AtryumPlugin"]

# A before_tool_callback: (tool, args, tool_context) -> None | dict (awaitable).
BeforeToolCallback = Callable[[Any, Mapping[str, Any], Any], Awaitable[Optional[dict]]]


def _tool_name(tool: Any) -> str:
    """ADK passes a BaseTool (``.name``); tolerate a bare string too."""
    return getattr(tool, "name", None) or str(tool)


async def _decide(client: AtryumClient, tool: Any, args: Optional[Mapping[str, Any]]) -> Optional[dict]:
    decision: Decision = await client.decide_async(_tool_name(tool), dict(args or {}))
    return None if decision.allowed else decision.to_tool_response()


_default_client: Optional[AtryumClient] = None


def _get_default_client() -> AtryumClient:
    global _default_client
    if _default_client is None:
        _default_client = AtryumClient()
    return _default_client


async def atryum_gate(tool: Any, args: Mapping[str, Any], tool_context: Any = None) -> Optional[dict]:
    """Zero-config ``before_tool_callback``: gate every tool via env-configured Atryum.

    Returns ``None`` to allow the tool, or a deny dict (returned to the model).
    Configure via env vars (``ATRYUM_URL``, ``ATRYUM_SOURCE``, ...). For explicit
    config, use :func:`make_atryum_gate` instead.
    """
    return await _decide(_get_default_client(), tool, args)


def make_atryum_gate(
    client: Optional[AtryumClient] = None,
    **config: Any,
) -> BeforeToolCallback:
    """Build a configured ``before_tool_callback``.

    Pass an existing ``client`` or any :class:`AtryumConfig` field as a keyword
    (``url``, ``source``, ``client_name``, ``agent_id``, ``request_timeout``,
    ``poll_interval``, ``decision_deadline``, ``fail_open``). The returned
    callable exposes the client as ``.atryum_client``.
    """
    resolved = client or AtryumClient(**config)

    async def _gate(tool: Any, args: Mapping[str, Any], tool_context: Any = None) -> Optional[dict]:
        return await _decide(resolved, tool, args)

    _gate.atryum_client = resolved  # type: ignore[attr-defined]
    return _gate


# ---------------------------------------------------------------------------
# AtryumPlugin — built lazily so importing this module never requires google-adk.
# ---------------------------------------------------------------------------
_plugin_cls: Optional[type] = None


def _build_plugin_class() -> type:
    global _plugin_cls
    if _plugin_cls is not None:
        return _plugin_cls

    from google.adk.plugins.base_plugin import BasePlugin  # lazy: only needs ADK here

    class AtryumPlugin(BasePlugin):
        """Gate every tool call under a Runner through Atryum.

        Register once — ``InMemoryRunner(agent=..., plugins=[AtryumPlugin(...)])``
        or ``Runner(..., plugins=[AtryumPlugin(...)])`` — to cover all agents and
        tools with no per-agent wiring. Fails closed by default.
        """

        def __init__(
            self,
            url: Optional[str] = None,
            *,
            name: str = "atryum",
            client: Optional[AtryumClient] = None,
            source: Optional[str] = None,
            client_name: Optional[str] = None,
            agent_id: Optional[str] = None,
            client_version: Optional[str] = None,
            request_timeout: Optional[float] = None,
            poll_interval: Optional[float] = None,
            decision_deadline: Optional[float] = None,
            fail_open: Optional[bool] = None,
        ) -> None:
            super().__init__(name=name)
            self.client = client or AtryumClient(
                url,
                source=source,
                client_name=client_name,
                agent_id=agent_id,
                client_version=client_version,
                request_timeout=request_timeout,
                poll_interval=poll_interval,
                decision_deadline=decision_deadline,
                fail_open=fail_open,
            )

        async def before_tool_callback(  # type: ignore[override]
            self,
            *,
            tool: Any,
            tool_args: dict,
            tool_context: Any,
        ) -> Optional[dict]:
            return await _decide(self.client, tool, tool_args)

    _plugin_cls = AtryumPlugin
    return _plugin_cls


def __getattr__(name: str) -> Any:
    # PEP 562 lazy attribute: `from atryum_adk.plugin import AtryumPlugin` builds
    # the class on demand, so `import atryum_adk.plugin` alone stays ADK-free.
    if name == "AtryumPlugin":
        return _build_plugin_class()
    raise AttributeError(f"module {__name__!r} has no attribute {name!r}")
