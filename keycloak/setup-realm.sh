#!/usr/bin/env bash
#
# setup-realm.sh — provision the "atryum" Keycloak realm from scratch via the
# Admin REST API. Idempotent: re-running on an existing realm only patches the
# pieces that drift.
#
# Each step has a comment showing the equivalent admin-UI path, so this script
# doubles as executable documentation for the manual setup.
#
# Usage:
#   docker compose up -d keycloak
#   ./keycloak/setup-realm.sh
#
# Env overrides:
#   KC_URL       (default http://localhost:8089)
#   KC_ADMIN     (default admin)
#   KC_PASSWORD  (default admin)
#   REALM        (default atryum)
#   KC_SETUP_SKIP_IF_REALM_EXISTS
#                (default false)         — exit without reconciling settings
#                                            when REALM already exists
#   CLIENT_ID    (default atryum)         — the confidential client used by
#                                            agents doing client_credentials
#   ADMIN_CLIENT_ID
#                (default atryum-admin)   — the public SPA client used by
#                                            Atryum's admin UI
#   SCOPE_NAME   (default atryum:mcp)     — resource scope, also the audience
#   AUDIENCE     (default atryum)         — literal value placed in `aud` claim

set -euo pipefail

KC_URL="${KC_URL:-http://localhost:8089}"
KC_ADMIN="${KC_ADMIN:-admin}"
KC_PASSWORD="${KC_PASSWORD:-admin}"
REALM="${REALM:-atryum}"
KC_SETUP_SKIP_IF_REALM_EXISTS="${KC_SETUP_SKIP_IF_REALM_EXISTS:-false}"
CLIENT_ID="${CLIENT_ID:-atryum}"
ADMIN_CLIENT_ID="${ADMIN_CLIENT_ID:-atryum-admin}"
SCOPE_NAME="${SCOPE_NAME:-atryum:mcp}"
AUDIENCE="${AUDIENCE:-atryum}"
REDIRECT_URI="${REDIRECT_URI:-http://localhost:8080/*}"
ADMIN_REDIRECT_URI_DEV="${ADMIN_REDIRECT_URI_DEV:-http://localhost:5174/ui/auth/callback}"
ADMIN_REDIRECT_URI_EMBEDDED="${ADMIN_REDIRECT_URI_EMBEDDED:-http://localhost:8080/ui/auth/callback}"

# Token lifespans (seconds). Demo-friendly defaults: 24h access tokens,
# 30-day sessions. Stock Keycloak defaults (5min access / 30min idle /
# 10hr max) are what causes OAuth-based MCP clients (opencode, Claude Code)
# to "expire fast" in dev. Override via env if you want different values.
ACCESS_TOKEN_LIFESPAN="${ACCESS_TOKEN_LIFESPAN:-86400}"
SSO_SESSION_IDLE_TIMEOUT="${SSO_SESSION_IDLE_TIMEOUT:-2592000}"
SSO_SESSION_MAX_LIFESPAN="${SSO_SESSION_MAX_LIFESPAN:-2592000}"
CLIENT_SESSION_IDLE_TIMEOUT="${CLIENT_SESSION_IDLE_TIMEOUT:-86400}"
CLIENT_SESSION_MAX_LIFESPAN="${CLIENT_SESSION_MAX_LIFESPAN:-2592000}"

need() { command -v "$1" >/dev/null 2>&1 || { echo "missing: $1" >&2; exit 1; }; }
need curl
need jq

log() { printf "\033[1;36m==>\033[0m %s\n" "$*"; }
warn() { printf "\033[1;33m!!\033[0m %s\n" "$*" >&2; }

# Step 1 — UI: http://localhost:8089 → log in as admin/admin
log "Acquiring admin token..."
TOKEN=$(curl -fsS -X POST "${KC_URL}/realms/master/protocol/openid-connect/token" \
  -d "client_id=admin-cli&grant_type=password&username=${KC_ADMIN}&password=${KC_PASSWORD}" \
  | jq -r .access_token)
[ -n "$TOKEN" ] && [ "$TOKEN" != "null" ] || { echo "admin login failed" >&2; exit 1; }

api() {
  local method="$1" path="$2"; shift 2
  curl -sS -X "$method" \
    -H "Authorization: Bearer $TOKEN" \
    -H "Content-Type: application/json" \
    "${KC_URL}/admin/realms${path}" "$@"
}

