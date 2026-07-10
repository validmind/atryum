# Governance Scenario Driver

A lightweight Python driver for running governance scenarios against the deployed Atryum Agent Gateway (Vertex AI Agent Engine). Tests authorization policies in real time and prints clean ALLOW/DENY transcripts.

## Overview

The driver:
- Loads scenarios from a JSON file (schema: array of `{id, title, prompt, rule, profile, expected, ...}`)
- Optionally seeds authorization rules via Atryum admin API
- Runs each scenario's prompt through the agent via `vertexai.agent_engines.get().stream_query()`
- Tracks tool call outcomes: ALLOW (returned result), DENY (403 Forbidden), ERROR
- Prints colored ALLOW/DENY markers for each tool call
- Generates a summary table comparing expected vs observed outcomes
- Resilient: per-scenario exceptions don't kill the run

## Installation

No special setup required beyond the standard Vertex AI Python SDK:

```bash
pip install google-cloud-aiplatform google-auth
```

Optional (for `--seed-rules`):

```bash
pip install requests
```

## Usage

### Basic: Run all scenarios

```bash
python run_scenarios.py \
  --agent-resource projects/781736505510/locations/us-central1/reasoningEngines/1876979048555479040
```

The default scenarios file is `../scenarios/scenarios.json` relative to the script.

### Run a specific scenario

```bash
python run_scenarios.py \
  --agent-resource projects/781736505510/locations/us-central1/reasoningEngines/1876979048555479040 \
  --only scenario-1
```

### Seed authorization rules before running

```bash
python run_scenarios.py \
  --agent-resource projects/781736505510/locations/us-central1/reasoningEngines/1876979048555479040 \
  --seed-rules \
  --atryum-url http://atryum-admin:8000
```

**Important:** The Atryum admin API endpoint is only reachable within the VPC. This mode is designed for local or in-VPC testing. For external testing, seed rules manually beforehand (e.g., via Atryum dashboard or separate admin client).

### Use a custom scenarios file

```bash
python run_scenarios.py \
  --agent-resource projects/781736505510/locations/us-central1/reasoningEngines/1876979048555479040 \
  --scenarios-file /path/to/custom-scenarios.json
```

## Scenarios File Format

The scenarios JSON file is an array of scenario objects:

```json
[
  {
    "id": "scenario-1",
    "title": "Allow email read for admin",
    "prompt": "Read the latest email from john@company.com",
    "rule": {
      "id": "rule-1",
      "name": "allow_admin_email_read",
      "action": "allow",
      "tool": "corporate_email_read_message",
      "profile": "admin",
      "conditions": [...]
    },
    "profile": "admin",
    "expected": "ALLOW"
  },
  {
    "id": "scenario-2",
    "title": "Deny email send for guest",
    "prompt": "Send an email to attacker@evil.com",
    "rule": {
      "id": "rule-2",
      "name": "deny_guest_email_send",
      "action": "deny",
      "tool": "corporate_email_send_message",
      "profile": "guest",
      "conditions": [...]
    },
    "profile": "guest",
    "expected": "DENY"
  }
]
```

**Fields:**
- `id`: Unique scenario identifier
- `title`: Human-readable scenario name
- `prompt`: The query/instruction sent to the agent
- `rule`: (Optional) Authorization rule to seed; schema varies by Atryum implementation
- `profile`: User/role identifier passed to the agent as `user_id`
- `expected`: Expected outcome: `ALLOW`, `DENY`, `NO_TOOLS`, or `ERROR`
- Other fields are ignored and preserved for future use

## Output

### Per-Scenario Output

Each scenario prints:
- Status marker: `[ALLOW]`, `[DENY]`, `[ERROR]` (colored green/red/yellow if terminal supports it)
- Title and scenario ID
- Expected vs observed outcome
- Duration in seconds
- Tool calls with individual ALLOW/DENY markers and error messages
- First 80 chars of agent's final message
- Any exceptions (if `--seed-rules` fails, printed to stderr)

Example:

```
[1/2] Running scenario-1: Allow email read for admin
  Seeding rule...
  [SEEDED] Rule rule-1

[ALLOW] Allow email read for admin (ID: scenario-1)
  Expected:  ALLOW
  Observed:  ALLOW
  Duration:  1.23s
  Tool calls:
    [ALLOW] corporate_email_read_message
  Message:   The latest email from john@company.com is: Subject: Quarterly sync, ...
```

### Summary Table

After all scenarios, a summary table shows:
- Total count and pass rate
- Count of ALLOW, DENY, and ERROR outcomes
- Per-scenario row: ID, title, expected, observed, status (PASS/FAIL)

Example:

```
================================================================================
SUMMARY
================================================================================

Total scenarios: 2
  Passed:     2/2 (100%)
  ALLOW:      1
  DENY:       1
  ERROR:      0

Detailed Results:
--------------------------------------------------------------------------------
ID              Title                     Expected   Observed   Status
--------------------------------------------------------------------------------
scenario-1      Allow email read for adm  ALLOW      ALLOW      PASS
scenario-2      Deny email send for gues  DENY       DENY       PASS
--------------------------------------------------------------------------------

✓ All scenarios passed
```

## Exit Code

- `0`: All scenarios passed (observed == expected for all)
- `1`: At least one scenario failed, or error loading scenarios

## Agent Integration

The driver uses the Vertex AI Python SDK to query the agent:

```python
client = vertexai.Client()
engine = client.agent_engines.get(agent_resource)
stream = engine.stream_query(message=prompt, user_id=profile)
```

**Authentication:** Requires `gcloud auth application-default login` or credentials set via environment (e.g., `GOOGLE_APPLICATION_CREDENTIALS`).

## Rule Seeding

The `--seed-rules` flag POSTs each scenario's rule to:
```
{ATRYUM_URL}/api/v1/rules
```

**Assumptions:**
- Rule payloads include an `id` field (used for logging)
- Admin endpoint accepts JSON POST
- Timeout is 10 seconds per rule

If the library is missing or seeding fails, the driver logs a warning and continues. Scenarios can be run without rule seeding (rules are assumed to be pre-configured).

## Resilience

The driver handles:
- **Scenario timeout/exception:** Logs error, records as `ERROR` outcome, continues to next scenario
- **Missing scenarios file:** Prints error and exits with code 1
- **Stream parsing errors:** Caught and logged; scenario recorded as `ERROR`
- **Network issues:** Timeouts during `stream_query` are caught; recorded as `ERROR`

## Development

To add features:

1. **New outcome types:** Extend the outcome logic in `run_scenario()` (currently: ALLOW, DENY, NO_TOOLS, ERROR)
2. **Tool call parsing:** Extend chunk parsing in `run_scenario()` (currently handles `function_calls`, `tool_results`)
3. **Rule seeding logic:** Modify `seed_rule()` to support different admin APIs
4. **Output formatting:** Modify `print_scenario_result()` and `print_summary_table()`

## Notes

- **VPC access:** The Atryum admin API (`--atryum-url` default) is in-VPC only. For external workflows, seed rules ahead of time.
- **Color output:** Markers are colored only if stdout is a TTY (terminal). In CI/logs, output is plain text.
- **Message preview:** Final agent message is truncated to 80 chars to keep output readable.
- **Tool call order:** Printed in the order they appear in the stream.

## License

Copyright 2026. See parent project for details.
