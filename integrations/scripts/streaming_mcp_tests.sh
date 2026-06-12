#!/usr/bin/env bash
# End-to-end tests for multi-frame SSE MCP responses through Atryum.
#
# Usage:
#   ./scripts/streaming_mcp_tests.sh
#
# Requires: python3, go, curl

set -euo pipefail

INTEGRATIONS_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
REPO_ROOT="$(cd "$INTEGRATIONS_ROOT/.." && pwd)"
export INTEGRATIONS_ROOT REPO_ROOT

# shellcheck source=../lib/common.sh
source "$INTEGRATIONS_ROOT/lib/common.sh"
# shellcheck source=../lib/atryum.sh
source "$INTEGRATIONS_ROOT/lib/atryum.sh"

MOCK_STREAMING_MCP_BASE=""
MOCK_STREAMING_MCP_URL=""
MOCK_STREAMING_MCP_PORT=""

start_mock_streaming_mcp() {
  pick_port MOCK_STREAMING_MCP_PORT
  MOCK_STREAMING_MCP_BASE="http://127.0.0.1:${MOCK_STREAMING_MCP_PORT}"
  MOCK_STREAMING_MCP_URL="${MOCK_STREAMING_MCP_BASE}/mcp"
  export MOCK_STREAMING_MCP_BASE MOCK_STREAMING_MCP_URL MOCK_STREAMING_MCP_PORT
  log "Starting mock streaming MCP on ${MOCK_STREAMING_MCP_URL}"
  python3 "$INTEGRATIONS_ROOT/fixtures/mock-streaming-mcp/streaming_mcp_server.py" \
    --port "$MOCK_STREAMING_MCP_PORT" \
    >"$RUN_DIR/mock-streaming-mcp.log" 2>&1 &
  echo $! >"$RUN_DIR/mock-streaming-mcp.pid"
  wait_for_http "$MOCK_STREAMING_MCP_URL" 40 0.25 || {
    tail -n 30 "$RUN_DIR/mock-streaming-mcp.log" >&2 || true
    die "mock streaming MCP failed to start"
  }
}

cleanup_streaming_mock() {
  cleanup_process "$RUN_DIR/mock-streaming-mcp.pid"
}

prepare_runtime() {
  pick_port ATRYUM_PORT
  pick_port MOCK_OIDC_PORT
  export ATRYUM_PORT MOCK_OIDC_PORT
  ATRYUM_URL="http://127.0.0.1:${ATRYUM_PORT}"
  export ATRYUM_URL
}

render_streaming_atryum_config() {
  local out_path="$1"
  render_atryum_config "no-auth" "streaming-mock" \
    "$INTEGRATIONS_ROOT/fixtures/atryum/base.toml" \
    "$out_path"
  sed -i "s|__MOCK_STREAMING_MCP_URL__|${MOCK_STREAMING_MCP_URL}|g" "$out_path"
}

run_fake_agent_mcp() {
  local server="streaming-mock"
  # First positional arg is the MCP server name; flags like --tool must not be consumed.
  if [[ $# -gt 0 && "$1" != --* ]]; then
    server="$1"
    shift
  fi
  local base="$ATRYUM_URL"
  local args=()
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --base)
        base="$2"
        shift 2
        ;;
      *)
        args+=("$1")
        shift
        ;;
    esac
  done
  # shellcheck disable=SC2068
  python3 "$REPO_ROOT/scripts/fake_agent.py" --base "$base" mcp "$server" "${args[@]}"
}

assert_contains() {
  local haystack="$1" needle="$2" label="$3"
  if [[ "$haystack" != *"$needle"* ]]; then
    warn "$label missing expected substring '$needle'"
    echo "$haystack" >&2
    return 1
  fi
}

main() {
  need_cmd python3
  need_cmd curl
  need_cmd go
  ensure_python_deps

  reset_case_runtime "streaming-mcp-tests"
  prepare_runtime
  trap 'cleanup_streaming_mock; cleanup_all' EXIT

  start_mock_streaming_mcp

  local config_path="$RUN_DIR/atryum.toml"
  render_streaming_atryum_config "$config_path"
  start_atryum "$config_path"
  seed_auto_approve_rules

  local output rc=0

  log "Case 1: direct mock upstream (streaming client, 3 SSE data frames)"
  set +e
  output="$(run_fake_agent_mcp '' \
    --base "$MOCK_STREAMING_MCP_BASE" \
    --tool stream_echo \
    --arguments '{"value": 7}' \
    --stream \
    --min-frames 3 \
    --expect-substring "STREAM_FINAL:7" 2>&1)"
  rc=$?
  set -e
  (( rc == 0 )) || die "direct mock streaming client failed"
  log "PASS: direct mock upstream"

  log "Case 2: Atryum tools/call (JSON client) extracts terminal SSE result"
  set +e
  output="$(run_fake_agent_mcp \
    --tool stream_echo \
    --arguments '{"value": 42}' \
    --expect-substring "STREAM_FINAL:42" 2>&1)"
  rc=$?
  set -e
  (( rc == 0 )) || die "atryum JSON client tools/call failed"
  assert_contains "$output" "STREAM_FINAL:42" "tools/call JSON" || die "Case 2 assertion failed"
  log "PASS: atryum tools/call JSON collapse"

  log "Case 3: Atryum tools/call (streaming client) still returns correct terminal result"
  set +e
  output="$(run_fake_agent_mcp \
    --tool stream_echo \
    --arguments '{"value": 99}' \
    --stream \
    --expect-substring "STREAM_FINAL:99" 2>&1)"
  rc=$?
  set -e
  (( rc == 0 )) || die "atryum streaming client tools/call failed"
  assert_contains "$output" "STREAM_FINAL:99" "tools/call stream" || die "Case 3 assertion failed"
  log "PASS: atryum tools/call streaming client terminal result"

  log "Case 4: Atryum forward path preserves buffered SSE body for stream clients"
  set +e
  output="$(run_fake_agent_mcp \
    --method ping \
    --params '{}' \
    --stream \
    --min-frames 3 \
    --expect-substring "STREAM_FINAL:ping" 2>&1)"
  rc=$?
  set -e
  (( rc == 0 )) || die "atryum forward streaming client failed"
  assert_contains "$output" '"message": "frame-1"' "forward stream" || die "Case 4 missing progress frame"
  assert_contains "$output" "STREAM_FINAL:ping" "forward stream" || die "Case 4 missing terminal marker"
  log "PASS: atryum forward SSE relay to streaming client"

  log "All streaming MCP tests passed"
}

main "$@"