# Step 2 — UI: top-left realm switcher → "Create realm" → name "atryum"
if api GET "/${REALM}" -o /dev/null -w "%{http_code}" | grep -q 200; then
  log "Realm '${REALM}' already exists."
  if [ "$KC_SETUP_SKIP_IF_REALM_EXISTS" = "true" ]; then
    log "Skipping setup because KC_SETUP_SKIP_IF_REALM_EXISTS=true."
    exit 0
  fi
else
  log "Creating realm '${REALM}'..."
  api POST "" -d "{\"realm\":\"${REALM}\",\"enabled\":true}" >/dev/null
fi

REALM_ID=$(api GET "/${REALM}" | jq -r .id)

# Step 2b — UI: Realm Settings → Sessions / Tokens
#   Tune token & session lifespans for demos. Stock Keycloak defaults
#   (accessTokenLifespan=300s) are why OAuth MCP clients felt like they
#   "expired" after 5 minutes. With these settings a single auth lasts
#   ~30 days, with the access token itself good for 24h before refresh.
log "Tuning realm token/session lifespans (access=${ACCESS_TOKEN_LIFESPAN}s, sso_max=${SSO_SESSION_MAX_LIFESPAN}s)..."
api PUT "/${REALM}" -d "$(cat <<EOF
{
  "realm": "${REALM}",
  "accessTokenLifespan": ${ACCESS_TOKEN_LIFESPAN},
  "accessTokenLifespanForImplicitFlow": ${ACCESS_TOKEN_LIFESPAN},
  "ssoSessionIdleTimeout": ${SSO_SESSION_IDLE_TIMEOUT},
  "ssoSessionMaxLifespan": ${SSO_SESSION_MAX_LIFESPAN},
  "ssoSessionIdleTimeoutRememberMe": ${SSO_SESSION_IDLE_TIMEOUT},
  "ssoSessionMaxLifespanRememberMe": ${SSO_SESSION_MAX_LIFESPAN},
  "clientSessionIdleTimeout": ${CLIENT_SESSION_IDLE_TIMEOUT},
  "clientSessionMaxLifespan": ${CLIENT_SESSION_MAX_LIFESPAN}
}
EOF
)" >/dev/null

# Step 3 — UI: Client Scopes → Create client scope
#   Name: atryum:mcp
#   Type: Default
#   Settings → "Include in token scope": ON
#     (without this, the JWT's `scope` claim won't contain "atryum:mcp" and
#      Atryum's required_scope check fails with "missing required scope")
log "Ensuring client scope '${SCOPE_NAME}'..."
SCOPE_ID=$(api GET "/${REALM}/client-scopes" \
  | jq -r --arg n "$SCOPE_NAME" '.[] | select(.name==$n) | .id' | head -1)

SCOPE_BODY=$(cat <<EOF
{
  "name": "${SCOPE_NAME}",
  "protocol": "openid-connect",
  "attributes": {
    "include.in.token.scope": "true",
    "display.on.consent.screen": "true"
  }
}
EOF
)

if [ -z "$SCOPE_ID" ]; then
  api POST "/${REALM}/client-scopes" -d "$SCOPE_BODY" >/dev/null
  SCOPE_ID=$(api GET "/${REALM}/client-scopes" \
    | jq -r --arg n "$SCOPE_NAME" '.[] | select(.name==$n) | .id' | head -1)
else
  # PUT requires id in the body
  api PUT "/${REALM}/client-scopes/${SCOPE_ID}" \
    -d "$(echo "$SCOPE_BODY" | jq --arg id "$SCOPE_ID" '. + {id:$id}')" >/dev/null
fi

# Step 3b — UI: Client Scopes → atryum:mcp → Mappers → Add → By configuration
#                → Audience
#   Name: aud-atryum
#   Included Custom Audience: atryum   (leave Included Client Audience empty)
#   Add to access token: ON
#     (without `included.custom.audience` set, tokens won't have
#      `aud:"atryum"` and Atryum rejects with "issuer/audience not allowed")
log "Ensuring audience mapper on '${SCOPE_NAME}'..."
MAPPER_ID=$(api GET "/${REALM}/client-scopes/${SCOPE_ID}/protocol-mappers/models" \
  | jq -r '.[] | select(.protocolMapper=="oidc-audience-mapper") | .id' | head -1)

