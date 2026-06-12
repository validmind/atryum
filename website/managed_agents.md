Claude Managed Agents

Claude Managed Agents are different from Claude Code hooks or MCP proxying.

Anthropic runs the harness and the sandbox. The Claude agent does not call Atryum before every tool call. Atryum connects outbound to Anthropic, watches the session event stream, and answers Claude when the session stops for `requires_action`.

Concepts in Anthropic terms:
- Agent: reusable Claude config, tools, MCP servers, skills, prompt.
- Environment: where Anthropic runs the session.
- Session: one running agent task in an environment.
- Events: the log/stream Atryum watches.

To use this with Atryum:

1. Configure `[[managed_agents]]` with an Anthropic `api_key` and `workspace`.
2. Restart Atryum.
3. In Agents, edit an Atryum agent and link a Claude Managed Agent.
4. Configure the Claude agent's tools to ask for confirmation, usually `permission_policy = always_ask`.
5. Start Claude sessions normally. Atryum discovers sessions for linked Claude agents and watches them.

If a tool is `always_allow`, Anthropic runs it without stopping. Atryum can record it after the fact, but it cannot block it.

Manual session registration still exists at `/api/v1/admin/managed-agents/sessions`. Use it for debugging or when you do not want to link a Claude agent in the UI.

Linking writes Atryum metadata onto the Claude agent so two Atryum installs do not accidentally fight over the same Claude agent. Use force connect only when you mean to take it over.
