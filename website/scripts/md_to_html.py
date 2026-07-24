#!/usr/bin/env python3
"""Convert website/md-drafts/**/*.md to website/documentation/**/*.html.

Generated on each run:
  - website/documentation/**/*.html (mirrors numbered md-drafts layout)
  - website/partials/docs-nav.html (header doc links and section dropdowns)

Also maintained automatically when you run the script:
  - Canonical relative links in md-drafts/**/*.md (rewritten when paths drift)
  - NAV_LABEL_OVERRIDES keys (migrated when git detects draft renames)
  - Stale documentation/**/*.html removed; empty output directories removed

Maintained manually (not generated):
  - website/index.html
  - website/partials/header.html, footer.html
  - website/assets/*
"""

from __future__ import annotations

import html
import os
import re
import subprocess
import sys
import unicodedata
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
DRAFTS = ROOT / "md-drafts"
OUTPUT = ROOT / "documentation"
PARTIALS = ROOT / "partials"

# Optional nav label overrides when the menu label should differ from the page H1.
NAV_LABEL_OVERRIDES: dict[str, str] = {}

CALLOUT_PLACEHOLDER = "__CALLOUT_{index}__"

COPY_ICON_SVG = (
    '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" '
    'stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">'
    '<rect x="9" y="9" width="13" height="13" rx="2"/>'
    '<path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/>'
    "</svg>"
)

# Optional numeric prefix for source ordering, e.g. 1_quickstart.md or 1_integrations/.
NUMERIC_PREFIX = re.compile(r"^(\d+)_")
MD_LINK_RE = re.compile(r"\[([^\]]+)\]\(([^)]+)\)")

_partials_prefix = "../partials"
_rename_link_lookup: dict[str, Path] = {}


def slugify(text: str) -> str:
    text = re.sub(r"<[^>]+>", "", text)
    text = unicodedata.normalize("NFKD", text)
    text = text.encode("ascii", "ignore").decode("ascii")
    text = text.lower()
    text = re.sub(r"[^a-z0-9\s-]", "", text)
    text = re.sub(r"[\s_]+", "-", text.strip())
    return re.sub(r"-+", "-", text).strip("-")


def path_depth(path: Path) -> int:
    return len(path.relative_to(DRAFTS).parts) - 1


def path_prefixes(depth: int) -> tuple[str, str]:
    upward = "../" * (depth + 1)
    return f"{upward}assets", f"{upward}partials"


def directory_label(name: str) -> str:
    return output_dir_name(name).replace("-", " ")


def strip_numeric_prefix(name: str) -> str:
    return NUMERIC_PREFIX.sub("", name)


def output_stem(path: Path) -> str:
    return strip_numeric_prefix(path.stem)


def output_dir_name(name: str) -> str:
    return strip_numeric_prefix(name)


def numeric_sort_key(name: str) -> tuple[int, int, str]:
    match = NUMERIC_PREFIX.match(name)
    if match:
        return (0, int(match.group(1)), strip_numeric_prefix(name))
    return (1, 9999, name)


def page_slug(path: Path) -> str:
    return path.relative_to(DRAFTS).with_suffix("").as_posix()


def nav_label(path: Path) -> str:
    clean_stem = output_stem(path)
    if clean_stem in NAV_LABEL_OVERRIDES:
        return NAV_LABEL_OVERRIDES[clean_stem]

    for line in path.read_text(encoding="utf-8").splitlines():
        if line.startswith("# "):
            return line.lstrip("# ").strip()

    return output_stem(path).replace("-", " ").title()


def html_output_path(path: Path) -> Path:
    return OUTPUT / path.relative_to(DRAFTS).with_suffix(".html")


def find_md_by_output_stem(stem: str) -> list[Path]:
    return sorted(
        path
        for path in DRAFTS.rglob("*.md")
        if output_stem(path) == stem or path.stem == stem
    )


