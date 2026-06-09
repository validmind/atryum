#!/usr/bin/env bash

set -euo pipefail

# shellcheck source=lib/common.sh
source "$INTEGRATIONS_ROOT/lib/common.sh"
# shellcheck source=lib/yaml.sh
source "$INTEGRATIONS_ROOT/lib/yaml.sh"
# shellcheck source=lib/auth.sh
source "$INTEGRATIONS_ROOT/lib/auth.sh"
# shellcheck source=lib/atryum.sh
source "$INTEGRATIONS_ROOT/lib/atryum.sh"

harness_skip_reason() {
  yaml_get_field "$INTEGRATIONS_ROOT/config/harnesses.yaml" harnesses "$1" skip_reason
}

harness_api_key_env() {
  yaml_get_field "$INTEGRATIONS_ROOT/config/harnesses.yaml" harnesses "$1" api_key_env
}

harness_available() {
  local harness_id="$1"
  local skip
  skip="$(harness_skip_reason "$harness_id")"
  if [[ -n "$skip" ]]; then
    warn "skip $harness_id: $skip"
    return 1
  fi

  if [[ "$harness_id" == "fake-agent" ]]; then
    return 0
  fi

  local cli
  cli="$(yaml_get_field "$INTEGRATIONS_ROOT/config/harnesses.yaml" harnesses "$harness_id" cli)"
  if [[ -z "$cli" ]]; then
    warn "skip $harness_id: no CLI configured"
    return 1
  fi
  command -v "$cli" >/dev/null 2>&1 || {
    warn "skip $harness_id: '$cli' not on PATH"
    return 1
  }

  local key_env key_optional
  key_env="$(harness_api_key_env "$harness_id")"
  key_optional="$(yaml_get_field "$INTEGRATIONS_ROOT/config/harnesses.yaml" harnesses "$harness_id" api_key_env_optional)"
  if [[ -n "$key_env" && -z "${!key_env:-}" ]]; then
    if [[ "$key_optional" == "true" ]]; then
      return 0
    fi
    # Amp may already be authenticated via `amp login` without AMP_API_KEY.
    if [[ "$harness_id" == "amp" ]] && amp threads list --json >/dev/null 2>&1; then
      return 0
    fi
    warn "skip $harness_id: env $key_env is unset"
    return 1
  fi
  return 0
}

