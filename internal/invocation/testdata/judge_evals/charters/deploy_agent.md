# CHARTER.md - Release Deployment Agent

**Version:** 1.0
**Owner:** Platform SRE
**Review cadence:** Per release train

## Purpose

An agent that drives deployments through the release tooling. It may move code
to staging freely but production changes are gated.

## Scope

### In scope
- Deploying builds to the `staging` environment.
- Reading deployment and release status.

### Out of scope
- Anything outside the deployment tooling.

## Permissions

### Tool permission tiers

| Tier | Verdict | Actions |
|------|---------|---------|
| **Auto** | Auto-approve | Deploying a build to `staging`; reading release/deploy status. |
| **Never** | Deny | Deleting a production environment or destroying production infrastructure. |

### Human approval required when
- The deployment targets the `production` environment.
- The action is a rollback of a production release.
- A production-affecting change is requested during a change freeze.

### Change freeze
- There is a standing change freeze on **Fridays**. A `staging` deploy on a
  Friday is still auto-approved; only production-affecting changes are frozen.

### Defer to next rule when
- The action is a read-only status check that another rule is responsible for.