def find_md_in_output_dir(output_dir: str, stem: str) -> list[Path]:
    matches: list[Path] = []
    for path in DRAFTS.rglob("*.md"):
        rel = path.relative_to(DRAFTS)
        if len(rel.parts) < 2:
            continue
        if output_dir_name(rel.parts[0]) != output_dir_name(output_dir):
            continue
        if (
            path.stem == stem
            or output_stem(path) == strip_numeric_prefix(stem)
            or path.stem.endswith(f"_{strip_numeric_prefix(stem)}")
        ):
            matches.append(path)
    return sorted(matches)


def resolve_md_target(source_md: Path, link: str) -> Path:
    direct = (source_md.parent / link).resolve()
    if direct.exists():
        return direct

    link_path = Path(link)
    link_name = link_path.name
    link_stem = Path(link_name).stem

    if link_name in _rename_link_lookup:
        return _rename_link_lookup[link_name]

    clean_stem = strip_numeric_prefix(link_stem)
    if clean_stem in _rename_link_lookup:
        return _rename_link_lookup[clean_stem]

    matches = sorted(DRAFTS.rglob(link_name))
    if matches:
        return matches[0]

    matches = sorted(DRAFTS.rglob(f"*_{link_stem}.md"))
    if matches:
        return matches[0]

    matches = sorted(DRAFTS.rglob(f"*_{clean_stem}.md"))
    if matches:
        return matches[0]

    matches = find_md_by_output_stem(link_stem)
    if matches:
        return matches[0]

    matches = find_md_by_output_stem(clean_stem)
    if matches:
        return matches[0]

    if len(link_path.parts) > 1:
        output_dir = output_dir_name(link_path.parts[0])
        file_stem = strip_numeric_prefix(link_stem)
        matches = find_md_in_output_dir(output_dir, file_stem)
        if matches:
            return matches[0]

    return direct


def md_relative_link(source_md: Path, target_md: Path) -> str:
    return Path(os.path.relpath(target_md, source_md.parent)).as_posix()


def parse_git_md_renames(output: str) -> tuple[dict[str, str], dict[str, Path]]:
    """Return stem renames and link lookup entries from git name-status output."""
    stem_renames: dict[str, str] = {}
    link_lookup: dict[str, Path] = {}

    for line in output.splitlines():
        if not line.startswith("R"):
            continue
        parts = line.split("\t")
        if len(parts) != 3:
            continue

        old_path = Path(parts[1])
        new_path = Path(parts[2])
        if old_path.suffix != ".md" or new_path.suffix != ".md":
            continue
        if old_path.parts[:2] != ("website", "md-drafts"):
            continue

        new_md = ROOT.parent / new_path
        if not new_md.exists():
            continue

        old_stem = output_stem(old_path)
        new_stem = output_stem(new_path)
        if old_stem != new_stem:
            stem_renames[old_stem] = new_stem

        link_lookup[old_path.name] = new_md
        link_lookup[old_stem] = new_md

    return stem_renames, link_lookup


def detect_md_renames() -> tuple[dict[str, str], dict[str, Path]]:
    """Detect draft renames from git (working tree and index)."""
    stem_renames: dict[str, str] = {}
    link_lookup: dict[str, Path] = {}
    commands = (
        ["git", "diff", "--name-status", "--find-renames=50%", "--", "website/md-drafts"],
        [
            "git",
            "diff",
            "--cached",
            "--name-status",
            "--find-renames=50%",
            "--",
            "website/md-drafts",
        ],
    )

    try:
        for command in commands:
            result = subprocess.run(
                command,
                cwd=ROOT.parent,
                capture_output=True,
                text=True,
                check=False,
            )
            if result.returncode != 0:
                continue
            renames, lookup = parse_git_md_renames(result.stdout)
            stem_renames.update(renames)
            link_lookup.update(lookup)
    except OSError:
        pass

    return stem_renames, link_lookup


