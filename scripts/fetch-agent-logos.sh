#!/usr/bin/env bash
# Fetch brand logo SVGs for Atryum agent icons.
# Usage: bash scripts/fetch-agent-logos.sh   (FORCE=1 to overwrite existing files)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TARGET_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)/ui/public/atryum-agent-icons"
UA="Mozilla/5.0 (X11; Linux x86_64; rv:128.0) Gecko/20100101 Firefox/128.0"
ACCEPT="image/svg+xml,image/*,*/*;q=0.8"
ACCEPT_LANG="en-US,en;q=0.5"

mkdir -p "${TARGET_DIR}"

# Format: "<source-url>|<output-filename>"
#
# Sources used:
#   - simpleicons.org   CC0; per-product brand mark via /<slug>.
#   - upload.wikimedia.org   PD-textlogo / public-domain entries.
#   - ampcode.com/amp-mark-color.svg   Amp's own favicon, served directly.
#
# opencode has no static URL either but we extract it from their JS
# bundle below.
LOGOS=(
  "https://cdn.simpleicons.org/cursor|cursor.svg"
  "https://cdn.simpleicons.org/claude|claude.svg"
  "https://cdn.simpleicons.org/claude|claude-code.svg"
  "https://cdn.simpleicons.org/claude|claude-desktop.svg"
  "https://cdn.simpleicons.org/github|copilot.svg"
  "https://upload.wikimedia.org/wikipedia/commons/f/f9/Salesforce.com_logo.svg|agentforce.svg"
  "https://upload.wikimedia.org/wikipedia/commons/0/04/ChatGPT_logo.svg|codex.svg"
  "https://ampcode.com/amp-mark-color.svg|amp.svg"
  "https://cdn.simpleicons.org/zedindustries|zed.svg"
  "https://cdn.simpleicons.org/cline|cline.svg"
  "https://cdn.simpleicons.org/windsurf|windsurf.svg"
  "https://raw.githubusercontent.com/Aider-AI/aider/main/aider/website/assets/aider-square.jpg|aider.jpg"
)

downloaded=0
skipped=0
failed=0
total_bytes=0

for entry in "${LOGOS[@]}"; do
  url="${entry%%|*}"
  filename="${entry##*|}"
  dest="${TARGET_DIR}/${filename}"

  if [[ -f "${dest}" && "${FORCE:-0}" != "1" ]]; then
    echo "skip:     ${filename} (exists; set FORCE=1 to overwrite)"
    skipped=$((skipped + 1))
    continue
  fi

  echo "fetching: ${filename} <- ${url}"
  if curl -fsSL -A "${UA}" -H "Accept: ${ACCEPT}" -H "Accept-Language: ${ACCEPT_LANG}" "${url}" -o "${dest}.tmp"; then
    mv "${dest}.tmp" "${dest}"
    size=$(wc -c < "${dest}" | tr -d ' ')
    total_bytes=$((total_bytes + size))
    downloaded=$((downloaded + 1))
    echo "  ok:     ${filename} (${size} bytes)"
  else
    rm -f "${dest}.tmp"
    echo "  FAIL:   ${filename}" >&2
    failed=$((failed + 1))
  fi
done

# opencode special case: the SVG isn't served at a stable URL — it's
# embedded as a `data:image/svg+xml,...` URL-encoded string inside their
# JS bundle (the brand page's "Download SVG" button does the decode
# client-side). We do the same thing here. The bundle name is hashed and
# changes on each opencode deploy, so we discover it from /brand first.
opencode_dest="${TARGET_DIR}/opencode.svg"
if [[ -f "${opencode_dest}" && "${FORCE:-0}" != "1" ]]; then
  echo "skip:     opencode.svg (exists; set FORCE=1 to overwrite)"
  skipped=$((skipped + 1))
else
  echo "fetching: opencode.svg <- opencode.ai (extracted from JS bundle)"
  if UA="${UA}" python3 - "${opencode_dest}" <<'PY'
import os, re, sys, urllib.parse, urllib.request

def get(url: str) -> str:
    req = urllib.request.Request(url, headers={"User-Agent": os.environ["UA"]})
    with urllib.request.urlopen(req, timeout=20) as r:
        return r.read().decode()

# opencode.ai/brand modulepreloads many index-*.js bundles; only the brand
# page's bundle actually carries logoLightSquareSvg. Try them in order
# until one matches.
html = get("https://opencode.ai/brand")
bundles = re.findall(r"/_build/assets/index-[A-Za-z0-9_-]+\.js", html)
if not bundles:
    sys.exit("no index-*.js bundles found in opencode.ai/brand HTML")
for path in bundles:
    js = get("https://opencode.ai" + path)
    m = re.search(r'logoLightSquareSvg\s*=\s*"(data:image/svg\+xml,[^"]+)"', js)
    if m:
        prefix = "data:image/svg+xml,"
        svg = urllib.parse.unquote(m.group(1)[len(prefix):])
        with open(sys.argv[1], "w") as f:
            f.write(svg)
        print(f"  bundle:   {path}", file=sys.stderr)
        sys.exit(0)
sys.exit("logoLightSquareSvg not found in any of " + ", ".join(bundles))
PY
  then
    size=$(wc -c < "${opencode_dest}" | tr -d ' ')
    total_bytes=$((total_bytes + size))
    downloaded=$((downloaded + 1))
    echo "  ok:     opencode.svg (${size} bytes)"
  else
    echo "  FAIL:   opencode.svg" >&2
    failed=$((failed + 1))
  fi
fi

echo
echo "Summary: ${downloaded} downloaded, ${skipped} skipped, ${failed} failed, ${total_bytes} bytes total"
echo "Target:  ${TARGET_DIR}"

if [[ ${failed} -gt 0 ]]; then
  exit 1
fi
