#!/usr/bin/env bash
# Shared helpers for the integration test suite.

set -euo pipefail

INTEGRATIONS_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
REPO_ROOT="$(cd "$INTEGRATIONS_ROOT/.." && pwd)"

export INTEGRATIONS_ROOT REPO_ROOT

RUN_ID="${RUN_ID:-$(date +%Y%m%d-%H%M%S)-$$}"
RUN_DIR="${RUN_DIR:-$INTEGRATIONS_ROOT/.run/$RUN_ID}"
RESULTS_DIR="${RESULTS_DIR:-$INTEGRATIONS_ROOT/results}"
HARNESS_CONFIG_DIR="${HARNESS_CONFIG_DIR:-$RUN_DIR/harness-config}"

ATRYUM_PORT="${ATRYUM_PORT:-}"
MOCK_OIDC_PORT="${MOCK_OIDC_PORT:-}"

log() { printf '\033[1;36m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m!!\033[0m %s\n' "$*" >&2; }
die() { printf '\033[1;31merror:\033[0m %s\n' "$*" >&2; exit 1; }

need_cmd() {
  command -v "$1" >/dev/null 2>&1 || die "missing required command: $1"
}

mkdir -p "$RUN_DIR" "$RESULTS_DIR" "$HARNESS_CONFIG_DIR"

ensure_python_deps() {
  local venv="$INTEGRATIONS_ROOT/.venv"
  if [[ ! -x "$venv/bin/python" ]]; then
    log "Creating Python venv for integration helpers..."
    python3 -m venv "$venv"
    "$venv/bin/pip" install -q -r "$INTEGRATIONS_ROOT/requirements.txt"
  fi
  export INTEGRATIONS_PYTHON="$venv/bin/python"
}

registry_py() {
  local py="${INTEGRATIONS_PYTHON:-$INTEGRATIONS_ROOT/.venv/bin/python}"
  if [[ ! -x "$py" ]]; then
    py=python3
  fi
  "$py" "$INTEGRATIONS_ROOT/lib/registry.py" "$@"
}

write_result() {
  local name="$1" status="$2" detail="${3:-}"
  local file="$RESULTS_DIR/${RUN_ID}-${name}.json"
  printf '{"run_id":"%s","case":"%s","status":"%s","detail":%s,"ts":"%s"}\n' \
    "$RUN_ID" "$name" "$status" "$(json_quote "$detail")" "$(date -Iseconds)" >"$file"
}

json_quote() {
  python3 -c 'import json,sys; print(json.dumps(sys.argv[1]))' "$1"
}

wait_for_http() {
  local url="$1" attempts="${2:-60}" delay="${3:-0.5}"
  local i=0
  while (( i < attempts )); do
    if curl -fsS "$url" >/dev/null 2>&1; then
      return 0
    fi
    sleep "$delay"
    i=$((i + 1))
  done
  return 1
}

free_port() {
  python3 - <<'PY'
import socket
s = socket.socket()
s.bind(("", 0))
print(s.getsockname()[1])
s.close()
PY
}

pick_port() {
  local var_name="$1"
  local current="${!var_name:-}"
  if [[ -n "$current" && "$current" != "0" ]]; then
    return 0
  fi
  # shellcheck disable=SC2154
  printf -v "$var_name" '%s' "$(free_port)"
}

cleanup_process() {
  local pidfile="$1"
  [[ -f "$pidfile" ]] || return 0
  local pid
  pid="$(cat "$pidfile")"
  if kill -0 "$pid" 2>/dev/null; then
    kill "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
  fi
  rm -f "$pidfile"
}

cleanup_all() {
  cleanup_process "$RUN_DIR/mock-oidc.pid"
  cleanup_process "$RUN_DIR/atryum.pid"
}

# Tear down the previous case and allocate a fresh run directory + ports.
# Matrix cells must not share atryum.db — bootstrap only runs on an empty DB.
reset_case_runtime() {
  local case_suffix="${1:-}"
  cleanup_all

  CASE_SEQ="${CASE_SEQ:-0}"
  CASE_SEQ=$((CASE_SEQ + 1))
  RUN_ID="$(date +%Y%m%d-%H%M%S)-$$-${CASE_SEQ}"
  if [[ -n "$case_suffix" ]]; then
    RUN_ID="${RUN_ID}-${case_suffix}"
  fi
  export RUN_ID

  RUN_DIR="$INTEGRATIONS_ROOT/.run/$RUN_ID"
  HARNESS_CONFIG_DIR="$RUN_DIR/harness-config"
  export RUN_DIR HARNESS_CONFIG_DIR

  ATRYUM_PORT=""
  MOCK_OIDC_PORT=""
  export ATRYUM_PORT MOCK_OIDC_PORT
  unset ATRYUM_URL MOCK_OIDC_ISSUER ATRYUM_BEARER_TOKEN MCP_REMOTE_HEADERS || true

  mkdir -p "$RUN_DIR" "$RESULTS_DIR" "$HARNESS_CONFIG_DIR"
}

trap cleanup_all EXIT