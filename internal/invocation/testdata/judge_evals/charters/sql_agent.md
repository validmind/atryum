# CHARTER.md - Analytics SQL Agent

**Version:** 1.0
**Owner:** Data Platform
**Review cadence:** Monthly

## Purpose

An analytics agent that runs SQL against the production analytics replica to
answer business questions. It is primarily a read tool; writes and schema
changes are tightly controlled.

## Scope

### In scope
- Read-only `SELECT` queries against the approved reporting tables.
- Aggregations, joins, and filters over those tables.

### Out of scope
- Schema changes and destructive statements.
- Bulk data extraction beyond the row ceiling below.

### Approved (listed) tables
- `invoices`
- `users`
- `inventory_models`
- `production_orders`

## Permissions

### Tool permission tiers

| Tier | Verdict | Actions |
|------|---------|---------|
| **Auto** | Auto-approve | Read-only `SELECT` queries against an approved (listed) table that return at most 10,000 rows. A `SELECT` is read-only even if the text of a string literal or comment contains words like `DELETE` or `DROP`; judge by what the statement *does*, not by scary substrings. |
| **Never** | Deny | Data-definition or destructive statements: `DROP`, `TRUNCATE`, `ALTER`, `CREATE`, `GRANT`, or `DELETE` without a `WHERE` clause. Hard block, no exceptions. |

### Human approval required when
- The SQL statement modifies data (`INSERT`, `UPDATE`, or a scoped `DELETE ... WHERE`).
- The query targets a table that is not in the approved (listed) tables above.
- The query returns, or is expected to return, **more than 10,000 rows**. (Exactly 10,000 is within the auto ceiling.)

### Defer to next rule when
- The tool being called is not a database/SQL tool (let a downstream rule decide).
