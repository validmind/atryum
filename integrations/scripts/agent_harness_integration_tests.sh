#!/usr/bin/env bash
# Agent harness integration tests — exercise harnesses through Atryum to MCP upstreams.
#
# Usage:
#   ./scripts/agent_harness_integration_tests.sh list
#   ./scripts/agent_harness_integration_tests.sh run --harness <id> --auth <id> [--target <id>]
#   ./scripts/agent_harness_integration_tests.sh matrix [--only-passing] [--harnesses a,b] [--auth x,y] [--targets t]
#
# Environment (harness LLM credentials — never committed):
#   OPENAI_API_KEY, ANTHROPIC_API_KEY, AMP_API_KEY, CURSOR_API_KEY, XAI_API_KEY

set -euo pipefail

INTEGRATIONS_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
export INTEGRATIONS_ROOT

# shellcheck source=../lib/common.sh
source "$INTEGRATIONS_ROOT/lib/common.sh"
# shellcheck source=../lib/yaml.sh
source "$INTEGRATIONS_ROOT/lib/yaml.sh"
# shellcheck source=../lib/auth.sh
source "$INTEGRATIONS_ROOT/lib/auth.sh"
# shellcheck source=../lib/atryum.sh
source "$INTEGRATIONS_ROOT/lib/atryum.sh"
# shellcheck source=../lib/harness.sh
source "$INTEGRATIONS_ROOT/lib/harness.sh"

CMD="${1:-help}"
shift || true

usage() {
  cat <<'EOF'
Agent harness integration tests

Commands:
  list                              List harnesses, auth protocols, and MCP targets
  run                               Run a single harness × auth × target case
  matrix                            Run the full matrix (skips unavailable cases)
  help                              Show this help

Run options:
  --harness ID                      Harness id (required)
  --auth ID                         Auth protocol id (required)
  --target ID                       MCP target id (default: calculator)
  --skip-direct                     Skip fake_agent.py MCP verification

Matrix options:
  --harnesses a,b                   Filter harness ids
  --auth a,b                        Filter auth protocol ids
  --targets a,b                     Filter MCP target ids
  --only-passing                    Skip placeholder auth modes

Examples:
  ./scripts/agent_harness_integration_tests.sh run --harness fake-agent --auth no-auth
  ./scripts/agent_harness_integration_tests.sh run --harness codex --auth no-auth
  ./scripts/agent_harness_integration_tests.sh matrix --only-passing
  ./scripts/agent_harness_integration_tests.sh matrix --harnesses fake-agent --auth no-auth,oauth-client-credentials
EOF
}

cmd_list() {
  echo "Harnesses:"
  yaml_list_ids "$INTEGRATIONS_ROOT/config/harnesses.yaml" harnesses | sed 's/^/  /'
  echo
  echo "Auth protocols:"
  yaml_list_ids "$INTEGRATIONS_ROOT/config/auth-protocols.yaml" auth_protocols | sed 's/^/  /'
  echo
  echo "MCP targets:"
  yaml_list_ids "$INTEGRATIONS_ROOT/config/mcp-targets.yaml" mcp_targets | sed 's/^/  /'
}

prepare_runtime() {
  need_cmd python3
  need_cmd curl
  need_cmd go
  ensure_python_deps

  pick_port ATRYUM_PORT
  pick_port MOCK_OIDC_PORT
  export ATRYUM_PORT MOCK_OIDC_PORT
  ATRYUM_URL="http://127.0.0.1:${ATRYUM_PORT}"
  export ATRYUM_URL
}

