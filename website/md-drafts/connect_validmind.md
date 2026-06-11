# Connect ValidMind#### Prerequisites

Before connecting Atryum to ValidMind:

- [x] Set up a ValidMind record type for AI agents if one does not already exist. ([Manage inventory record types](https://docs.validmind.ai/guide/inventory/manage-inventory-record-types.html))
- [x] Add a custom long text field to that record type for each agent's constitution. ([Manage inventory fields](https://docs.validmind.ai/guide/inventory/manage-inventory-fields.html))
- [x] Create at least one record in that record type and fill in its constitution field. ([Register records in the inventory](https://docs.validmind.ai/guide/inventory/register-records-in-inventory.html))
- [x] Make sure you have valid ValidMind API and secret keys for the organization you want Atryum to connect to. ([Manage your profile](https://docs.validmind.ai/guide/configuration/manage-your-profile.html#access-keys))

#### Connect Atryum to ValidMind

1. Set up ValidMind for Atryum:

```bash
./atryum setup validmind
```

2. Follow the prompts and enter your:

    - **ValidMind Base URL** — The URL for ValidMind organization you want Atryum to connect to. For example: `https://app.prod.validmind.ai/`
    - **ValidMind API key**  — Your organization API Key.
    - **ValidMind API secret** — Your organization Secret Key.

3. Atryum prints the path to the updated `atryum.toml` file. Open the file and verify that it has been edited with your submitted configurations:
    - On macOS, this is typically under `~/Library/Application Support/atryum/atryum.toml`.
    - On Linux, this is typically under `~/.config/atryum/atryum.toml`.

4. Restart Atryum so it loads the updated ValidMind credentials:

    a. In the terminal where Atryum is running, press `Ctrl+C` to stop it.
    b. Start Atryum again:

        ```bash
        ./atryum run --init-servers
        ```

5. In your browser, navigate to [`localhost:8080/ui/settings`](http://localhost:8080/ui/settings).

6. Under Agent Record Sync, select the:

    - ValidMind **Organization** — The ValidMind organization to sync records from.
    - Organization **Record Type** — The record type that identifies your agent records.
    - **Constitution Field** — The custom long text field that stores each agent's policy.

7. Click **Save Settings** to apply your changes.

#### Set up AI evaluation rule

Create a rule in Atryum to route matching tool invocations to a record that evaluates the call against the agent's constitution:

1. In Atryum, click **Rules** in the left sidebar.

2. Click **New Rule**.

3. Under **Action**, select `AI Evaluation`.

4. Under **Evaluation Model**, select the ValidMind model configuration to use for the evaluation.

5. Under **Agents**, select the synced ValidMind agent records you want this rule to apply to. Leave this empty to match all agents.
    - Under **Servers/Sources**, select the servers or sources the rule should apply to. Leave this empty to match all servers.
    - Under **Tools**, select the tools the rule should apply to. Leave this empty to match all tools.

7. Make sure that **Enabled** is checked, then click **Create**.

8. Try a tool invocation from your agent again. Atryum should evaluate the invocation against the agent's constitution instead of treating it like a brand-new manual decision.