MAPPER_BODY=$(cat <<EOF
{
  "name": "aud-${AUDIENCE}",
  "protocol": "openid-connect",
  "protocolMapper": "oidc-audience-mapper",
  "consentRequired": false,
  "config": {
    "included.custom.audience": "${AUDIENCE}",
    "access.token.claim": "true",
    "introspection.token.claim": "true",
    "id.token.claim": "false",
    "userinfo.token.claim": "false",
    "lightweight.claim": "false"
  }
}
EOF
)

if [ -z "$MAPPER_ID" ]; then
  api POST "/${REALM}/client-scopes/${SCOPE_ID}/protocol-mappers/models" \
    -d "$MAPPER_BODY" >/dev/null
else
  api PUT "/${REALM}/client-scopes/${SCOPE_ID}/protocol-mappers/models/${MAPPER_ID}" \
    -d "$(echo "$MAPPER_BODY" | jq --arg id "$MAPPER_ID" '. + {id:$id}')" >/dev/null
fi

# Step 3c — UI: Realm Settings → Client Scopes → Default → add "atryum:mcp"
#   (makes every client automatically get this scope as a default scope, so
#    its mappers and `scope` claim entry are emitted on every token)
log "Adding '${SCOPE_NAME}' to realm default scopes..."
api PUT "/${REALM}/default-default-client-scopes/${SCOPE_ID}" >/dev/null

# Step 4 — UI: Clients → Create client
#   Client ID: atryum
#   Client authentication: ON  (confidential)
#   Authentication flow: Service accounts roles → ON
#   Valid redirect URIs: http://localhost:8080/*
log "Ensuring confidential client '${CLIENT_ID}'..."
ATRYUM_UUID=$(api GET "/${REALM}/clients?clientId=${CLIENT_ID}" | jq -r '.[0].id // empty')

CLIENT_BODY=$(cat <<EOF
{
  "clientId": "${CLIENT_ID}",
  "enabled": true,
  "protocol": "openid-connect",
  "publicClient": false,
  "serviceAccountsEnabled": true,
  "standardFlowEnabled": true,
  "directAccessGrantsEnabled": false,
  "redirectUris": ["${REDIRECT_URI}"],
  "attributes": {
    "oauth2.device.authorization.grant.enabled": "false"
  }
}
EOF
)

if [ -z "$ATRYUM_UUID" ]; then
  api POST "/${REALM}/clients" -d "$CLIENT_BODY" >/dev/null
  ATRYUM_UUID=$(api GET "/${REALM}/clients?clientId=${CLIENT_ID}" | jq -r '.[0].id')
else
  api PUT "/${REALM}/clients/${ATRYUM_UUID}" \
    -d "$(echo "$CLIENT_BODY" | jq --arg id "$ATRYUM_UUID" '. + {id:$id}')" >/dev/null
fi

# Fetch and display the client secret (needed for client_credentials grant)
SECRET=$(api GET "/${REALM}/clients/${ATRYUM_UUID}/client-secret" | jq -r .value)

# Step 4b — UI: Clients → Create client
#   Client ID: atryum-admin
#   Client authentication: OFF  (public SPA + PKCE)
#   Authentication flow: Standard flow → ON
#   Valid redirect URIs:
#     http://localhost:5174/ui/auth/callback
#     http://localhost:8080/ui/auth/callback
#   Web origins:
#     http://localhost:5174
#     http://localhost:8080
log "Ensuring public admin UI client '${ADMIN_CLIENT_ID}'..."
ADMIN_UUID=$(api GET "/${REALM}/clients?clientId=${ADMIN_CLIENT_ID}" | jq -r '.[0].id // empty')

ADMIN_CLIENT_BODY=$(cat <<EOF
{
  "clientId": "${ADMIN_CLIENT_ID}",
  "enabled": true,
  "protocol": "openid-connect",
  "publicClient": true,
  "serviceAccountsEnabled": false,
  "standardFlowEnabled": true,
  "implicitFlowEnabled": false,
  "directAccessGrantsEnabled": false,
  "redirectUris": ["${ADMIN_REDIRECT_URI_DEV}", "${ADMIN_REDIRECT_URI_EMBEDDED}"],
  "webOrigins": ["http://localhost:5174", "http://localhost:8080"],
  "attributes": {
    "pkce.code.challenge.method": "S256",
    "oauth2.device.authorization.grant.enabled": "false"
  }
}
EOF
)