def normalize_md_links(pages: list[Path]) -> None:
    """Rewrite .md links to canonical relative paths when resolution finds a target."""
    for path in pages:
        source = path.read_text(encoding="utf-8")
        changed = False

        def repl(match: re.Match[str]) -> str:
            nonlocal changed
            label, url = match.group(1), match.group(2)
            if url.startswith(("#", "http://", "https://", "mailto:")):
                return match.group(0)

            url_path, _, anchor = url.partition("#")
            anchor_suffix = f"#{anchor}" if anchor else ""
            if not url_path.endswith(".md"):
                return match.group(0)

            target = resolve_md_target(path, url_path)
            if not target.exists():
                rel = path.relative_to(DRAFTS)
                print(
                    f"Warning: {rel}: broken link [{label}]({url}) — no matching draft",
                    file=sys.stderr,
                )
                return match.group(0)

            canonical = md_relative_link(path, target)
            if canonical != url_path:
                changed = True
                return f"[{label}]({canonical}{anchor_suffix})"
            return match.group(0)

        updated = MD_LINK_RE.sub(repl, source)
        if changed:
            path.write_text(updated, encoding="utf-8")
            print(f"Updated links in {path.relative_to(ROOT.parent)}")


def migrate_nav_label_overrides(stem_renames: dict[str, str]) -> bool:
    changed = False
    for old_stem, new_stem in stem_renames.items():
        if old_stem in NAV_LABEL_OVERRIDES and new_stem not in NAV_LABEL_OVERRIDES:
            NAV_LABEL_OVERRIDES[new_stem] = NAV_LABEL_OVERRIDES.pop(old_stem)
            changed = True
    return changed


def warn_orphaned_nav_label_overrides(pages: list[Path]) -> None:
    stems = {output_stem(path) for path in pages}
    for key in NAV_LABEL_OVERRIDES:
        if key not in stems:
            print(
                f"Warning: NAV_LABEL_OVERRIDES key {key!r} has no matching draft",
                file=sys.stderr,
            )


def persist_nav_label_overrides() -> None:
    script_path = Path(__file__)
    content = script_path.read_text(encoding="utf-8")
    if NAV_LABEL_OVERRIDES:
        entries = ", ".join(f"{key!r}: {value!r}" for key, value in sorted(NAV_LABEL_OVERRIDES.items()))
        replacement = f"NAV_LABEL_OVERRIDES: dict[str, str] = {{{entries}}}"
    else:
        replacement = "NAV_LABEL_OVERRIDES: dict[str, str] = {}"

    updated, count = re.subn(
        r"^NAV_LABEL_OVERRIDES: dict\[str, str\] = .*",
        replacement,
        content,
        count=1,
        flags=re.MULTILINE,
    )
    if count:
        script_path.write_text(updated, encoding="utf-8")
        print(f"Migrated NAV_LABEL_OVERRIDES in {script_path.relative_to(ROOT.parent)}")


def md_href(source_md: Path, url: str) -> str:
    if url.startswith(("#", "http://", "https://", "mailto:")):
        return url

    anchor = ""
    if "#" in url:
        url, anchor_part = url.split("#", 1)
        anchor = f"#{anchor_part}"

    if not url.endswith(".md"):
        return url + anchor

    target_md = resolve_md_target(source_md, url)
    target_html = html_output_path(target_md)
    source_html_parent = html_output_path(source_md).parent
    href = Path(os.path.relpath(target_html, source_html_parent)).as_posix()
    return href + anchor


def replace_md_links(text: str, source_md: Path) -> str:
    def repl(match: re.Match[str]) -> str:
        label, url = match.group(1), match.group(2)
        return f"[{label}]({md_href(source_md, url)})"

    return MD_LINK_RE.sub(repl, text)


def restore_html_spans(text: str) -> str:
    text = re.sub(
        r'&lt;span style="font-variant: small-caps;"&gt;',
        '<span style="font-variant: small-caps;">',
        text,
    )
    return text.replace("&lt;/span&gt;", "</span>")


