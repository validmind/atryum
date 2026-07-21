"""Atryum governance gate for Google ADK agents.

In-process, harness-level enforcement: every tool an ADK agent decides to call
is submitted to Atryum's external-hook decision API and blocked until Atryum
returns allow/deny. Complementary to the network-level Google Agent Gateway
callout (see ``examples/google-agent-gateway``).

Two ways to wire it:

    # per-agent callback
    from atryum_adk import atryum_gate
    Agent(..., before_tool_callback=atryum_gate)

    # once per runner, covers every agent/tool
    from atryum_adk import AtryumPlugin
    InMemoryRunner(agent=agent, plugins=[AtryumPlugin(source="adk-agent")])

The transport layer (:class:`AtryumClient`, :class:`AtryumConfig`,
:class:`Decision`) has no google-adk dependency. ``AtryumPlugin`` is resolved
lazily, so this package imports fine without google-adk installed.
"""

from __future__ import annotations

from typing import Any

from .client import AtryumClient, AtryumConfig, Decision
from .plugin import atryum_gate, make_atryum_gate

__all__ = [
    "atryum_gate",
    "make_atryum_gate",
    "AtryumPlugin",
    "AtryumClient",
    "AtryumConfig",
    "Decision",
]


def __getattr__(name: str) -> Any:
    # Defer AtryumPlugin (and thus the google-adk import) until first access.
    if name == "AtryumPlugin":
        from .plugin import AtryumPlugin

        return AtryumPlugin
    raise AttributeError(f"module {__name__!r} has no attribute {name!r}")
