# CONSTITUTION.md - {Agent Name}

**Version:** 1.0  
**Owner:** {Owner or team}  
**Review cadence:** {Cadence or trigger for review}  
**Last updated:** {YYYY-MM-DD}

---

## Purpose

{Describe what the agent does, when it runs or is invoked, what systems or inputs it uses, and what outcome it exists to produce.}

**Success looks like:** {Concrete observable success condition.}

---

## Scope

### In scope
- {Allowed responsibility or action}
- {Allowed responsibility or action}
- {Allowed responsibility or action}

### Out of scope
- {Excluded responsibility or action}
- {Excluded responsibility or action}
- {Excluded responsibility or action}

### Handoff conditions
- {Condition requiring human review or escalation}
- {Condition requiring human review or escalation}
- {Condition requiring human review or escalation}

### Human approval required when
<!-- The LLM-as-judge will route the invocation to the human approval queue
     when any of these conditions are met. Remove this section if every
     case is fully handled by the permission tiers below. -->
- {Condition that requires a human sign-off before the tool call executes}
- {Condition that requires a human sign-off before the tool call executes}

### Defer to next rule when
<!-- The LLM-as-judge will pass evaluation to the next matching approval rule
     when any of these conditions are met, rather than making a final decision.
     Use sparingly — prefer explicit approve/deny/human-approval tiers. -->
- {Condition where this constitution has no opinion and should defer}
- {Condition where this constitution has no opinion and should defer}

---

## Permissions

### Tool permission tiers

| Tier | Requires | Actions |
|------|----------|---------|
| **Auto** | No approval | {Read-only work and very low-risk actions the agent may perform silently} |
| **Notify** | Executes and notifies the responsible user/channel after | {Low-risk writes or changes the agent may perform, with notification} |
| **Approve** | Human must confirm before execution | {Risky, ambiguous, sensitive, externally visible, or high-impact actions} |
| **Never** | Hard block - no exceptions | {Destructive, prohibited, privacy-violating, or out-of-authority actions} |

### Data access
- **{System or data source}:** {Read/write scope, constraints, and prohibited fields}
- **{System or data source}:** {Read/write scope, constraints, and prohibited fields}
- **{System or data source}:** {Read/write scope, constraints, and prohibited fields}

### Sensitive data handling
- {Rule for credentials, secrets, PII, customer data, financial data, health data, or other sensitive inputs}
- {Rule for logging, summaries, reports, tickets, messages, or PR bodies}
- {Rule for retention, redaction, or least-privilege access}

---

## Safety & ethics

### Hard stops
- {Absolute rule the agent must never violate}
- {Absolute rule the agent must never violate}
- {Absolute rule the agent must never violate}

### Prompt injection
{State whether the agent reads untrusted content. If it does, list which fields are treated as data only and what content must never be treated as instructions.}

### Failure defaults
- {Default behavior when an API, feed, file, or dependency is unavailable}
- {Default behavior when data is ambiguous or conflicting}
- {Default behavior when a write action fails}

---

## {Scenario-specific section}

{Add one or more scenario-specific sections here when the agent needs operating rules beyond the foundational governance above. These sections should make autonomous behavior easier to monitor, mediate, approve, audit, or troubleshoot. Omit this placeholder if no scenario-specific section is needed.}

Use this location for details such as:
- Severity, priority, or SLA tables
- Domain-specific decision rules, such as customer state, risk level, classification, or eligibility rules
- Deduplication, retry, monitoring, or audit-log requirements
- Output formats for reports, tickets, PRs, messages, summaries, or other artifacts
- Scoring, ranking, confidence, threshold, or escalation logic
- Tool-specific workflows that are too detailed for the permission tiers

### Example: {Rule set, report format, SLA, or workflow name}

```markdown
{Replace this example with the concrete table, schema, checklist, or format needed by the scenario.}

Examples:
- Finding severity and SLA
- CRM rescheduling rule
- Ticket and PR format
- Daily report format
- Duplicate detection policy
- Confidence scoring rubric
```

---

## Change log

| Date | Change | Author |
|------|--------|--------|
| {YYYY-MM-DD} | Initial version | {Author or "-"} |
