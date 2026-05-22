# Running Atryum With the ValidMind Backend in Docker Compose

This guide explains the local Docker Compose override setup used when Atryum and the ValidMind backend need to run together on one Mac.

It is written for developers who are comfortable with application code but do not live in Docker every day.

## Why This Exists

Atryum and the backend each have their own `docker-compose` stack. If you start both stacks without coordination, you get three practical problems:

- Both stacks try to publish Keycloak on `localhost:8088`.
- Each stack gets its own isolated Docker network, so containers cannot reliably call each other by service name.
- Local URLs become confusing because host-facing URLs use `localhost`, while container-to-container URLs must use Docker service names.

The local override files solve those problems by making both Compose projects join one shared Docker network and by making only one stack publish Keycloak.

This document primarily describes a hybrid local setup:

- The ValidMind backend API and frontend authenticate users against Auth0.
- The backend Compose stack still runs the Keycloak container.
- Atryum's `atryum` Keycloak realm is provisioned into that backend-owned Keycloak container.
- Atryum uses the shared Keycloak for local MCP/OAuth flows and uses the backend API over the shared Docker network.

This works well when you want one Keycloak process and one shared network, while keeping the backend itself pointed at Auth0.

If your backend is purely Auth0-backed and you only need Keycloak for Atryum, the cleaner setup is usually the inverse: keep Atryum's Keycloak and override out the backend Keycloak services. That avoids running the backend Keycloak just to host Atryum's realm. See [Alternative: Keep Atryum Keycloak](#alternative-keep-atryum-keycloak).

## Auth Ownership Models

There are two reasonable ways to avoid duplicate local Keycloak containers.

| Model | Backend Auth | Atryum Auth | Which Keycloak Runs | Best When |
|---|---|---|---|---|
| Hybrid shared backend Keycloak | Auth0 | Atryum realm in backend Keycloak | Backend Keycloak | You want to use the backend stack's existing Keycloak container as shared local auth infrastructure |
| Atryum-owned Keycloak | Auth0 | Atryum Keycloak | Atryum Keycloak | The backend does not need Keycloak locally and should stay Auth0-only |

The override files shown below implement the hybrid shared backend Keycloak model.

## What the Overrides Do

Docker Compose automatically reads `docker-compose.override.yml` from the same directory as `docker-compose.yml` or `docker-compose.yaml`. You do not need to pass `-f docker-compose.override.yml` manually.

In `atryum/docker-compose.override.yml`:

```yaml
networks:
  default:
    name: atryum-llm-judge-test-net
    external: true

services:
  keycloak:
    profiles: !override ["atryum-only-disabled"]
  keycloak-init:
    profiles: !override ["atryum-only-disabled"]
  atryum:
    depends_on: !override
      postgres:
        condition: service_healthy
```

This does three things for the hybrid model:

- Uses the external Docker network named `atryum-llm-judge-test-net` instead of Atryum's standalone `atryum-net`.
- Disables Atryum's local Keycloak and `keycloak-init` services by moving them to a profile you do not run.
- Removes Atryum's startup dependency on `keycloak-init`, because the backend stack is now responsible for running the Keycloak container.

The `!override` tags matter. Without them, Compose may merge lists or maps instead of replacing them, which can leave the original `dev` profile or `keycloak-init` dependency active.

In `backend/docker-compose.override.yml`:

```yaml
networks:
  default:
    name: atryum-llm-judge-test-net
    external: true
```

This puts backend services on the same Docker network as Atryum.

## Resulting Local Topology

From your Mac browser or command line, use `localhost` and the published ports:

| Service | Mac URL |
|---|---|
| Atryum API | `http://localhost:8080` |
| Atryum UI | `http://localhost:5175` |
| Backend API | `http://localhost:5000` |
| Backend Keycloak | `http://localhost:8088` |

From inside containers, use Docker service names on the shared network:

| Caller | Target | Container URL |
|---|---|---|
| Atryum container | Backend API | `http://api:5000` |
| Backend container | Atryum API | `http://atryum:8080` |
| Atryum container | Shared Keycloak | `http://keycloak:8088` |
| Atryum container | Atryum Postgres | `postgres:5432` |
| Backend container | Backend Postgres | `db:5432` |

The important rule is: `localhost` means "this same container" when code runs inside Docker. Use service names such as `api`, `atryum`, and `keycloak` for container-to-container traffic.

Port ownership is intentionally split:

| Port | Owner | Notes |
|---|---|---|
| `5000` | Backend API | Published by the backend stack |
| `8088` | Backend Keycloak | Atryum's Keycloak is disabled by the override |
| `9000` | Backend Keycloak management interface | Published by the backend stack |
| `8080` | Atryum API | Published by the Atryum stack |
| `5175` | Atryum Vite UI | Published by the Atryum stack |
| `5432` | Backend Postgres | Atryum Postgres is container-only via `expose`, so it does not take a Mac host port |

In the hybrid setup, "Backend Keycloak" means the Keycloak container from the backend Compose stack. It does not mean the backend API has to authenticate against Keycloak. The backend API can still use Auth0 while this Keycloak container hosts only the local Atryum realm.

## Mac Prerequisites

- Install and start Docker Desktop for Mac.
- Use Docker Compose v2.24 or newer. The override uses `!override`, which older Compose versions do not understand.
- On Apple Silicon Macs, expect backend images marked `platform: linux/amd64` to run under emulation and build more slowly.
- Install `jq` if you need to run Atryum's Keycloak setup script from macOS: `brew install jq`.

Check your Compose version:

```bash
docker compose version
```

If Compose fails with an error about `!override`, update Docker Desktop.

## One-Time Setup

Create the shared external network before starting either stack:

```bash
docker network inspect atryum-llm-judge-test-net >/dev/null 2>&1 || docker network create atryum-llm-judge-test-net
```

Compose will fail fast if the network does not exist because both override files mark it as `external: true`.

## Start the Backend Stack

From the backend repo:

```bash
cd /path/to/backend
docker compose --profile headless up -d --wait --build api keycloak
```

This starts the backend API, its database and Redis dependencies, migrations, and the backend-owned Keycloak container. The `headless` profile avoids starting the backend frontend unless you need it.

For the hybrid setup, configure the backend API to use Auth0 as its primary OIDC provider. In `backend/vmconfig_local.toml`, that means the local Keycloak `[[oidc_config]]` stays commented out or disabled, and the Auth0 `[[oidc_config]]` is primary:

```toml
# Local Keycloak is disabled for backend API/frontend login.
#[[oidc_config]]
#type = "keycloak"
#keycloak_server_url = "http://keycloak:8088"
#is_primary_config = true

# Auth0 is the primary OIDC provider for backend API/frontend login.
[[oidc_config]]
name = "<auth0 config name>"
userinfo_endpoint = "https://<your-auth0-custom-domain>/.well-known/openid-configuration"
api_audience = "https://api.dev.vm.validmind.ai"
type = "auth0"
auth0_domain = "<your-tenant>.us.auth0.com"
auth0_client_id = "<auth0 client id>"
auth0_client_secret = "<auth0 client secret>"
can_modify_oidc = true
is_primary_config = true
```

Do not copy real Auth0 client secrets into documentation or shared branches.

Keycloak should be available on your Mac at:

```text
http://localhost:8088
```

The local admin login is usually:

```text
username: admin
password: admin
```

## Provision the Atryum Realm in the Shared Keycloak

Atryum's Keycloak container is disabled in this shared setup, but Atryum still needs an `atryum` realm. Create or reconcile that realm in the backend-owned Keycloak by running Atryum's setup script from the Atryum repo:

```bash
cd /path/to/atryum
KC_URL=http://localhost:8088 REALM=atryum ./keycloak/setup-realm.sh
```

The script is idempotent. You can re-run it after Keycloak changes to reconcile the realm, client scope, audience mapper, Dynamic Client Registration policy, and default test user.

Conceptually, this is the "yeet the Atryum realm into backend Keycloak" step. The backend Keycloak ends up with at least two possible realms:

| Realm | Used By | Notes |
|---|---|---|
| `vmlocal` | Backend local Keycloak mode | Not used by the backend API when Auth0 is primary |
| `atryum` | Atryum local MCP/OAuth flows | Created by `atryum/keycloak/setup-realm.sh` |

The backend API does not need to trust the `atryum` realm for this setup. Atryum validates tokens from that realm for `/mcp/`, while Atryum calls backend internal endpoints using machine-user credentials.

The script prints an Atryum config snippet at the end. Keep the issuer as the Docker hostname when Atryum runs in Compose:

```toml
[[auth]]
enabled = true
issuer = "http://keycloak:8088/realms/atryum"
audience = "atryum"
required_scope = "atryum:mcp"
agent_id_claim = "client_id"
```

Use `http://localhost:8088/realms/atryum` only from your Mac. Use `http://keycloak:8088/realms/atryum` inside containers.

## Configure Atryum for Backend Crosstalk

In `atryum/atryum.toml`, point Atryum at the backend API by Docker service name:

```toml
[backend]
base_url = "http://api:5000"
machine_key = "<backend machine key>"
machine_secret = "<backend machine secret>"
connection_timeout_seconds = 5
```

Do not use `http://localhost:5000` here when Atryum runs in Docker. Inside the Atryum container, `localhost` is the Atryum container itself, not the backend API container.

For local AI evaluation testing, also configure the agent sync and evaluation fields as needed:

```toml
[agent_sync]
org_cuid = "<org cuid>"
agent_record_type_slug = "ai-agents"

[ai_evaluation]
constitution_field_key = "constitution"
```

Do not commit real machine credentials in shared branches.

## Start the Atryum Stack

From the Atryum repo:

```bash
cd /path/to/atryum
docker compose --profile dev up -d --wait --build
```

Or use the project shortcut:

```bash
just up
```

Because `docker-compose.override.yml` is present, this starts Atryum, Atryum's Postgres, and the Atryum frontend, but not Atryum's own Keycloak.

Open the Atryum UI:

```text
http://localhost:5175
```

## Optional Auth0 Configuration

If you are testing with Auth0 tokens instead of local Keycloak tokens, configure Atryum with the same issuer and audience values used by the backend. Atryum validates JWT access tokens by issuer, audience, JWKS signature, and expiry.

For browser Auth0 access tokens, leave `required_scope` empty because those tokens may contain scopes such as `openid profile email` or may omit the `scope` claim entirely:

```toml
[[auth]]
enabled = true
issuer = "https://login.dev.vm.validmind.ai/"
audience = "https://api.dev.vm.validmind.ai"
jwks_url = "https://login.dev.vm.validmind.ai/.well-known/jwks.json"
required_scope = ""
agent_id_claim = "azp"

[[auth]]
enabled = true
issuer = "https://dev-bpojk7up.us.auth0.com/"
audience = "https://api.dev.vm.validmind.ai"
jwks_url = "https://dev-bpojk7up.us.auth0.com/.well-known/jwks.json"
required_scope = ""
agent_id_claim = "azp"
```

Keep both issuer blocks only if you need to accept tokens from both the custom Auth0 domain and the tenant's default Auth0 domain.

## Alternative: Keep Atryum Keycloak

If the backend API and frontend are already Auth0-backed and do not need local Keycloak at all, prefer this setup:

- Keep Atryum's Keycloak and `keycloak-init` services enabled.
- Override out the backend Keycloak services instead.
- Keep the shared Docker network so Atryum and backend API can still call each other by service name.

In that model, do not use the Atryum override shown earlier to disable Atryum Keycloak. Instead, remove or rename `atryum/docker-compose.override.yml`, or create an Atryum override that only joins the shared network and leaves `keycloak`, `keycloak-init`, and the `atryum.depends_on` entries alone.

Example Atryum override for the Atryum-owned Keycloak model:

```yaml
networks:
  default:
    name: atryum-llm-judge-test-net
    external: true
```

Then disable the backend Keycloak services in `backend/docker-compose.override.yml`:

```yaml
networks:
  default:
    name: atryum-llm-judge-test-net
    external: true

services:
  keycloak:
    profiles: !override ["backend-keycloak-disabled"]
  keycloak-db-init:
    profiles: !override ["backend-keycloak-disabled"]
```

With this alternative, Keycloak still publishes `localhost:8088`, but that port belongs to the Atryum stack. The Atryum `keycloak-init` service provisions the `atryum` realm automatically as part of `docker compose --profile dev up`.

## Verifying the Setup

Check that both projects are attached to the same network:

```bash
docker network inspect atryum-llm-judge-test-net
```

You should see containers from both repos in the network's `Containers` section.

Check backend health from your Mac:

```bash
curl http://localhost:5000/api/v1/health
```

Check Atryum health from your Mac:

```bash
curl http://localhost:8080/healthz
```

Check Atryum's logs for the backend connection check:

```bash
cd /path/to/atryum
docker compose --profile dev logs -f atryum
```

If `[backend].base_url` is set, Atryum calls the backend at startup. A failure there usually means the backend API is not running, the shared network is missing, the URL uses `localhost` by mistake, or the machine credentials are wrong.

## Stopping the Stacks

Stop Atryum:

```bash
cd /path/to/atryum
docker compose --profile dev down
```

Stop the backend:

```bash
cd /path/to/backend
docker compose --profile headless down
```

The external network is intentionally not removed by `docker compose down`. Remove it only after both stacks are stopped:

```bash
docker network rm atryum-llm-judge-test-net
```

Use `down -v` only when you intentionally want to delete local Docker volumes and reset persisted databases.

## Troubleshooting

| Symptom | Likely Cause | Fix |
|---|---|---|
| `network atryum-llm-judge-test-net declared as external, but could not be found` | The shared network was not created | Run the one-time `docker network create` command |
| Compose fails on `!override` | Docker Compose is too old | Update Docker Desktop |
| Port `8088` is already allocated | Both Keycloak stacks are running, or an old container is still up | Stop the extra stack and make sure Atryum's override is being read |
| Atryum cannot reach backend | `backend.base_url` uses `localhost`, backend API is not running, or stacks are on different networks | Use `http://api:5000`, start backend `api`, and inspect the shared network |
| Atryum rejects local Keycloak tokens | Issuer, audience, or scope does not match Atryum config | Re-run `./keycloak/setup-realm.sh` and confirm `issuer`, `audience`, and `required_scope` |
| Browser can open `localhost:8088`, but Atryum cannot use that issuer | `localhost` means different things on Mac and inside Docker | Configure Atryum with `http://keycloak:8088/realms/atryum` |
| Backend and Atryum both have a service named `frontend` | Docker DNS aliases on a shared network can be ambiguous for duplicate service names | Do not use `frontend` for container-to-container calls; use the APIs by `api` and `atryum` |
