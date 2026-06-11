# Connect agents

To retrieve the available setup options for each supported coding agent:

```bash
./atryum hooks --help
```

#### Install Atryum hooks

The hooks command currently supports direct setup for Cursor and Claude Code. Other agent integrations may use separate extensions or MCP configuration.

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

***

Connecting via MCP:

Add a standard mcp connection json wherever your agent expects it. Use this url:

http://<atryum-host-and-port>/mcp/<server_name>

Where atryumhost-and-port deaults to localhost:8080 and `server_name` is the name you gave the mcp server in the Servers section of the ui.


Connecting via coding agent/harness:

Examples exist in the repo for Claude Code, Amp, Pi, and others. These only support no-auth mode for now so you will need to set config env vars:

ATRYUM_URL
ATRYUM_AGENT_ID

Optionally:
ATRYUM_CLIENT_NAME
ATRYUM_CLIENT_VERSION