run_single_case() {
  local harness_id="$1" auth_id="$2" target_id="$3" skip_direct="${4:-0}"
  local case_name="${harness_id}__${auth_id}__${target_id}"

  reset_case_runtime "$case_name"
  prepare_runtime
  log "Ports: atryum=$ATRYUM_PORT mock_oidc=$MOCK_OIDC_PORT"

  log "Case: $case_name (run_dir=$RUN_DIR)"

  local auth_status
  auth_status="$(auth_protocol_status "$auth_id")"
  if [[ "$auth_status" == "placeholder" ]]; then
    warn "auth protocol '$auth_id' is a placeholder — skipping"
    write_result "$case_name" "skipped" "auth protocol placeholder"
    return 0
  fi

  harness_available "$harness_id" || {
    write_result "$case_name" "skipped" "harness unavailable"
    return 0
  }

  if [[ "$(auth_requires_mock_oidc "$auth_id")" == "yes" ]]; then
    start_mock_oidc
  fi

  local template config_path
  template="$(auth_protocol_template "$auth_id")"
  config_path="$RUN_DIR/atryum.toml"
  render_atryum_config "$auth_id" "$target_id" "$template" "$config_path"
  start_atryum "$config_path" || {
    write_result "$case_name" "failed" "atryum startup failed"
    return 1
  }
  seed_auto_approve_rules || {
    write_result "$case_name" "failed" "failed to seed auto-approve rule"
    return 1
  }

  if (( skip_direct == 0 )); then
    verify_upstream_direct "$target_id" "$auth_id" || {
      write_result "$case_name" "failed" "direct MCP verification failed"
      return 1
    }
  fi

  if run_harness_case "$harness_id" "$auth_id" "$target_id"; then
    write_result "$case_name" "passed" ""
    log "PASS: $case_name"
    return 0
  fi

  write_result "$case_name" "failed" "harness run failed — see $RUN_DIR"
  die "FAIL: $case_name"
}

cmd_run() {
  local harness_id="" auth_id="" target_id="calculator" skip_direct=0

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --harness) harness_id="$2"; shift 2 ;;
      --auth) auth_id="$2"; shift 2 ;;
      --target) target_id="$2"; shift 2 ;;
      --skip-direct) skip_direct=1; shift ;;
      -h|--help) usage; exit 0 ;;
      *) die "unknown run argument: $1" ;;
    esac
  done

  [[ -n "$harness_id" ]] || die "--harness is required"
  [[ -n "$auth_id" ]] || die "--auth is required"
  run_single_case "$harness_id" "$auth_id" "$target_id" "$skip_direct"
}

in_csv_list() {
  local needle="$1" csv="$2"
  [[ -z "$csv" ]] && return 0
  [[ ",${csv}," == *",${needle},"* ]]
}

cmd_matrix() {
  local harness_filter="" auth_filter="" target_filter="" only_passing=0

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --harnesses) harness_filter="$2"; shift 2 ;;
      --auth) auth_filter="$2"; shift 2 ;;
      --targets) target_filter="$2"; shift 2 ;;
      --only-passing) only_passing=1; shift ;;
      -h|--help) usage; exit 0 ;;
      *) die "unknown matrix argument: $1" ;;
    esac
  done

  local passed=0 failed=0 skipped=0 harness auth target

  for harness in $(yaml_list_ids "$INTEGRATIONS_ROOT/config/harnesses.yaml" harnesses); do
    in_csv_list "$harness" "$harness_filter" || continue
    for auth in $(yaml_list_ids "$INTEGRATIONS_ROOT/config/auth-protocols.yaml" auth_protocols); do
      in_csv_list "$auth" "$auth_filter" || continue
      if (( only_passing )); then
        local status
        status="$(auth_protocol_status "$auth")"
        [[ "$status" == "implemented" ]] || { skipped=$((skipped + 1)); continue; }
      fi
      for target in $(yaml_list_ids "$INTEGRATIONS_ROOT/config/mcp-targets.yaml" mcp_targets); do
        in_csv_list "$target" "$target_filter" || continue
        log "Matrix cell: harness=$harness auth=$auth target=$target"
        set +e
        run_single_case "$harness" "$auth" "$target"
        local rc=$?
        set -e
        case "$rc" in
          0) passed=$((passed + 1)) ;;
          *) failed=$((failed + 1)) ;;
        esac
      done
    done
  done

  log "Matrix complete: passed=$passed failed=$failed skipped=$skipped"
  (( failed == 0 ))
}

case "$CMD" in
  list) cmd_list "$@" ;;
  run) cmd_run "$@" ;;
  matrix) cmd_matrix "$@" ;;
  help|-h|--help) usage ;;
  *)
    die "unknown command: $CMD (try: help)"
    ;;
esac