install_hook() {
  local harness_id="$1"
  local hook_json
  hook_json="$(yaml_get_field "$INTEGRATIONS_ROOT/config/harnesses.yaml" harnesses "$harness_id" hook)"
  [[ -n "$hook_json" && "$hook_json" != "null" ]] || return 0

  local hook_type
  hook_type="$(echo "$hook_json" | python3 -c 'import json,sys; print(json.loads(sys.stdin.read()).get("type",""))')"

  case "$hook_type" in
    command)
      local script host source
      script="$(echo "$hook_json" | python3 -c 'import json,sys; print(json.loads(sys.stdin.read())["script"])')"
      host="$(echo "$hook_json" | python3 -c 'import json,sys; print(json.loads(sys.stdin.read())["host"])')"
      source="$(echo "$hook_json" | python3 -c 'import json,sys; print(json.loads(sys.stdin.read())["source"])')"
      local hook_dest="$HARNESS_CONFIG_DIR/atryum-hook.mjs"
      cp "$REPO_ROOT/examples/$script" "$hook_dest"
      chmod +x "$hook_dest"

      case "$source" in
        claude-code)
          mkdir -p "$HARNESS_CONFIG_DIR/claude"
          cat >"$HARNESS_CONFIG_DIR/claude/settings.json" <<EOF
{
  "hooks": {
    "PreToolUse": [{
      "matcher": "*",
      "hooks": [{
        "type": "command",
        "command": "ATRYUM_URL=${ATRYUM_URL} ATRYUM_HOOK_HOST=${host} ATRYUM_HOOK_EVENT=PreToolUse ATRYUM_SOURCE=${source} ATRYUM_AGENT_ID=${harness_id} node ${hook_dest}"
      }]
    }],
    "PostToolUse": [{
      "matcher": "*",
      "hooks": [{
        "type": "command",
        "command": "ATRYUM_URL=${ATRYUM_URL} ATRYUM_HOOK_HOST=${host} ATRYUM_HOOK_EVENT=PostToolUse ATRYUM_SOURCE=${source} ATRYUM_AGENT_ID=${harness_id} node ${hook_dest}"
      }]
    }]
  }
}
EOF
          export CLAUDE_CONFIG_DIR="$HARNESS_CONFIG_DIR/claude"
          ;;
        cursor)
          mkdir -p "$HARNESS_CONFIG_DIR/cursor"
          cat >"$HARNESS_CONFIG_DIR/cursor/hooks.json" <<EOF
{
  "hooks": {
    "preToolUse": [{
      "type": "command",
      "command": "ATRYUM_URL=${ATRYUM_URL} ATRYUM_HOOK_HOST=${host} ATRYUM_HOOK_EVENT=preToolUse ATRYUM_SOURCE=${source} ATRYUM_AGENT_ID=${harness_id} node ${hook_dest}"
    }],
    "postToolUse": [{
      "type": "command",
      "command": "ATRYUM_URL=${ATRYUM_URL} ATRYUM_HOOK_HOST=${host} ATRYUM_HOOK_EVENT=postToolUse ATRYUM_SOURCE=${source} ATRYUM_AGENT_ID=${harness_id} node ${hook_dest}"
    }]
  }
}
EOF
          export CURSOR_HOOKS_FILE="$HARNESS_CONFIG_DIR/cursor/hooks.json"
          ;;
      esac
      ;;
    plugin)
      if [[ "${INTEGRATION_AMP_MCP_ONLY:-}" == "1" && "$harness_id" == "amp" ]]; then
        warn "skipping amp plugin hook (INTEGRATION_AMP_MCP_ONLY=1)"
        return 0
      fi
      local script
      script="$(echo "$hook_json" | python3 -c 'import json,sys; print(json.loads(sys.stdin.read())["script"])')"
      mkdir -p "$HARNESS_CONFIG_DIR/amp/plugins"
      cp "$REPO_ROOT/examples/$script" "$HARNESS_CONFIG_DIR/amp/plugins/atryum.ts"
      export PLUGINS=all
      export AMP_PLUGIN_DIR="$HARNESS_CONFIG_DIR/amp/plugins"
      export ATRYUM_AGENT_ID="$harness_id"
      export ATRYUM_URL
      ;;
    external-invocations)
      warn "hook type external-invocations is documented only — no local install"
      ;;
  esac
}