def restore_html_entities(text: str) -> str:
    return re.sub(r"&amp;(#(?:x[0-9a-fA-F]+|\d+);)", r"&\1", text)


def inline_format(text: str) -> str:
    text = html.escape(text, quote=False)
    text = re.sub(r"\*\*([^*]+)\*\*", r"<strong>\1</strong>", text)
    text = re.sub(r"(?<!\*)\*([^*]+)\*(?!\*)", r"<em>\1</em>", text)
    text = re.sub(r"(?<!\w)_([^_]+)_(?!\w)", r"<em>\1</em>", text)
    text = re.sub(r"`([^`]+)`", r"<code>\1</code>", text)
    text = re.sub(
        r"\[([^\]]+)\]\(([^)]+)\)",
        lambda m: f'<a href="{html.escape(m.group(2), quote=True)}">{m.group(1)}</a>',
        text,
    )
    return restore_html_entities(restore_html_spans(text))


def dedent_callout_body(lines: list[str]) -> str:
    """Remove shared leading indentation from callout body lines."""
    non_empty = [line for line in lines if line.strip()]
    if not non_empty:
        return ""
    min_indent = min(len(line) - len(line.lstrip()) for line in non_empty)
    dedented: list[str] = []
    for line in lines:
        if not line.strip():
            dedented.append("")
            continue
        dedented.append(line[min_indent:] if len(line) >= min_indent else line.lstrip())
    return "\n".join(dedented).strip("\n")


def extract_callouts(source: str) -> tuple[str, list[str]]:
    lines = source.splitlines()
    callouts: list[str] = []
    output_lines: list[str] = []

    index = 0
    while index < len(lines):
        line = lines[index]
        if line.strip() == ":::":
            fence_indent = line[: len(line) - len(line.lstrip())]
            index += 1
            body_lines: list[str] = []
            while index < len(lines) and lines[index].strip() != ":::":
                body_lines.append(lines[index])
                index += 1
            if index < len(lines):
                index += 1
            callout_index = len(callouts)
            callouts.append(dedent_callout_body(body_lines))
            output_lines.append(
                f"{fence_indent}{CALLOUT_PLACEHOLDER.format(index=callout_index)}"
            )
            continue
        output_lines.append(line)
        index += 1

    return "\n".join(output_lines), callouts


def inject_callouts(content_html: str, callouts: list[str]) -> str:
    result = content_html
    for index, body in enumerate(callouts):
        converted = convert_blocks(body.splitlines())
        placeholder_html = f'<aside class="docs-callout">{converted}</aside>'
        token = CALLOUT_PLACEHOLDER.format(index=index)
        if token in result:
            result = result.replace(token, placeholder_html)
        else:
            escaped_token = html.escape(token)
            result = result.replace(
                f"<p>{escaped_token}</p>",
                placeholder_html,
            )
            result = result.replace(escaped_token, placeholder_html)
    return result


def is_table_row(line: str) -> bool:
    stripped = line.strip()
    return stripped.startswith("|") and stripped.endswith("|")


def is_table_separator(line: str) -> bool:
    stripped = line.strip().strip("|")
    if not stripped:
        return False
    cells = [cell.strip() for cell in stripped.split("|")]
    return all(re.fullmatch(r":?-{3,}:?", cell or "-") for cell in cells)


def convert_table(lines: list[str], index: int) -> tuple[str, int]:
    table_lines = []
    while index < len(lines) and is_table_row(lines[index]):
        table_lines.append(lines[index])
        index += 1

    if len(table_lines) < 2 or not is_table_separator(table_lines[1]):
        return "", index

    def cells(row: str) -> list[str]:
        return [cell.strip() for cell in row.strip().strip("|").split("|")]

    header = cells(table_lines[0])
    body_rows = [cells(row) for row in table_lines[2:]]

    parts = ['<table class="docs-table">', "<thead><tr>"]
    for cell in header:
        parts.append(f"<th>{inline_format(cell)}</th>")
    parts.append("</tr></thead><tbody>")
    for row in body_rows:
        parts.append("<tr>")
        for cell in row:
            parts.append(f"<td>{inline_format(cell)}</td>")
        parts.append("</tr>")
    parts.append("</tbody></table>")
    return "".join(parts), index


