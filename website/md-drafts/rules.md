# Atryum Rules


Atryum supports 4 rule results: 
- Auto-Approve
- Human Approve
- Auto Deny
- AI Evaluation


AI Evaluation is only available after you have set up an AI provider on the settings page or connected Atryum to a ValidMind organization.


Atryum rules are evaluated top to bottom with 'first match wins' logic. The first rule that matches a tool call returns the result to the system.
However, AI Evaluation is an exception, it can return 'forward for more rule evaluations', usually it does this if the Charter doesn't have anything to do with the tool call being evaluated.


If no rule matches, then atryum falls back to `policy.provider` value from `atryum.toml` and if unset, defaults to `manual_approval`.


Rules can be mapped to agents, servers, and specific tool calls.

Harness level tools (e.g. `read`, `write`) can't be detected by atryum, but you can just type them in and press save.


We reccomend you create 4 sections of rules:
- At the top put rules for tools you never want automatically run, send them to Human Approval or Auto Deny.
- In the middle put your auto approves: list/read operations you want to just happen. Speeds up agents and saves on tokens.
- Below that put your AI Evaluations
- Lastly put an explicit blanket Auto Deny or Human Approval.
