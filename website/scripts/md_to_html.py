#!/usr/bin/env python3
"""Convert website/md-drafts/*.md to website/*.html."""

from __future__ import annotations

import html
import re
import unicodedata
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
DRAFTS = ROOT / "md-drafts"
OUTPUT = ROOT

PAGE_SLUGS = {
    "quickstart": "quickstart",
    "invocations": "invocations",
    "rules": "rules",
    "connect-mcp-servers": "connect-mcp-servers",
    "connect-validmind": "connect-validmind",
    "connect-agents": "connect-agents",
    "configure-llm-providers": "configure-llm-providers",
}

CALLOUT_PLACEHOLDER = "__CALLOUT_{index}__"
INSTALL_COMMANDS_PLACEHOLDER = "__INSTALL_COMMANDS__"
QUICKSTART_INSTALL_HEADING = "download atryum"

COPY_ICON_SVG = (
    '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" '
    'stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">'
    '<rect x="9" y="9" width="13" height="13" rx="2"/>'
    '<path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/>'
    "</svg>"
)


def slugify(text: str) -> str:
    text = re.sub(r"<[^>]+>", "", text)
    text = unicodedata.normalize("NFKD", text)
    text = text.encode("ascii", "ignore").decode("ascii")
    text = text.lower()
    text = re.sub(r"[^a-z0-9\s-]", "", text)
    text = re.sub(r"[\s_]+", "-", text.strip())
    return re.sub(r"-+", "-", text).strip("-")


def replace_md_links(text: str) -> str:
    def repl(match: re.Match[str]) -> str:
        label, url = match.group(1), match.group(2)
        if url.startswith("#"):
            return match.group(0)
        if ".md" in url:
            url = url.replace(".md", ".html")
        return f"[{label}]({url})"

    return re.sub(r"\[([^\]]+)\]\(([^)]+)\)", repl, text)


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
    text = re.sub(r"`([^`]+)`", r"<code>\1</code>", text)
    text = re.sub(
        r"\[([^\]]+)\]\(([^)]+)\)",
        lambda m: f'<a href="{html.escape(m.group(2), quote=True)}">{m.group(1)}</a>',
        text,
    )
    return restore_html_entities(restore_html_spans(text))


def inject_quickstart_install_partial(lines: list[str]) -> list[str]:
    """Replace quickstart download commands with the shared install partial."""
    result: list[str] = []
    index = 0

    while index < len(lines):
        line = lines[index]
        if (
            line.startswith("### ")
            and line.lstrip("# ").strip().lower() == QUICKSTART_INSTALL_HEADING
        ):
            result.append(line)
            index += 1
            while index < len(lines):
                if lines[index].startswith("## ") or lines[index].startswith("### "):
                    break
                index += 1
            result.append(INSTALL_COMMANDS_PLACEHOLDER)
            continue

        result.append(line)
        index += 1

    return result


def render_install_commands() -> str:
    return (
        '<div class="install install--docs">\n'
        '          <div data-include="partials/install-commands.html"></div>\n'
        "        </div>"
    )


def extract_callouts(source: str) -> tuple[str, list[str]]:
    pattern = re.compile(r":::\n(.*?)\n:::", re.DOTALL)
    callouts: list[str] = []

    def repl(match: re.Match[str]) -> str:
        index = len(callouts)
        callouts.append(match.group(1).strip("\n"))
        return CALLOUT_PLACEHOLDER.format(index=index)

    return pattern.sub(repl, source), callouts


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
        if line.startswith(" " * (base_indent + 4)):
            dedented.append(line[base_indent + 4 :])
        elif line.startswith(" " * (base_indent + 1)):
            dedented.append(line[base_indent + 4 :] if len(line) > base_indent + 4 else line.lstrip())
        else:
            dedented.append(line.lstrip())
    return dedented


def collect_list_item_body(lines: list[str], index: int, base_indent: int) -> tuple[list[str], int]:
    body_lines: list[str] = []
    while index < len(lines):
        line = lines[index]
        if is_list_item_line(line):
            _, indent = list_item_content(line)
            if indent == base_indent:
                break
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

        if line.strip() == INSTALL_COMMANDS_PLACEHOLDER:
            html_parts.append(render_install_commands())
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

        if re.match(r"^\s*[-*]\s+\[x\]\s+", line):
            text = re.sub(r"^\s*[-*]\s+\[x\]\s+", "", line)
            html_parts.append(f'<p class="docs-checklist">{inline_format(text)}</p>')
            index += 1
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
) -> str:
    return f"""<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8" />
<meta name="viewport" content="width=device-width, initial-scale=1.0" />
<title>{html.escape(title)} · Atryum</title>
<link rel="icon" type="image/svg+xml" href="assets/atryum-logo-favicon.svg" />
<link rel="preconnect" href="https://fonts.googleapis.com" />
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin />
<link href="https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600;700&family=JetBrains+Mono:wght@400;500&display=swap" rel="stylesheet" />
<link rel="stylesheet" href="assets/style.css" />
<script src="assets/includes.js" defer></script>
</head>
<body data-page="{html.escape(page_slug)}">
  <div data-include="partials/header.html"></div>

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
          <div class="docs-kicker">Documentation</div>
          <h1 class="docs-title">{html.escape(title)}</h1>
          <p class="docs-intro">{inline_format(subtitle)}</p>

          <article class="docs-content">
        {content_html}
          </article>
        </div>
      </div>
    </div>
  </main>

  <div data-include="partials/footer.html"></div>
</body>
</html>
"""


def convert_file(path: Path) -> None:
    source = path.read_text(encoding="utf-8")
    source = replace_md_links(source)
    source, callouts = extract_callouts(source)
    lines = source.splitlines()

    title_line = next(i for i, line in enumerate(lines) if line.startswith("# "))
    title = lines[title_line].lstrip("# ").strip()

    subtitle_index = title_line + 1
    while subtitle_index < len(lines) and not lines[subtitle_index].strip():
        subtitle_index += 1
    subtitle = lines[subtitle_index].strip()

    body_lines = lines[:title_line] + lines[subtitle_index + 1 :]
    while body_lines and not body_lines[0].strip():
        body_lines.pop(0)

    if path.stem == "quickstart":
        body_lines = inject_quickstart_install_partial(body_lines)
    content_html = convert_blocks(body_lines)
    content_html = inject_callouts(content_html, callouts)
    toc_html = extract_toc(content_html)
    page_slug = PAGE_SLUGS.get(path.stem, path.stem)

    output_path = OUTPUT / f"{path.stem}.html"
    output_path.write_text(
        page_template(
            title=title,
            subtitle=subtitle,
            content_html=content_html,
            toc_html=toc_html,
            page_slug=page_slug,
        ),
        encoding="utf-8",
    )
    print(f"Wrote {output_path.relative_to(ROOT.parent)}")


def main() -> None:
    for path in sorted(DRAFTS.glob("*.md")):
        convert_file(path)


if __name__ == "__main__":
    main()