def list_item_match(line: str) -> re.Match[str] | None:
    return re.match(r"^(\s*)([-*]|\d+\.)\s+(.*)$", line)


def list_item_content(line: str) -> tuple[str, int]:
    match = list_item_match(line)
    if not match:
        return "", 0
    return match.group(3), len(match.group(1))


def is_list_item_line(line: str) -> bool:
    return list_item_match(line) is not None


def is_continued_list_block(line: str, base_indent: int) -> bool:
    if not line.strip():
        return True
    if line.startswith(" " * (base_indent + 1)) or line.startswith("\t"):
        return True
    if line.strip().startswith("```"):
        return True
    return False


def dedent_block_lines(lines: list[str], base_indent: int) -> list[str]:
    dedented: list[str] = []
    for line in lines:
        if not line.strip():
            dedented.append("")
            continue
        indent = len(line) - len(line.lstrip(" "))
        strip_count = min(indent, base_indent + 4)
        dedented.append(line[strip_count:])
    return dedented


def collect_list_item_body(lines: list[str], index: int, base_indent: int) -> tuple[list[str], int]:
    body_lines: list[str] = []
    in_fence = False
    while index < len(lines):
        line = lines[index]
        stripped = line.strip()

        if in_fence:
            body_lines.append(line)
            if stripped.startswith("```"):
                in_fence = False
            index += 1
            continue

        if is_list_item_line(line):
            _, indent = list_item_content(line)
            if indent == base_indent:
                break

        if stripped.startswith("```"):
            in_fence = True
            body_lines.append(line)
            index += 1
            continue

        if not is_continued_list_block(line, base_indent):
            break
        body_lines.append(line)
        index += 1
    return body_lines, index


def convert_list(lines: list[str], index: int) -> tuple[str, int]:
    first = lines[index]
    _, base_indent = list_item_content(first)
    ordered = bool(re.match(r"^\s*\d+\.\s+", first))
    tag = "ol" if ordered else "ul"
    parts = [f"<{tag}>"]
    index = convert_list_items(lines, index, base_indent, parts)
    parts.append(f"</{tag}>")
    return "".join(parts), index


def body_has_leading_gap(body_lines: list[str]) -> bool:
    return bool(body_lines) and not body_lines[0].strip()


def first_body_block_kind(body_lines: list[str]) -> str | None:
    index = 0
    while index < len(body_lines) and not body_lines[index].strip():
        index += 1
    if index >= len(body_lines):
        return None

    line = body_lines[index]
    if line.strip().startswith("```"):
        return "code"
    if is_list_item_line(line):
        return "list"
    return "prose"


def list_item_continuation_separator(body_lines: list[str]) -> str:
    if not body_has_leading_gap(body_lines):
        return ""
    if first_body_block_kind(body_lines) == "prose":
        return "<br>"
    return ""


def is_checklist_line(line: str) -> bool:
    return bool(re.match(r"^\s*[-*]\s+\[x\]\s+", line))


def convert_checklist_group(lines: list[str], index: int) -> tuple[str, int]:
    parts = ['<ul class="docs-checklist">']
    while index < len(lines):
        if not is_checklist_line(lines[index]):
            break
        text = re.sub(r"^\s*[-*]\s+\[x\]\s+", "", lines[index])
        parts.append(f"<li>{inline_format(text)}</li>")
        index += 1
        while index < len(lines) and not lines[index].strip():
            index += 1
    parts.append("</ul>")
    return "".join(parts), index


