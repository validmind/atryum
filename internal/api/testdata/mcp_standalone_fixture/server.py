"""Real MCP server (official Python SDK, FastMCP, Streamable HTTP transport)
used by mcp_standalone_stream_test.go to exercise Atryum's standalone-SSE-
stream relay path against genuine SDK behavior, not a hand-rolled fixture.

FastMCP's Context.report_progress() calls send_progress_notification()
without related_request_id, so the server's message router sends every
progress update to the standalone GET stream — never to the tools/call POST
response body. That's exactly the case internal/mcp/client.go's
standaloneStream machinery exists for (see docs/architecture.md's "Live SSE
relay for tools/call" section). This fixture is not a workaround for that
behavior; it demonstrates it, because it's the real SDK's actual behavior.

Reads PORT from the environment (default 8642) so the Go test harness can
pick a free port per run.
"""

import asyncio
import os

from mcp.server.fastmcp import Context, FastMCP

PORT = int(os.environ.get("PORT", "8642"))

mcp = FastMCP("atryum-standalone-fixture", host="127.0.0.1", port=PORT)


@mcp.tool()
async def slow_streaming_task(ctx: Context, steps: int = 3, delay_seconds: float = 1.0) -> str:
    """Report progress steps times with real delays, then return a result."""
    for i in range(1, steps + 1):
        await ctx.report_progress(progress=i, total=steps, message=f"step {i}/{steps}")
        await asyncio.sleep(delay_seconds)
    return f"done after {steps} real progress notifications"


if __name__ == "__main__":
    mcp.run(transport="streamable-http")
