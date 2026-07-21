"""Minimal runnable ADK agent gated by Atryum via AtryumPlugin.

Mirrors the shape of every Atryum integration: the agent has a read tool
(``read_file``) and a mutating tool (``run_shell_command``); each tool call is
submitted to Atryum before it runs. With a sensible ruleset (allow reads, deny
destructive shell) this prints one ALLOW and one DENY.

Run it:
    pip install -e '.[adk]'          # installs google-adk
    export GOOGLE_API_KEY=...        # (or configure another ADK model)
    export ATRYUM_URL=http://localhost:8080
    python example_agent.py

Requires a running Atryum and a configured ADK model. This script performs no
GCP calls itself; the only network egress is to your ADK model and to Atryum.
"""

from __future__ import annotations

import asyncio
import json
import logging
import subprocess

from google.adk.agents import Agent
from google.adk.runners import InMemoryRunner
from google.genai import types

from atryum_adk import AtryumPlugin

# Surface the plugin's ALLOW/DENY decisions on the console.
logging.basicConfig(level=logging.INFO, format="[atryum] %(message)s")
logging.getLogger("atryum_adk").setLevel(logging.INFO)

APP = "atryum-adk-demo"


def read_file(path: str) -> dict:
    """Read a text file and return its contents."""
    print(f"   >> read_file executing on {path}")
    return {"content": f"(demo) first line of {path}"}


def run_shell_command(command: str) -> dict:
    """Run a shell command and return its output."""
    print(f"   >> run_shell_command EXECUTING: {command}")
    out = subprocess.run(command, shell=True, capture_output=True, text=True)
    return {"stdout": out.stdout, "stderr": out.stderr, "code": out.returncode}


agent = Agent(
    name="atryum_demo_agent",
    model="gemini-2.5-flash",
    instruction=(
        "You are a devops assistant. Call read_file to read files and "
        "run_shell_command to run shell commands. Attempt every tool the user "
        "asks for. If a tool is blocked, tell the user it was blocked and why."
    ),
    tools=[read_file, run_shell_command],
    # NOTE: alternatively wire per-agent with a callback instead of the plugin:
    #   from atryum_adk import atryum_gate
    #   before_tool_callback=atryum_gate,
)


async def main() -> None:
    # One AtryumPlugin on the runner gates every tool of every agent it runs.
    runner = InMemoryRunner(
        agent=agent,
        app_name=APP,
        plugins=[AtryumPlugin(source="adk-agent", agent_id="adk-demo-agent")],
    )
    session = await runner.session_service.create_session(app_name=APP, user_id="user1")
    prompt = (
        "Read the file /etc/hostname for me, and also run the shell command "
        "`rm -rf /tmp/demo` to clean up. Please do both."
    )
    print(f"\nUSER: {prompt}\n")
    async for event in runner.run_async(
        user_id="user1",
        session_id=session.id,
        new_message=types.Content(role="user", parts=[types.Part(text=prompt)]),
    ):
        if not event.content:
            continue
        for part in event.content.parts:
            if getattr(part, "function_call", None):
                print(f"   AGENT wants tool: {part.function_call.name}({dict(part.function_call.args)})")
            if getattr(part, "function_response", None):
                print(f"   tool result: {json.dumps(part.function_response.response)[:160]}")
            if getattr(part, "text", None) and part.text.strip():
                print(f"\nAGENT: {part.text.strip()}\n")


if __name__ == "__main__":
    asyncio.run(main())
