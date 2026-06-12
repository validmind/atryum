#!/usr/bin/env bash
# Install the latest Atryum release binary for this machine.
#
# Usage:
#   curl -fsSL https://github.com/validmind/atryum/raw/main/install_atryum.sh | bash
#
# Optional environment variables:
#   ATRYUM_INSTALL_DIR  Destination directory (default: current directory)
#   ATRYUM_BINARY_NAME  Installed binary name (default: atryum)
#   ATRYUM_VERSION      Release tag to install (default: latest GitHub release)
#   GITHUB_TOKEN        GitHub token for private repositories or higher API limits

set -euo pipefail

REPO="validmind/atryum"
INSTALL_DIR="${ATRYUM_INSTALL_DIR:-.}"
BINARY_NAME="${ATRYUM_BINARY_NAME:-atryum}"

die() {
  echo "error: $*" >&2
  exit 1
}

need_cmd() {
  command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"
}

github_curl() {
  local args=(-fsSL)
  if [ -n "${GITHUB_TOKEN:-}" ]; then
    args+=(-H "Authorization: Bearer ${GITHUB_TOKEN}")
  fi
  curl "${args[@]}" "$@"
}

detect_platform() {
  local os arch uname_os uname_arch

  uname_os="$(uname -s)"
  uname_arch="$(uname -m)"

  case "$uname_os" in
    Darwin) os="darwin" ;;
    Linux) os="linux" ;;
    *) die "unsupported operating system: $uname_os (supported: Linux, macOS)" ;;
  esac

  case "$uname_arch" in
    x86_64 | amd64) arch="amd64" ;;
    arm64 | aarch64) arch="arm64" ;;
    *) die "unsupported CPU architecture: $uname_arch (supported: amd64, arm64)" ;;
  esac

  printf '%s-%s' "$os" "$arch"
}

release_json() {
  local tag="${ATRYUM_VERSION:-}"

  if [ -n "$tag" ]; then
    github_curl "https://api.github.com/repos/${REPO}/releases/tags/${tag}"
    return
  fi

  github_curl "https://api.github.com/repos/${REPO}/releases?per_page=1"
}

resolve_release_asset() {
  local artifact="$1"
  local payload="$2"

  python3 - "$artifact" "$payload" <<'PY'
import json
import sys

artifact = sys.argv[1]
payload = sys.argv[2]
release = json.loads(payload)
if isinstance(release, list):
    if not release:
        raise SystemExit(1)
    release = release[0]

tag = release["tag_name"]
for asset in release.get("assets", []):
    if asset.get("name") == artifact:
        print(tag)
        print(asset["browser_download_url"])
        print(asset["url"])
        raise SystemExit(0)

raise SystemExit(1)
PY
}

download_asset() {
  local browser_url="$1"
  local api_url="$2"
  local tmp="$3"

  if curl -fsSL "$browser_url" -o "$tmp" 2>/dev/null; then
    return 0
  fi

  if [ -n "${GITHUB_TOKEN:-}" ]; then
    curl -fsSL \
      -H "Authorization: Bearer ${GITHUB_TOKEN}" \
      -H "Accept: application/octet-stream" \
      "$api_url" -o "$tmp"
    return 0
  fi

  die "failed to download release asset; set GITHUB_TOKEN if this repository is private"
}

main() {
  local platform artifact release tag browser_url api_url tmp target resolved

  need_cmd curl
  need_cmd chmod
  need_cmd mv
  need_cmd mkdir
  need_cmd python3

  platform="$(detect_platform)"
  artifact="atryum-${platform}"

  release="$(release_json)" || die "could not fetch Atryum release metadata"
  resolved="$(resolve_release_asset "$artifact" "$release")" \
    || die "release metadata does not include ${artifact}"

  tag="$(printf '%s\n' "$resolved" | sed -n '1p')"
  browser_url="$(printf '%s\n' "$resolved" | sed -n '2p')"
  api_url="$(printf '%s\n' "$resolved" | sed -n '3p')"

  tmp="$(mktemp)"
  target="${INSTALL_DIR%/}/${BINARY_NAME}"

  mkdir -p "$INSTALL_DIR"

  echo "Installing Atryum ${tag} (${artifact}) to ${target}..."
  download_asset "$browser_url" "$api_url" "$tmp"
  chmod +x "$tmp"
  mv "$tmp" "$target"

  echo "Installed ${target}"
  echo "Run: ./${BINARY_NAME} setup demo"
}

main "$@"
