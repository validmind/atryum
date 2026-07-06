#!/usr/bin/env bash

set -euo pipefail

# shellcheck source=lib/common.sh
source "$INTEGRATIONS_ROOT/lib/common.sh"
# shellcheck source=lib/yaml.sh
source "$INTEGRATIONS_ROOT/lib/yaml.sh"

start_mock_oidc() {
  ensure_python_deps
  local venv="$INTEGRATIONS_ROOT/.venv"

  log "Starting mock OIDC on :${MOCK_OIDC_PORT}"
  "$venv/bin/python" "$INTEGRATIONS_ROOT/auth/mock-oidc/mock_oidc.py" \
    --host 127.0.0.1 \
    --port "$MOCK_OIDC_PORT" \
    --env-file "$RUN_DIR/mock-oidc.env" \
    >"$RUN_DIR/mock-oidc.log" 2>&1 &
  echo $! >"$RUN_DIR/mock-oidc.pid"

  wait_for_http "http://127.0.0.1:${MOCK_OIDC_PORT}/healthz" 40 0.25 || {
    tail -n 30 "$RUN_DIR/mock-oidc.log" >&2 || true
    return 1
  }
  # shellcheck disable=SC1090
  source "$RUN_DIR/mock-oidc.env"
  export MOCK_OIDC_ISSUER MOCK_OIDC_TOKEN_URL MOCK_OIDC_DCR_URL MOCK_OIDC_CLIENT_ID MOCK_OIDC_CLIENT_SECRET
}

auth_protocol_status() {
  yaml_get_field "$INTEGRATIONS_ROOT/config/auth-protocols.yaml" auth_protocols "$1" status
}

auth_protocol_template() {
  local rel
  rel="$(yaml_get_field "$INTEGRATIONS_ROOT/config/auth-protocols.yaml" auth_protocols "$1" atryum_template)"
  echo "$INTEGRATIONS_ROOT/$rel"
}

auth_requires_mock_oidc() {
  local auth_id="$1"
  local py="${INTEGRATIONS_PYTHON:-python3}"
  "$py" - "$INTEGRATIONS_ROOT" "$auth_id" "$py" <<'PY'
import json, subprocess, sys
root, auth_id, py = sys.argv[1], sys.argv[2], sys.argv[3]
item = json.loads(subprocess.check_output([
    py, f"{root}/lib/registry.py",
    f"{root}/config/auth-protocols.yaml", "auth_protocols", auth_id,
], text=True))
reqs = item.get("requires") or []
print("yes" if "mock-oidc" in reqs else "no")
PY
}

build_mcp_url() {
  local auth_id="$1" harness_id="$2" server_name="$3"
  local base="$ATRYUM_URL/mcp/${server_name}"

  case "$auth_id" in
    no-auth)
      echo "${base}?agent_id=${harness_id}"
      ;;
    oauth-client-credentials|oauth-dcr|static-bearer)
      local token
      token="$(fetch_bearer_token "$auth_id")" || return 1
      # mcp-remote accepts OAuth bearer via query for some clients; primary path
      # is Authorization header via MCP_REMOTE_HEADERS (set by caller).
      export ATRYUM_BEARER_TOKEN="$token"
      echo "$base"
      ;;
    *)
      echo "$base"
      ;;
  esac
}

