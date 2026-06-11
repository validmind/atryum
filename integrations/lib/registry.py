#!/usr/bin/env python3
"""Read integration registry YAML files with or without PyYAML installed."""

from __future__ import annotations

import json
import sys
from pathlib import Path
from typing import Any


def load_registry(path: Path, section: str) -> list[dict[str, Any]]:
    text = path.read_text(encoding="utf-8")
    try:
        import yaml  # type: ignore

        data = yaml.safe_load(text) or {}
        items = data.get(section, [])
        return items if isinstance(items, list) else []
    except ImportError:
        return _parse_flat_registry(text, section)


def _parse_flat_registry(text: str, section: str) -> list[dict[str, Any]]:
    items: list[dict[str, Any]] = []
    current: dict[str, Any] | None = None
    in_section = False
    multiline_key: str | None = None
    multiline_lines: list[str] = []

    def flush_multiline() -> None:
        nonlocal multiline_key, multiline_lines
        if current is not None and multiline_key is not None:
            current[multiline_key] = "\n".join(multiline_lines).rstrip("\n")
        multiline_key = None
        multiline_lines = []

    for line in text.splitlines():
        if line.strip() == f"{section}:":
            in_section = True
            continue
        if not in_section:
            continue
        if line.startswith("  - id:"):
            flush_multiline()
            if current is not None:
                items.append(current)
            current = {"id": line.split(":", 1)[1].strip()}
            continue
        if line.startswith("    ") and current is not None:
            if multiline_key is not None:
                if line.startswith("      "):
                    multiline_lines.append(line[6:])
                    continue
                flush_multiline()
            if ":" not in line.strip():
                continue
            key, val = line.strip().split(":", 1)
            val = val.strip()
            if val == "|":
                multiline_key = key
                multiline_lines = []
                continue
            if val in ("null", "~"):
                current[key] = None
            elif val.startswith("[") and val.endswith("]"):
                current[key] = [v.strip() for v in val[1:-1].split(",") if v.strip()]
            elif val.startswith('"') and val.endswith('"'):
                current[key] = val[1:-1]
            elif val.startswith("'") and val.endswith("'"):
                current[key] = val[1:-1]
            else:
                current[key] = val
            continue
        if line and not line.startswith(" "):
            break

    flush_multiline()
    if current is not None:
        items.append(current)
    return items


def get_item(path: Path, section: str, item_id: str) -> dict[str, Any]:
    for item in load_registry(path, section):
        if item.get("id") == item_id:
            return item
    raise KeyError(f"{item_id} not found in {section} of {path}")


def main() -> None:
    if len(sys.argv) < 3:
        print("usage: registry.py <file> <section> [id] [field]", file=sys.stderr)
        raise SystemExit(2)

    path = Path(sys.argv[1])
    section = sys.argv[2]
    if len(sys.argv) == 3:
        for item in load_registry(path, section):
            print(item["id"])
        return

    item_id = sys.argv[3]
    item = get_item(path, section, item_id)
    if len(sys.argv) == 4:
        print(json.dumps(item))
        return

    field = sys.argv[4]
    val = item.get(field)
    if val is None:
        print("", end="")
    elif isinstance(val, (dict, list)):
        print(json.dumps(val))
    else:
        print(val, end="")


if __name__ == "__main__":
    main()