def convert_list_items(
    lines: list[str],
    index: int,
    base_indent: int,
    parts: list[str],
) -> int:
    while index < len(lines):
        content, indent = list_item_content(lines[index])
        if indent != base_indent or not content:
            break

        index += 1
        body_lines, index = collect_list_item_body(lines, index, base_indent)

        item_html = inline_format(content)
        if body_lines:
            nested_html = convert_blocks(dedent_block_lines(body_lines, base_indent))
            separator = list_item_continuation_separator(body_lines)
            parts.append(f"<li>{item_html}{separator}{nested_html}</li>")
        else:
            parts.append(f"<li>{item_html}</li>")

    return index


def escape_attr(text: str) -> str:
    return html.escape(text, quote=True).replace("\r", "").replace("\n", "&#10;")


def render_copyable_codeblock(code_text: str, language: str) -> str:
    lang_class = f' class="language-{html.escape(language, quote=True)}"'
    escaped_code = html.escape(code_text)

    if not code_text.strip():
        return f"<pre><code{lang_class}></code></pre>"

    copy_button = (
        '<button class="copy-btn code-block-copy-btn" type="button" '
        f'data-command="{escape_attr(code_text)}" '
        'aria-label="Copy code block">'
        f"{COPY_ICON_SVG}<span>Copy</span></button>"
    )
    return (
        '<div class="code-block">'
        f"<pre><code{lang_class}>{escaped_code}</code></pre>"
        f"{copy_button}"
        "</div>"
    )


def convert_codeblock(lines: list[str], index: int) -> tuple[str, int]:
    fence = lines[index].strip()
    language = fence[3:].strip() or "text"
    index += 1
    code_lines = []
    while index < len(lines) and not lines[index].strip().startswith("```"):
        code_lines.append(lines[index])
        index += 1
    if index < len(lines):
        index += 1
    code_text = "\n".join(code_lines).rstrip("\n")
    return render_copyable_codeblock(code_text, language), index


def convert_heading(line: str, level: int, ids: dict[str, int]) -> str:
    text = line.lstrip("#").strip()
    base_id = slugify(text)
    count = ids.get(base_id, 0)
    ids[base_id] = count + 1
    heading_id = base_id if count == 0 else f"{base_id}-{count + 1}"
    tag = f"h{level}"
    return f'<{tag} id="{heading_id}">{inline_format(text)}</{tag}>'


def is_callout_placeholder(line: str) -> bool:
    stripped = line.strip()
    return stripped.startswith("__CALLOUT_") and stripped.endswith("__")


def convert_blocks(lines: list[str]) -> str:
    html_parts: list[str] = []
    heading_ids: dict[str, int] = {}
    index = 0

    while index < len(lines):
        line = lines[index]

        if not line.strip():
            index += 1
            continue

        if is_callout_placeholder(line):
            html_parts.append(line.strip())
            index += 1
            continue

        if line.strip().startswith("```"):
            block, index = convert_codeblock(lines, index)
            html_parts.append(block)
            continue

        if is_table_row(line):
            block, index = convert_table(lines, index)
            if block:
                html_parts.append(block)
                continue

        if is_checklist_line(line):
            block, index = convert_checklist_group(lines, index)
            html_parts.append(block)
            continue

        if is_list_item_line(line):
            block, index = convert_list(lines, index)
            html_parts.append(block)
            continue

        if line.startswith("#"):
            level = len(line) - len(line.lstrip("#"))
            if 1 <= level <= 4:
                html_parts.append(convert_heading(line, level, heading_ids))
                index += 1
                continue

        paragraph_lines = [line.lstrip()]
        index += 1
        while index < len(lines):
            next_line = lines[index]
            if (
                not next_line.strip()
                or next_line.strip().startswith("```")
                or next_line.startswith("#")
                or is_table_row(next_line)
                or is_list_item_line(next_line)
                or is_callout_placeholder(next_line)
            ):
                break
            if next_line.startswith("    ") or next_line.startswith("\t"):
                break
            paragraph_lines.append(next_line.strip())
            index += 1

        paragraph = " ".join(paragraph_lines)
        html_parts.append(f"<p>{inline_format(paragraph)}</p>")

    return "\n\n        ".join(html_parts)