fetch_bearer_token() {
  local auth_id="$1"
  case "$auth_id" in
    oauth-client-credentials)
      curl -fsS -X POST "$MOCK_OIDC_TOKEN_URL" \
        -d "grant_type=client_credentials" \
        -d "client_id=${MOCK_OIDC_CLIENT_ID}" \
        -d "client_secret=${MOCK_OIDC_CLIENT_SECRET}" \
        -d "scope=atryum:mcp" \
        | python3 -c 'import json,sys; print(json.load(sys.stdin)["access_token"])'
      ;;
    oauth-dcr)
      local reg
      reg="$(curl -fsS -X POST "$MOCK_OIDC_DCR_URL" \
        -H 'Content-Type: application/json' \
        -d '{"client_name":"integration-dcr","grant_types":["client_credentials"],"token_endpoint_auth_method":"client_secret_post","scope":"atryum:mcp"}')"
      local client_id client_secret
      client_id="$(echo "$reg" | python3 -c 'import json,sys; print(json.load(sys.stdin)["client_id"])')"
      client_secret="$(echo "$reg" | python3 -c 'import json,sys; print(json.load(sys.stdin)["client_secret"])')"
      curl -fsS -X POST "$MOCK_OIDC_TOKEN_URL" \
        -d "grant_type=client_credentials" \
        -d "client_id=${client_id}" \
        -d "client_secret=${client_secret}" \
        -d "scope=atryum:mcp" \
        | python3 -c 'import json,sys; print(json.load(sys.stdin)["access_token"])'
      ;;
    static-bearer)
      [[ -n "${ATRYUM_STATIC_BEARER_TOKEN:-}" ]] || return 1
      echo "$ATRYUM_STATIC_BEARER_TOKEN"
      ;;
    *)
      return 1
      ;;
  esac
}

mcp_remote_headers_env() {
  local auth_id="$1"
  case "$auth_id" in
    oauth-client-credentials|oauth-dcr|static-bearer)
      local token
      token="$(fetch_bearer_token "$auth_id")" || return 1
      export MCP_REMOTE_HEADERS="Authorization: Bearer ${token}"
      ;;
    no-auth)
      unset MCP_REMOTE_HEADERS || true
      ;;
  esac
}

harness_token_command_env() {
  local auth_id="$1"
  unset ATRYUM_ACCESS_TOKEN ATRYUM_TOKEN_COMMAND ATRYUM_TOKEN_COMMAND_TIMEOUT_MS ATRYUM_TOKEN_REFRESH_SKEW_MS || true

  case "$auth_id" in
    no-auth)
      return 0
      ;;
    oauth-client-credentials)
      local script="$RUN_DIR/token-command-${auth_id}.sh"
      cat >"$script" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
curl -fsS -X POST "$MOCK_OIDC_TOKEN_URL" \
  -d "grant_type=client_credentials" \
  -d "client_id=$MOCK_OIDC_CLIENT_ID" \
  -d "client_secret=$MOCK_OIDC_CLIENT_SECRET" \
  -d "scope=atryum:mcp"
EOF
      chmod 700 "$script"
      export ATRYUM_TOKEN_COMMAND="$script"
      ;;
    oauth-dcr)
      local reg client_id client_secret script="$RUN_DIR/token-command-${auth_id}.sh"
      reg="$(curl -fsS -X POST "$MOCK_OIDC_DCR_URL" \
        -H 'Content-Type: application/json' \
        -d '{"client_name":"integration-harness","grant_types":["client_credentials"],"token_endpoint_auth_method":"client_secret_post","scope":"atryum:mcp"}')"
      client_id="$(echo "$reg" | python3 -c 'import json,sys; print(json.load(sys.stdin)["client_id"])')"
      client_secret="$(echo "$reg" | python3 -c 'import json,sys; print(json.load(sys.stdin)["client_secret"])')"
      cat >"$script" <<EOF
#!/usr/bin/env bash
set -euo pipefail
curl -fsS -X POST "\$MOCK_OIDC_TOKEN_URL" \\
  -d "grant_type=client_credentials" \\
  -d "client_id=${client_id}" \\
  -d "client_secret=${client_secret}" \\
  -d "scope=atryum:mcp"
EOF
      chmod 700 "$script"
      export ATRYUM_TOKEN_COMMAND="$script"
      ;;
    static-bearer)
      [[ -n "${ATRYUM_STATIC_BEARER_TOKEN:-}" ]] || return 1
      export ATRYUM_ACCESS_TOKEN="$ATRYUM_STATIC_BEARER_TOKEN"
      ;;
  esac
}
