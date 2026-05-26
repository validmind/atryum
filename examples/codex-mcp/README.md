# Atryum Codex MCP setup

Codex does not currently expose a stable public command-hook API equivalent to
Amp's `tool.call` / `tool.result` events or Claude Code's `PreToolUse` /
`PostToolUse` hooks.

That means there is no honest Codex-native version of the Amp plugin that can
gate every native Codex file read, edit, and shell command through
`/api/v1/external/invocations`.

What Atryum can do for Codex today is mediate MCP calls. Configure Codex to use
Atryum's MCP proxy instead of connecting directly to the upstream MCP server:

```toml
# ~/.codex/config.toml

[mcp_servers.leanctx_via_atryum]
command = "npx"
args = [
  "mcp-remote",
  "http://localhost:8080/mcp/leanctx"
]
```

Replace `leanctx` with the Atryum MCP server name you configured in the Atryum
server registry.

## What this covers

- Codex calls to MCP tools routed through Atryum
- Atryum approval rules, invocation logging, and admin UI for those MCP calls

## What this does not cover

- Codex native shell execution
- Codex native file reads
- Codex native file edits

For those native Codex actions, use Codex's own `approval_policy` and
`sandbox_mode` controls until Codex exposes a stable pre/post native tool hook.