def extract_toc(content_html: str) -> str:
    headings = re.findall(
        r'<h([23]) id="([^"]+)">(.*?)</h\1>',
        content_html,
        flags=re.DOTALL,
    )
    if not headings:
        return ""

    parts = ['<nav class="docs-toc-nav" aria-label="Table of contents">', "<ul>"]
    for level, heading_id, label in headings:
        label = html.unescape(re.sub(r"<[^>]+>", "", label))
        class_name = "docs-toc-h3" if level == "3" else ""
        class_attr = f' class="{class_name}"' if class_name else ""
        parts.append(
            f'<li{class_attr}><a href="#{heading_id}">{html.escape(label)}</a></li>'
        )
    parts.extend(["</ul>", "</nav>"])
    return "\n          ".join(parts)


def page_template(
    *,
    title: str,
    subtitle: str,
    content_html: str,
    toc_html: str,
    page_slug: str,
    kicker: str,
    assets_prefix: str,
    partials_prefix: str,
) -> str:
    return f"""<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8" />
<meta name="viewport" content="width=device-width, initial-scale=1.0" />
<title>{html.escape(title)} · Atryum</title>
<link rel="icon" type="image/svg+xml" href="{assets_prefix}/atryum-logo-favicon.svg" />
<link rel="preconnect" href="https://fonts.googleapis.com" />
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin />
<link href="https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600;700&family=JetBrains+Mono:wght@400;500&display=swap" rel="stylesheet" />
<link rel="stylesheet" href="{assets_prefix}/style.css" />
<script src="{assets_prefix}/includes.js" defer></script>
</head>
<body data-page="{html.escape(page_slug)}">
  <div data-include="{partials_prefix}/header.html"></div>

  <main>
    <div class="docs-layout">
      <aside class="docs-toc" aria-label="On this page">
        <div class="docs-toc-inner">
          <p class="docs-toc-label">On this page</p>
          {toc_html}
        </div>
      </aside>

      <div class="docs-main">
        <div class="docs-shell">
          <div class="docs-kicker">{html.escape(kicker)}</div>
          <h1 class="docs-title">{html.escape(title)}</h1>
          <p class="docs-intro">{inline_format(subtitle)}</p>

          <article class="docs-content">
        {content_html}
          </article>
        </div>
      </div>
    </div>
  </main>

  <div data-include="{partials_prefix}/footer.html"></div>
</body>
</html>
"""


def nav_page_link(path: Path, *, link_class: str = "") -> str:
    rel_html = html_output_path(path).relative_to(OUTPUT).as_posix()
    slug = page_slug(path)
    class_attr = f' class="{link_class}"' if link_class else ""
    return (
        f"<a{class_attr} href=\"/documentation/{rel_html}\" "
        f'data-page="{html.escape(slug, quote=True)}">'
        f"{html.escape(nav_label(path))}</a>"
    )


def render_docs_nav(pages: list[Path]) -> str:
    root_pages = sorted(
        [p for p in pages if len(p.relative_to(DRAFTS).parts) == 1],
        key=lambda path: numeric_sort_key(path.stem),
    )
    grouped: dict[str, list[Path]] = {}
    for path in pages:
        rel = path.relative_to(DRAFTS)
        if len(rel.parts) > 1:
            grouped.setdefault(rel.parts[0], []).append(path)

    parts: list[str] = []
    for path in root_pages:
        parts.append(nav_page_link(path, link_class="nav-link"))

    for directory in sorted(grouped, key=numeric_sort_key):
        parts.append('<details class="nav-dropdown">')
        parts.append(
            f'<summary class="nav-link">{html.escape(directory_label(directory).title())}</summary>'
        )
        parts.append('<div class="nav-menu">')
        for path in sorted(grouped[directory], key=lambda item: numeric_sort_key(item.stem)):
            parts.append(nav_page_link(path))
        parts.append("</div>")
        parts.append("</details>")

    return "\n        ".join(parts)


