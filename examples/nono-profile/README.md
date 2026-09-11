# Atryum hooks inside a nono sandbox

A [nono](https://nono.sh) profile that lets a sandboxed Claude Code keep
talking to Atryum.

Every other directory under `examples/` shows how to wire an agent *into*
Atryum. This one is about what happens when something else is already
wrapped around the agent. nono is a capability-based sandbox: it runs Claude
Code with a deny-by-default filesystem, and the stock `claude` package profile
grants only what Claude Code itself needs. The Atryum hook is a separate
process that Claude Code spawns on every tool call, and it lives outside the
working directory, so under the stock profile it fails before it can reach the
server. `claude-local.json` extends the stock profile with the four paths the
hook touches, and nothing else.

## What the hook needs

`atryum setup claude` (or the manual install in `examples/claude-code-hook`)
leaves three things under `~/.atryum`:

| path                          | who uses it                                        | access the hook needs |
| ----------------------------- | -------------------------------------------------- | --------------------- |
| `~/.atryum/hooks/`            | `atryum-hook.mjs`, run by `node` on each tool call | read                  |
| `~/.atryum/agent-key`         | the agent API key, read via `ATRYUM_TOKEN_COMMAND` | read, single file     |
| `~/.atryum/agent-hook-state/` | tool-use to invocation-id map and the token cache  | read and write        |

The profile grants exactly that. The key is a `read_file` entry rather than a
directory grant so nothing else under `~/.atryum` becomes visible. The
operator's own sign-in from `atryum login` sits next to it in
`~/.atryum/credentials.json`, and the agent has no business reading that.

Network is left as the `claude` package profile configures it. The hook only
needs to reach the Atryum server, which for local development is
`http://localhost:8080`, and the stock profile does not block that.

## Install

```sh
mkdir -p ~/.config/nono/profiles
cp examples/nono-profile/claude-local.json ~/.config/nono/profiles/
nono profile validate claude-local
nono profile diff claude claude-local
```

The diff should show only the four filesystem additions:

```
  Filesystem:
    + read $HOME/.atryum/agent-hook-state
    + read $HOME/.atryum/hooks
    + write $HOME/.atryum/agent-hook-state
    + read_file $HOME/.atryum/agent-key
```

Then launch Claude Code through nono with the profile instead of directly:

```sh
nono run --profile claude-local claude
```

The `SessionStart` hook fires as usual and Atryum shows the session under the
agent the key belongs to. If tool calls stop reaching Atryum, ask nono
directly whether the profile grants each path the hook needs:

```sh
nono why --profile claude-local --path ~/.atryum/agent-key --op read
nono why --profile claude-local --path ~/.atryum/agent-hook-state --op readwrite
```

Each should answer `ALLOWED` with `Source: profile`.

## Adapting it

- **Different state directory.** If you set `ATRYUM_STATE_DIR` in the hook
  command, replace both `agent-hook-state` entries with that path.
- **OAuth instead of an API key.** With `ATRYUM_ACCESS_TOKEN` exported, or an
  `ATRYUM_TOKEN_COMMAND` that calls your identity provider, drop the
  `read_file` entry. The token cache still lands in the state directory, so
  keep those grants.
- **Other harnesses.** Cursor runs the same shared hook from the same paths,
  so the filesystem block carries over unchanged. Swap `extends` for the nono
  package profile of the harness you run, and add the paths that harness's own
  hook configuration lives under if the package profile does not already cover
  them. Amp's plugin (`examples/amp-plugin`) is a different script with its
  own paths; start from its README instead.
- **Tighter than this.** nono merges `filesystem` arrays additively, so a
  child profile can only widen what `claude` grants. To narrow the base, edit
  the package profile or start from `default` and rebuild the Claude Code
  grants yourself.

## Why bother

Atryum and nono answer different questions. nono decides what the process may
touch on this machine; Atryum decides which tool calls the agent may make, and
keeps the record. Running both means an agent that talks its way past one
control still has to clear the other. The profile is what keeps the second
control alive inside the first.