if [ -z "$ADMIN_UUID" ]; then
  api POST "/${REALM}/clients" -d "$ADMIN_CLIENT_BODY" >/dev/null
  ADMIN_UUID=$(api GET "/${REALM}/clients?clientId=${ADMIN_CLIENT_ID}" | jq -r '.[0].id')
else
  api PUT "/${REALM}/clients/${ADMIN_UUID}" \
    -d "$(echo "$ADMIN_CLIENT_BODY" | jq --arg id "$ADMIN_UUID" '. + {id:$id}')" >/dev/null
fi

# Step 4c — UI: Clients → atryum-admin → Client scopes → Dedicated scope
#              → Mappers → Add mapper → By configuration → Hardcoded claim
#   Token claim name: atryum_admin
#   Claim value: true
#   Claim JSON type: boolean
#   Add to access token: ON
log "Ensuring admin claim mapper on '${ADMIN_CLIENT_ID}'..."
ADMIN_MAPPER_ID=$(api GET "/${REALM}/clients/${ADMIN_UUID}/protocol-mappers/models" \
  | jq -r '.[] | select(.name=="atryum-admin-claim") | .id' | head -1)

ADMIN_MAPPER_BODY='{
  "name": "atryum-admin-claim",
  "protocol": "openid-connect",
  "protocolMapper": "oidc-hardcoded-claim-mapper",
  "consentRequired": false,
  "config": {
    "claim.name": "atryum_admin",
    "claim.value": "true",
    "jsonType.label": "boolean",
    "access.token.claim": "true",
    "id.token.claim": "false",
    "userinfo.token.claim": "false",
    "introspection.token.claim": "true"
  }
}'

if [ -z "$ADMIN_MAPPER_ID" ]; then
  api POST "/${REALM}/clients/${ADMIN_UUID}/protocol-mappers/models" \
    -d "$ADMIN_MAPPER_BODY" >/dev/null
else
  api PUT "/${REALM}/clients/${ADMIN_UUID}/protocol-mappers/models/${ADMIN_MAPPER_ID}" \
    -d "$(echo "$ADMIN_MAPPER_BODY" | jq --arg id "$ADMIN_MAPPER_ID" '. + {id:$id}')" >/dev/null
fi

# Step 5 — UI: Clients → Client Registration → Client Registration Policies
#   Under "Anonymous" policies:
#     - DELETE "Trusted Hosts"  (blocks DCR from non-listed hosts; too
#       restrictive for agents that can come from any IP — safe to remove
#       in dev, but in production prefer to whitelist agent IP ranges)
log "Removing 'Trusted Hosts' anonymous DCR policy..."
TRUSTED_ID=$(api GET "/${REALM}/components?type=org.keycloak.services.clientregistration.policy.ClientRegistrationPolicy" \
  | jq -r '.[] | select(.name=="Trusted Hosts" and .subType=="anonymous") | .id')
if [ -n "$TRUSTED_ID" ]; then
  api DELETE "/${REALM}/components/${TRUSTED_ID}" >/dev/null
else
  log "  (already removed)"
fi

# Step 5b — UI: same screen → click "Allowed Client Scopes" (anonymous)
#   Enable "Allow Default Scopes": ON
#   Allowed Client Scopes: openid, atryum:mcp, offline_access, profile, email
#     (openid and offline_access aren't realm-default scopes, so they're
#      blocked unless listed here — MCP SDKs request all five during DCR)
log "Updating 'Allowed Client Scopes' anonymous DCR policy..."
ALLOWED_ID=$(api GET "/${REALM}/components?type=org.keycloak.services.clientregistration.policy.ClientRegistrationPolicy" \
  | jq -r '.[] | select(.name=="Allowed Client Scopes" and .subType=="anonymous") | .id')

if [ -z "$ALLOWED_ID" ]; then
  warn "Allowed Client Scopes (anonymous) not found — skipping. DCR may fail."
