
# Clients (Agents) to Atryum
There are two ways to authenticate clients to atryum.

1. In no auth mode
2. In auth mode


In no auth mode, agents and harnesses self-identify with an agent_id. This is done as a `?agent_id=my_cool_id` query parameter on mcp path or harness clients send `agent_id` to the api. See `examples` directory.

In auth mode, agents and harnesses must authenticate via oauth to atryum. Atryum then uses their oauth clinet id as the agent_id when evaluating rules. A keycloak container is provided in docker compose for local/development setups. Your corporate IDP should be used if deploying this anywhere near production.


# Atryum to MCP servers

Atryum natively supports authenticating to remote MCP servers that support oauth2 and dynamic client registration. Fill in the details on the 'add server' page and press save and reconnect. Manual registration is also possible in the ui.
