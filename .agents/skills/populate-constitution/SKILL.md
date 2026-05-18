---
name: populate-constitution
description: Draft a complete agent constitution from an instructional prompt or agent brief. Use when Codex needs to create, populate, revise, or normalize CONSTITUTION.md-style governance documents that define an agent's purpose, scope, permissions, safety rules, handoff conditions, scenario-specific operating rules, and change log.
---

# Populate Constitution

## Workflow

1. Read the user's instructional prompt and identify the agent's job, users, connected systems, recurring cadence, autonomous actions, approval boundaries, and expected outputs.
2. Load `assets/constitution-template.md` when the exact output structure is needed.
3. If the prompt is underspecified, make conservative assumptions and mark them with bracketed placeholders such as `[Owner]`, `[Review cadence]`, or `[Confirm system]`.
4. Populate the foundational sections and add scenario-specific sections where the prompt needs more operational detail. Do not leave generic filler; write concrete rules derived from the prompt.
5. Preserve the constitution style: concise, operational, safety-forward, and written as binding instructions for the future agent and the tool-use governance system mediating it.
6. Add a one-row change log with today's date unless the user provides another date.

## Drafting Rules

- Use a specific title: `# CONSTITUTION.md - {Agent Name}`.
- Keep `Purpose` to one or two paragraphs plus a concrete `Success looks like` sentence.
- Define `Scope` with three subsections: `In scope`, `Out of scope`, and `Handoff conditions`.
- Define permission tiers as `Auto`, `Notify`, `Approve`, and `Never`, even if some tiers are sparse.
- Treat write actions, destructive actions, external communications, privacy-sensitive reads, and cross-system side effects as approval-sensitive unless explicitly safe in the prompt.
- Include prompt-injection rules whenever the agent reads untrusted text, user-generated content, documents, code, tickets, calendar descriptions, emails, CRM notes, webpages, or external feeds.
- Include failure defaults that bias toward surfacing uncertainty rather than silently acting.
- After `Safety & ethics`, add only the scenario-specific sections that make the agent's autonomous behavior more governable, auditable, or predictable.
- Add an output format section only when the agent produces recurring reports, tickets, PRs, messages, summaries, or other user-visible artifacts.
- Use scenario-specific sections for policies that do not belong cleanly in the foundation, such as severity/SLA tables, customer-state rules, ranking algorithms, report schemas, ticket templates, approval matrices, deduplication rules, or monitoring metrics.

## Interpreting Prompts

Map the prompt into constitution sections:

- Agent mission, cadence, and audience -> `Purpose`
- Things the agent may inspect, summarize, change, create, or send -> `In scope`
- Actions the agent must never perform or domains it must avoid -> `Out of scope` and `Never`
- Ambiguity, risk, escalations, high impact cases, missing access, or relationship-sensitive cases -> `Handoff conditions`
- Tools, apps, databases, files, APIs, calendars, repos, issue trackers, and CRMs -> `Data access`
- Autonomous writes and safe low-risk actions -> `Auto` or `Notify`
- Risky writes, irreversible changes, sensitive data, major business impact, or ambiguous cases -> `Approve`
- Scenario policies that mediate autonomous behavior -> scenario-specific sections after `Safety & ethics`
- Report shape, ticket schema, PR body, or briefing structure -> optional scenario-specific output section

## Quality Check

Before finalizing, verify that:

- The constitution can stand alone without the original prompt.
- Every autonomous action has a matching permission tier and a failure default.
- The `Never` tier contains hard blocks, not preferences.
- Sensitive data handling is explicit.
- Handoff conditions cover the most likely high-risk edge cases.
- Scenario-specific sections exist where they would make autonomous behavior easier to monitor, approve, audit, or troubleshoot.
- Any output format included is concrete enough that another agent could produce matching output.