configure_harness_mcp() {
  local harness_id="$1" auth_id="$2" target_id="$3"
  local template path transport server_name mcp_url dest py="${INTEGRATIONS_PYTHON:-python3}"
  template="$("$py" "$INTEGRATIONS_ROOT/lib/registry.py" \
    "$INTEGRATIONS_ROOT/config/harnesses.yaml" harnesses "$harness_id" mcp_config_template)"
  path="$("$py" "$INTEGRATIONS_ROOT/lib/registry.py" \
    "$INTEGRATIONS_ROOT/config/harnesses.yaml" harnesses "$harness_id" mcp_config_path)"
  transport="$("$py" "$INTEGRATIONS_ROOT/lib/registry.py" \
    "$INTEGRATIONS_ROOT/config/harnesses.yaml" harnesses "$harness_id" mcp_transport)"

  server_name="$(render_upstream_block "$target_id" | awk -F'"' '/^name =/{print $2; exit}')"

  if [[ "$transport" == "direct-jsonrpc" ]]; then
    export ATRYUM_MCP_SERVER="$server_name"
    return 0
  fi

  export CODEX_TEST_HOME="${CODEX_TEST_HOME:-$HARNESS_CONFIG_DIR/codex-home}"
  export GROK_TEST_HOME="${GROK_TEST_HOME:-$HARNESS_CONFIG_DIR/grok-home}"
  export AMP_SETTINGS_FILE="${AMP_SETTINGS_FILE:-$HARNESS_CONFIG_DIR/amp/settings.json}"

  mcp_remote_headers_env "$auth_id" || true
  mcp_url="$(build_mcp_url "$auth_id" "$harness_id" "$server_name")"

  case "$transport" in
    amp-settings-json)
      mkdir -p "$(dirname "$AMP_SETTINGS_FILE")"
      rm -f "$AMP_SETTINGS_FILE"
      local amp_env=()
      if [[ -n "${MCP_REMOTE_HEADERS:-}" ]]; then
        amp_env+=(--env "MCP_REMOTE_HEADERS=${MCP_REMOTE_HEADERS}")
      fi
      if ! AMP_SETTINGS_FILE="$AMP_SETTINGS_FILE" amp mcp add calculator_via_atryum \
        "${amp_env[@]}" \
        -- npx -y mcp-remote "$mcp_url"; then
        warn "amp mcp add failed for $AMP_SETTINGS_FILE"
        return 1
      fi
      return 0
      ;;
    codex-config-toml)
      dest="$CODEX_TEST_HOME/.codex/config.toml"
      mkdir -p "$(dirname "$dest")"
      for cred in auth.json .credentials.json; do
        if [[ -f "$HOME/.codex/$cred" ]]; then
          cp "$HOME/.codex/$cred" "$CODEX_TEST_HOME/.codex/"
        fi
      done
      if [[ -z "${OPENAI_API_KEY:-}" && -f "$HOME/.codex/auth.json" ]]; then
        : # use copied login credentials
      fi
      ;;
    grok-config-toml)
      mkdir -p "$GROK_TEST_HOME/.grok"
      if [[ -d "$HOME/.grok" ]]; then
        cp -a "$HOME/.grok/." "$GROK_TEST_HOME/.grok/" 2>/dev/null || true
      fi
      local grok_env=()
      if [[ -n "${MCP_REMOTE_HEADERS:-}" ]]; then
        grok_env+=(--env "MCP_REMOTE_HEADERS=${MCP_REMOTE_HEADERS}")
      fi
      HOME="$GROK_TEST_HOME" grok mcp add calculator_via_atryum \
        "${grok_env[@]}" \
        --command npx \
        --args -y mcp-remote "$mcp_url" >/dev/null 2>&1 || {
        warn "grok mcp add failed; writing config.toml fallback"
        dest="$GROK_TEST_HOME/.grok/config.toml"
        mkdir -p "$(dirname "$dest")"
        printf '%s\n' "$template" | sed "s|\$ATRYUM_MCP_URL|${mcp_url}|g" >"$dest"
      }
      return 0
      ;;
    *)
      if [[ -z "$template" || "$template" == "null" || -z "$path" || "$path" == "null" ]]; then
        return 0
      fi
      dest="$HARNESS_CONFIG_DIR/$(basename "$path")"
      mkdir -p "$(dirname "$dest")"
      ;;
  esac

  if [[ -n "${dest:-}" && -n "$template" && "$template" != "null" ]]; then
    printf '%s\n' "$template" | sed "s|\$ATRYUM_MCP_URL|${mcp_url}|g" >"$dest"
  fi

  case "$harness_id" in
    claude-code)
      export CLAUDE_MCP_CONFIG="$dest"
      ;;
    cursor)
      export CURSOR_MCP_CONFIG="$dest"
      ;;
    *)
      export HARNESS_MCP_CONFIG="$dest"
      ;;
  esac
}

