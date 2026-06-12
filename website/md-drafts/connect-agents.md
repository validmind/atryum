# Connect agents

Connect your coding agents to Atryum, allowing Atryum to review tool invocations before your agents run them.

Connect your coding agents to Atryum so tool invocations pass through Atryum before they run. Atryum evaluates each call against your rules, routes calls that need review to human approval, and records every outcome in the invocation audit log.

To retrieve the available setup options for each supported coding agent:

```bash
./atryum hooks --help
```

## Connect Cursor and Claude code

The hooks command currently supports direct setup for Cursor and Claude Code.
To install Atryum hooks for Cursor:

```bash
./atryum hooks install cursor
```

To install Atryum hooks for Claude Code:

```bash
./atryum hooks install claude-code
```

To remove Atryum hook commands later, run the matching uninstall command:

```bash
./atryum hooks uninstall cursor
./atryum hooks uninstall claude-code
```

## Connect other coding agents

Other agent integrations connect through Atryum's Model Context Protocol (MCP) proxy instead of calling upstream MCP servers directly.

Add a standard MCP connection entry wherever your agent expects it and point it at: `http://<atryum-host-and-port>/mcp/<server_name>`

- Replace `<atryum-host-and-port>` with your Atryum base URL. The default local address is `localhost:8080`.
- Replace `<server_name>` with the name you gave the MCP server under **Servers** in the Atryum UI.

Refer to the setup examples for:

- [Claude Code](https://github.com/validmind/atryum/tree/main/examples/claude-code-hook)
- [Amp](https://github.com/validmind/atryum/tree/main/examples/amp-plugin)
- [Pi](https://github.com/validmind/atryum/tree/main/examples/pi-extension)
- [Codex](https://github.com/validmind/atryum/tree/main/examples/codex-mcp)
- [Other harnesses](https://github.com/validmind/atryum/tree/main/examples)

These integrations currently support no-auth mode only. Your agent sends tool invocations to Atryum over the network without OAuth credentials. Use OAuth-backed Atryum when you need verified agent identity instead of self-declared labels.

Before starting your agent, export these environment variables in the same shell session:

- **ATRYUM_URL** — Base URL of your Atryum server. Defaults to `http://localhost:8080`. Change this when Atryum runs on another host or port.
- **ATRYUM_AGENT_ID** — Self-declared agent identifier that Atryum matches against Agent Record **Agent IDs**. Leave this unset if you do not need invocations tagged to a specific agent record.

For example:

```bash
export ATRYUM_URL=http://localhost:8080
export ATRYUM_AGENT_ID=amp-local
```

To tag invocations to an Agent Record and apply agent-scoped rules:

1. In Atryum, open an Agent Record or create one.
2. Add a stable string to its **Agent IDs** field, such as `amp-local` or `pi-alice`.
3. Export the same string as `ATRYUM_AGENT_ID` before starting your agent.
4. Try a tool invocation again. Atryum should attach the call to that Agent Record instead of leaving the agent column empty.

Optionally, set these variables to label the harness in Atryum:

- **ATRYUM_CLIENT_NAME** — Harness name shown in the Atryum agent column. Defaults to each integration's source label when unset.
- **ATRYUM_CLIENT_VERSION** — Harness version shown in Atryum. Some integrations also read their native version variables, such as `AMP_VERSION` or `PI_VERSION`.

Make sure Atryum is running and reachable at `ATRYUM_URL` before you start your agent. Pending tool calls appear under **Invocations** in the Atryum UI.