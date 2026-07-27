---
description: Connect this project to Atryum approval gating
---

Set up Atryum approval gating for this OpenCode project.

Reference files:
- @examples/opencode-plugin/atryum.ts
- @examples/opencode-plugin/README.md

Workflow:
1. Briefly explain what the plugin does.
2. If the user has not already told you their Atryum base URL, ask for it and default to `http://localhost:8080`.
3. If the user has not already given you a bearer token or token-minting command, explain how to get one from their Atryum web login and ask them to paste it. Wait for the user's reply before making file changes.
4. Once you have what you need:
   - copy `@examples/opencode-plugin/atryum.ts` to `.opencode/plugins/atryum.ts`
   - write the secret config to `~/.config/opencode/atryum.json` so it stays out of git
   - set the secret config file mode to `0600`
   - store `url` plus either `accessToken` or `tokenCommand`
   - never echo the secret back in chat after it is provided
5. If the user explicitly asks for project-local storage instead, write `.opencode/atryum.json` instead of the global config file and still keep it mode `0600`.
6. When you finish, list the files you created or changed and remind the user to restart OpenCode so the plugin loads.

If the user prefers short-lived OAuth, offer to store a `tokenCommand` instead of a raw `accessToken`.
