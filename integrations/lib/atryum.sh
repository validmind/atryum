#!/usr/bin/env bash

set -euo pipefail

# shellcheck source=lib/common.sh
source "$INTEGRATIONS_ROOT/lib/common.sh"
# shellcheck source=lib/yaml.sh
source "$INTEGRATIONS_ROOT/lib/yaml.sh"

render_upstream_block() {
  local target_id="$1"
  local py="${INTEGRATIONS_PYTHON:-python3}"
  "$py" - "$INTEGRATIONS_ROOT" "$target_id" <<'PY'
import json, os, subprocess, sys
root, target_id = sys.argv[1], sys.argv[2]
py = os.environ.get("INTEGRATIONS_PYTHON", "python3")
os.environ.setdefault("INTEGRATIONS_PYTHON", py)
item = json.loads(subprocess.check_output([
    py, f"{root}/lib/registry.py",
    f"{root}/config/mcp-targets.yaml", "mcp_targets", target_id,
], text=True))
u = item["upstream"]
lines = [
    "[[upstreams]]",
    f'name = "{u["name"]}"',
    f'mode = "{u["mode"]}"',
]
if u.get("command"):
    cmd = u["command"]
    if cmd == "__INTEGRATIONS_PYTHON__":
        cmd = os.environ.get("INTEGRATIONS_PYTHON", "python3")
    lines.append(f'command = "{cmd}"')
args = u.get("args") or []
if args:
    quoted = ", ".join(f'"{a}"' for a in args)
    lines.append(f"args = [{quoted}]")
if u.get("base_url"):
    lines.append(f'base_url = "{u["base_url"]}"')
lines.append(f'timeout_seconds = {u.get("timeout_seconds", 30)}')
lines.append(f'enabled = {"true" if u.get("enabled", True) else "false"}')
print("\n".join(lines))
PY
}

render_atryum_config() {
  local auth_id="$1" target_id="$2" template_path="$3" out_path="$4"
  local upstreams
  upstreams="$(render_upstream_block "$target_id")"
  sed \
    -e "s|__ATRYUM_PORT__|${ATRYUM_PORT}|g" \
    -e "s|__RUN_DIR__|${RUN_DIR}|g" \
    -e "s|__MOCK_OIDC_ISSUER__|${MOCK_OIDC_ISSUER:-http://127.0.0.1:${MOCK_OIDC_PORT}/realms/atryum}|g" \
    "$template_path" \
    | awk -v block="$upstreams" '{gsub(/__UPSTREAMS_BLOCK__/, block); print}' \
    >"$out_path"
}

build_atryum() {
  if [[ -x "$REPO_ROOT/atryum" ]]; then
    ATRYUM_BIN="$REPO_ROOT/atryum"
    return 0
  fi
  log "Building atryum binary..."
  (cd "$REPO_ROOT" && go build -o "$RUN_DIR/atryum" ./cmd/atryum)
  ATRYUM_BIN="$RUN_DIR/atryum"
}

start_atryum() {
  local config_path="$1"
  build_atryum
  log "Starting atryum on :${ATRYUM_PORT} (config: $config_path)"
  env \
    -u ANTHROPIC_API_KEY \
    -u ATRYUM_MANAGED_AGENTS_API_KEY \
    -u ATRYUM_MANAGED_AGENTS_WORKSPACE \
    ATRYUM_MCP_DEBUG="${ATRYUM_MCP_DEBUG:-1}" \
    "$ATRYUM_BIN" run -config "$config_path" \
    >"$RUN_DIR/atryum.log" 2>&1 &
  echo $! >"$RUN_DIR/atryum.pid"
  wait_for_http "$ATRYUM_URL/healthz" 80 0.25 || {
    warn "atryum failed to become healthy; tailing log:"
    tail -n 40 "$RUN_DIR/atryum.log" >&2 || true
    return 1
  }
}

seed_auto_approve_rules() {
  log "Seeding catch-all auto_approve rule via operator API"
  local payload
  payload='{
    "action": "auto_approve",
    "server_patterns": ["*"],
    "tool_patterns": ["*"],
    "description": "integration test catch-all auto-approve"
  }'
  curl -fsS -X POST "$ATRYUM_URL/api/v1/rules" \
    -H 'Content-Type: application/json' \
    -d "$payload" >/dev/null || {
    warn "failed to create auto_approve rule; tailing atryum log:"
    tail -n 20 "$RUN_DIR/atryum.log" >&2 || true
    return 1
  }
}

verify_upstream_direct() {
  local target_id="$1"
  local auth_id="${2:-no-auth}"
  local parsed server_name tool_name args_json expect_joined
  local py="${INTEGRATIONS_PYTHON:-python3}"
  parsed="$("$py" - "$INTEGRATIONS_ROOT" "$target_id" "$py" <<'PY'
import json, subprocess, sys
root, target_id, py = sys.argv[1], sys.argv[2], sys.argv[3]
item = json.loads(subprocess.check_output([
    py, f"{root}/lib/registry.py",
    f"{root}/config/mcp-targets.yaml", "mcp_targets", target_id,
], text=True))
v = item["verify"]
u = item["upstream"]
print(u["name"])
print(v["tool"])
print(json.dumps(v["arguments"]))
print("|".join(v["expect_substrings"]))
PY
)"
  server_name="$(echo "$parsed" | sed -n '1p')"
  tool_name="$(echo "$parsed" | sed -n '2p')"
  args_json="$(echo "$parsed" | sed -n '3p')"
  expect_joined="$(echo "$parsed" | sed -n '4p')"

  log "Direct MCP smoke via fake_agent.py (server=$server_name tool=$tool_name auth=$auth_id)"
  local bearer_args=()
  case "$auth_id" in
    oauth-client-credentials|oauth-dcr|static-bearer)
      local token
      token="$(fetch_bearer_token "$auth_id")" || return 1
      bearer_args=(--bearer "$token")
      ;;
  esac
  local output
  output="$(
    ATRYUM_URL="$ATRYUM_URL" \
    python3 "$REPO_ROOT/scripts/fake_agent.py" mcp \
      "$server_name" \
      --tool "$tool_name" \
      --arguments "$args_json" \
      "${bearer_args[@]}" 2>&1
  )" || return 1

  local part
  IFS='|' read -ra parts <<<"$expect_joined"
  for part in "${parts[@]}"; do
    if [[ "$output" != *"$part"* ]]; then
      warn "expected substring '$part' in output:"
      echo "$output" >&2
      return 1
    fi
  done
  log "Direct MCP verification passed"
}