run_harness_case() {
  local harness_id="$1" auth_id="$2" target_id="$3"
  local prompt expect py="${INTEGRATIONS_PYTHON:-python3}"
  prompt="$("$py" "$INTEGRATIONS_ROOT/lib/registry.py" \
    "$INTEGRATIONS_ROOT/config/mcp-targets.yaml" mcp_targets "$target_id" prompt)"
  local py="${INTEGRATIONS_PYTHON:-python3}"
  expect="$("$py" - "$INTEGRATIONS_ROOT" "$target_id" "$py" <<'PY'
import json, subprocess, sys
root, target_id, py = sys.argv[1], sys.argv[2], sys.argv[3]
item = json.loads(subprocess.check_output([
    py, f"{root}/lib/registry.py",
    f"{root}/config/mcp-targets.yaml", "mcp_targets", target_id,
], text=True))
print("|".join(item["verify"]["expect_substrings"]))
PY
)"

  export CODEX_TEST_HOME="${CODEX_TEST_HOME:-$HARNESS_CONFIG_DIR/codex-home}"
  export GROK_TEST_HOME="${GROK_TEST_HOME:-$HARNESS_CONFIG_DIR/grok-home}"
  export AMP_SETTINGS_FILE="${AMP_SETTINGS_FILE:-$HARNESS_CONFIG_DIR/amp/settings.json}"
  export CLAUDE_MCP_CONFIG="${CLAUDE_MCP_CONFIG:-}"

  install_hook "$harness_id"
  configure_harness_mcp "$harness_id" "$auth_id" "$target_id"

  local invoke
  log "Running harness=$harness_id auth=$auth_id target=$target_id"

  local output rc=0
  set +e
  if [[ "$harness_id" == "fake-agent" ]]; then
    local server_name tool_name args_json
    server_name="$(render_upstream_block "$target_id" | awk -F'"' '/^name =/{print $2; exit}')"
    tool_name="$("$py" - "$INTEGRATIONS_ROOT" "$target_id" "$py" <<'PY'
import json, subprocess, sys
root, target_id, py = sys.argv[1], sys.argv[2], sys.argv[3]
item = json.loads(subprocess.check_output([
    py, f"{root}/lib/registry.py",
    f"{root}/config/mcp-targets.yaml", "mcp_targets", target_id,
], text=True))
print(item["verify"]["tool"])
print(json.dumps(item["verify"]["arguments"]))
PY
)"
    args_json="$(echo "$tool_name" | sed -n '2p')"
    tool_name="$(echo "$tool_name" | sed -n '1p')"
    local bearer_args=()
    case "$auth_id" in
      oauth-client-credentials|oauth-dcr|static-bearer)
        local token
        token="$(fetch_bearer_token "$auth_id")" || return 1
        bearer_args=(--bearer "$token")
        ;;
    esac
    log "Command: python3 $REPO_ROOT/scripts/fake_agent.py mcp $server_name --tool $tool_name --arguments $args_json"
    output="$(
      ATRYUM_URL="$ATRYUM_URL" \
      python3 "$REPO_ROOT/scripts/fake_agent.py" mcp "$server_name" \
        --tool "$tool_name" \
        --arguments "$args_json" \
        "${bearer_args[@]}" 2>&1
    )"
    rc=$?
  else
    local invoke
    invoke="$(registry_py "$INTEGRATIONS_ROOT/config/harnesses.yaml" harnesses "$harness_id" invoke)"
    invoke="${invoke//\$PROMPT/$prompt}"
    invoke="${invoke//\$WORKDIR/$RUN_DIR}"
    invoke="${invoke//\$ATRYUM_URL/$ATRYUM_URL}"
    invoke="${invoke//\$CODEX_TEST_HOME/$CODEX_TEST_HOME}"
    invoke="${invoke//\$GROK_TEST_HOME/$GROK_TEST_HOME}"
    invoke="${invoke//\$CLAUDE_MCP_CONFIG/${CLAUDE_MCP_CONFIG:-}}"
    invoke="${invoke//\$AMP_SETTINGS_FILE/${AMP_SETTINGS_FILE:-}}"
    local harness_timeout="${HARNESS_TIMEOUT_SECONDS:-180}"
    log "Command: $invoke (timeout=${harness_timeout}s)"
    # shellcheck disable=SC2086
    output="$(timeout "$harness_timeout" bash -c "$invoke" 2>&1)"
    rc=$?
    if (( rc == 124 )); then
      warn "harness timed out after ${harness_timeout}s"
    fi
  fi
  set -e
  echo "$output" >"$RUN_DIR/${harness_id}-${auth_id}-${target_id}.log"

  if (( rc != 0 )); then
    warn "harness exited $rc"
    echo "$output" >&2
    return 1
  fi

  local part
  IFS='|' read -ra parts <<<"$expect"
  for part in "${parts[@]}"; do
    if [[ "$output" != *"$part"* ]]; then
      warn "expected '$part' in harness output"
      echo "$output" >&2
      return 1
    fi
  done
  log "Harness output contained expected value(s): ${expect//|/, }"
}