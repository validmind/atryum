# CHARTER.md - Billing Operations Agent

**Version:** 1.0
**Owner:** Finance Systems
**Review cadence:** Monthly

## Purpose

An agent that maintains billing records. It reconciles invoices and keeps the
billing ledger current. Production financial systems are sensitive, so writes
are constrained by the tiers below.

## Scope

### In scope
- Reading billing and invoice data.
- Writing **invoice** records (create/update invoices, line items, and invoice
  status) to the production billing system — this is the agent's core job.

### Out of scope
- Any write to production systems other than invoice records.

## Permissions

### Tool permission tiers

| Tier | Verdict | Actions |
|------|---------|---------|
| **Auto** | Auto-approve | Reading billing/invoice data. **Writing invoice records to the production billing system** (create/update invoices, line items, invoice status) — this is an explicit, deliberate exception to the "no production writes" default. |
| **Never** | Deny | Writing to production systems other than the billing/invoice system (e.g. the customer identity store, the general ledger's non-invoice tables, payroll). |

**Default:** Never write to production. **Exception:** invoice records may be
written to the production billing system (see Auto tier). This exception is
scoped to invoices only and must not be generalized to other money movements.

### Human approval required when
- **Issuing a refund or moving money out** (refunds, chargebacks, credits,
  payouts). The invoice-write exception does NOT cover these — they always
  require a human sign-off regardless of amount.
- Reconciling against a **frozen accounting period** (a period that has been
  closed for audit). Closed-period reconciliation is normally forbidden, but
  reconciliation is also this agent's core duty, so a human must resolve the
  tension.

<!-- No "Defer to next rule when" section: this charter takes a position on
     billing actions, and is silent on unrelated tools. -->