else
  PARENT_ID=$(api GET "/${REALM}/components/${ALLOWED_ID}" | jq -r .parentId)
  api PUT "/${REALM}/components/${ALLOWED_ID}" -d "$(cat <<EOF
{
  "id": "${ALLOWED_ID}",
  "name": "Allowed Client Scopes",
  "providerId": "allowed-client-templates",
  "providerType": "org.keycloak.services.clientregistration.policy.ClientRegistrationPolicy",
  "parentId": "${PARENT_ID}",
  "subType": "anonymous",
  "config": {
    "allow-default-scopes": ["true"],
    "allowed-client-scopes": ["openid", "${SCOPE_NAME}", "profile", "email"]
  }
}
EOF
)" >/dev/null
fi

# Step 5c — Delete the realm's `offline_access` client scope.
# WHY: Claude Code's MCP SDK auto-appends `offline_access` to its
# authorization request whenever the AS advertises it in `scopes_supported`
# (see https://code.claude.com/docs/en/mcp#restrict-oauth-scopes). Keycloak's
# DCR doesn't auto-merge realm defaults when the SDK supplies a `scope`
# field, so the DCR-registered client never has `offline_access` assigned,
# and the auth endpoint rejects with `Invalid scopes: atryum:mcp
# offline_access`. Deleting the scope from the realm removes it from
# `scopes_supported`, so Claude Code no longer appends it and the auth
# request becomes `scope=atryum:mcp` — which works. Refresh tokens still
# work in their default (non-offline) form. Codex doesn't request
# offline_access at all, so this isn't needed for Codex but doesn't hurt.
# UI equivalent: Client Scopes → offline_access → "Delete".
log "Removing 'offline_access' client scope from realm..."
OFFLINE_ID=$(api GET "/${REALM}/client-scopes" \
  | jq -r '.[] | select(.name=="offline_access") | .id' | head -1)
if [ -n "$OFFLINE_ID" ]; then
  api DELETE "/${REALM}/client-scopes/${OFFLINE_ID}" >/dev/null
else
  log "  (already removed)"
fi

# Step 6 — UI: Users → Add user
#   Username: user
#   Email: user@example.com (Email verified: ON to skip verify-email flow)
#   First name: Test, Last name: User
#   After save → Credentials tab → Set password: "password", Temporary: OFF
log "Ensuring default user 'user'..."
USER_BODY='{
  "username": "user",
  "enabled": true,
  "emailVerified": true,
  "email": "user@example.com",
  "firstName": "Test",
  "lastName": "User"
}'
USER_ID=$(api GET "/${REALM}/users?username=user&exact=true" | jq -r '.[0].id // empty')
if [ -z "$USER_ID" ]; then
  api POST "/${REALM}/users" \
    -d "$(echo "$USER_BODY" | jq '. + {credentials:[{type:"password",value:"password",temporary:false}]}')" >/dev/null
  USER_ID=$(api GET "/${REALM}/users?username=user&exact=true" | jq -r '.[0].id')
else
  # Patch profile fields and reset the password (idempotent on re-run).
  api PUT "/${REALM}/users/${USER_ID}" \
    -d "$(echo "$USER_BODY" | jq --arg id "$USER_ID" '. + {id:$id}')" >/dev/null
  api PUT "/${REALM}/users/${USER_ID}/reset-password" \
    -d '{"type":"password","value":"password","temporary":false}' >/dev/null
fi

printf "\n\033[1;32m✔ Realm '%s' is set up.\033[0m\n" "${REALM}"
cat <<EOF

  Issuer:        ${KC_URL}/realms/${REALM}
  Confidential client:
    client_id:     ${CLIENT_ID}
    client_secret: ${SECRET}
    grant:         client_credentials  (scope=${SCOPE_NAME})

  Admin UI public client:
    client_id:     ${ADMIN_CLIENT_ID}
    grant:         authorization_code + PKCE
    claim:         atryum_admin=true

  MCP clients can also self-register via Dynamic Client Registration at:
    ${KC_URL}/realms/${REALM}/clients-registrations/openid-connect

  Default user (for browser-based authorization-code flows):
    username: user
    password: password

  Atryum config (atryum.toml):
    [[auth]]
    issuer         = "http://keycloak:8089/realms/${REALM}"   # use docker hostname
    audience       = "${AUDIENCE}"
    required_scope = "${SCOPE_NAME}"
    agent_id_claim = "client_id"
    admin_enabled  = true
    admin_provider = "keycloak"
    admin_client_id = "${ADMIN_CLIENT_ID}"
    admin_scopes = "openid profile email ${SCOPE_NAME}"
    admin_claim = "atryum_admin"
    admin_claim_value = true

EOF
