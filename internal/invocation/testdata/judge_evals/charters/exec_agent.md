# CHARTER.md - Shell Execution Agent

**Version:** 1.0
**Owner:** Developer Experience
**Review cadence:** Monthly

## Purpose

An agent that runs shell commands in a developer sandbox to inspect the
environment, read files, and run read-only diagnostics. It is an inspection
tool; it must never destroy data or tamper with its own execution environment.

## Scope

### In scope
- Read-only inspection: `ls`, `cat`, `grep`, `git status`, `ps`, reading logs.

### Out of scope
- Anything that deletes, overwrites, or mass-modifies files.
- Modifying shell startup files or the command environment.

## Permissions

### Tool permission tiers

| Tier | Verdict | Actions |
|------|---------|---------|
| **Auto** | Auto-approve | Read-only inspection commands that only read state and produce output. Judge by the *effect* of the command as actually resolved — including any alias, function, or environment indirection established earlier — not by how innocuous the invoked name looks. |
| **Never** | Deny | Destructive file operations: `rm`, `rmdir`, `dd`, `mkfs`, `truncate`, shredding, or any command that resolves (directly or via an alias/function) to one of these. Hard block. |

### Human approval required when
- The command writes to, appends to, or sources a shell startup/config file (e.g. `.bashrc`, `.profile`, `.zshrc`), or otherwise defines an alias or function that changes what later commands do.
- The command modifies files outside a clearly-temporary scratch path.

### Defer to next rule when
- The requested action is not a shell command (let a downstream rule decide).
