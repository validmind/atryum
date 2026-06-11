#!/usr/bin/env bash
# Minimal YAML readers — avoids extra dependencies beyond python3.

set -euo pipefail

yaml_list_ids() {
  local file="$1" section="$2"
  python3 - "$file" "$section" <<'PY'
import sys
from pathlib import Path

try:
    import yaml  # type: ignore
except ImportError:
    yaml = None

path, section = sys.argv[1], sys.argv[2]
text = Path(path).read_text(encoding="utf-8")

if yaml is not None:
    data = yaml.safe_load(text) or {}
    for item in data.get(section, []):
        print(item["id"])
    raise SystemExit(0)

# Fallback: line-oriented parser for our flat registry files.
items = []
current = None
in_section = False
for line in text.splitlines():
    if line.strip() == f"{section}:":
        in_section = True
        continue
    if in_section and line.startswith("  - id:"):
        if current:
            items.append(current)
        current = line.split(":", 1)[1].strip()
    elif in_section and line.startswith("  - ") and not line.startswith("  - id:"):
        if current:
            items.append(current)
            current = None
    elif in_section and line and not line.startswith(" "):
        break
if current:
    items.append(current)
for item in items:
    print(item)
PY
}

yaml_get_field() {
  local file="$1" section="$2" id="$3" field="$4"
  python3 - "$file" "$section" "$id" "$field" <<'PY'
import sys
from pathlib import Path

try:
    import yaml  # type: ignore
except ImportError:
    yaml = None

path, section, wanted_id, field = sys.argv[1:5]
text = Path(path).read_text(encoding="utf-8")

if yaml is not None:
    data = yaml.safe_load(text) or {}
    for item in data.get(section, []):
        if item.get("id") == wanted_id:
            val = item.get(field)
            if val is None:
                print("", end="")
            elif isinstance(val, (dict, list)):
                import json
                print(json.dumps(val))
            else:
                print(val)
            raise SystemExit(0)
    raise SystemExit(1)

# Fallback parser
items = {}
current_id = None
current: dict = {}
in_section = False
for line in text.splitlines():
    if line.strip() == f"{section}:":
        in_section = True
        continue
    if not in_section:
        continue
    if line.startswith("  - id:"):
        if current_id:
            items[current_id] = current
        current_id = line.split(":", 1)[1].strip()
        current = {"id": current_id}
        continue
    if line.startswith("    ") and current_id and ":" in line:
        key, val = line.strip().split(":", 1)
        val = val.strip()
        if val in ('""', "''"):
            val = ""
        elif val.startswith('"') and val.endswith('"'):
            val = val[1:-1]
        elif val == "null":
            val = ""
        current[key] = val
    elif line.startswith("  - ") and not line.startswith("  - id:"):
        pass
    elif line and not line.startswith(" "):
        break
if current_id:
    items[current_id] = current

item = items.get(wanted_id)
if not item:
    raise SystemExit(1)
val = item.get(field, "")
print(val if val is not None else "", end="")
PY
}