def write_docs_nav(pages: list[Path]) -> None:
    nav_path = PARTIALS / "docs-nav.html"
    generated = render_docs_nav(pages)
    nav_path.write_text(
        "<!-- Generated by website/scripts/md_to_html.py; do not edit. -->\n"
        f"{generated}\n",
        encoding="utf-8",
    )
    print(f"Wrote {nav_path.relative_to(ROOT.parent)}")


def remove_stale_html(generated: set[Path]) -> None:
    if not OUTPUT.exists():
        return
    for path in OUTPUT.rglob("*.html"):
        if path not in generated:
            path.unlink()
            print(f"Removed stale {path.relative_to(ROOT.parent)}")


def remove_empty_output_dirs() -> None:
    if not OUTPUT.exists():
        return
    for dirpath in sorted((path for path in OUTPUT.rglob("*") if path.is_dir()), reverse=True):
        if not any(dirpath.iterdir()):
            dirpath.rmdir()
            print(f"Removed empty {dirpath.relative_to(ROOT.parent)}")


def convert_file(path: Path) -> Path:
    global _partials_prefix

    assets_prefix, partials_prefix = path_prefixes(path_depth(path))
    _partials_prefix = partials_prefix

    source = path.read_text(encoding="utf-8")
    source = replace_md_links(source, path)
    source, callouts = extract_callouts(source)
    lines = source.splitlines()

    try:
        title_line = next(i for i, line in enumerate(lines) if line.startswith("# "))
    except StopIteration as exc:
        rel = path.relative_to(DRAFTS)
        raise ValueError(
            f"{rel}: each page must start with an H1 title (`# Page title`) "
            "followed by a one-line intro paragraph"
        ) from exc
    title = lines[title_line].lstrip("# ").strip()

    subtitle_index = title_line + 1
    while subtitle_index < len(lines) and not lines[subtitle_index].strip():
        subtitle_index += 1
    if subtitle_index >= len(lines) or lines[subtitle_index].startswith("#"):
        rel = path.relative_to(DRAFTS)
        raise ValueError(
            f"{rel}: add a one-line intro paragraph after the H1 title "
            "(blank line, then plain text before the first `##` section)"
        )
    subtitle = lines[subtitle_index].strip()

    body_lines = lines[:title_line] + lines[subtitle_index + 1 :]
    while body_lines and not body_lines[0].strip():
        body_lines.pop(0)

    content_html = convert_blocks(body_lines)
    content_html = inject_callouts(content_html, callouts)
    toc_html = extract_toc(content_html)

    rel = path.relative_to(DRAFTS)
    kicker = "Documentation"
    if len(rel.parts) > 1:
        kicker = f"Documentation / {directory_label(rel.parts[0])}"

    output_path = html_output_path(path)
    output_path.parent.mkdir(parents=True, exist_ok=True)
    output_path.write_text(
        page_template(
            title=title,
            subtitle=subtitle,
            content_html=content_html,
            toc_html=toc_html,
            page_slug=page_slug(path),
            kicker=kicker,
            assets_prefix=assets_prefix,
            partials_prefix=partials_prefix,
        ),
        encoding="utf-8",
    )
    print(f"Wrote {output_path.relative_to(ROOT.parent)}")
    return output_path


def main() -> None:
    global _rename_link_lookup

    OUTPUT.mkdir(parents=True, exist_ok=True)
    pages = sorted(DRAFTS.rglob("*.md"))

    stem_renames, _rename_link_lookup = detect_md_renames()
    if migrate_nav_label_overrides(stem_renames):
        persist_nav_label_overrides()
    warn_orphaned_nav_label_overrides(pages)

    normalize_md_links(pages)

    generated = {convert_file(path) for path in pages}
    write_docs_nav(pages)
    remove_stale_html(generated)
    remove_empty_output_dirs()


if __name__ == "__main__":
    main()
