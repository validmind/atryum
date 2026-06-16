#!/usr/bin/env python3
"""Generate a single PDF manual from website/md-drafts/**/*.md.

The website's HTML generator remains the source of truth for page ordering and
Markdown conventions. This script intentionally has no third-party Python
dependencies so it can run in GitHub Pages builds and developer checkouts.
"""

from __future__ import annotations

import argparse
import html
import re
import sys
import textwrap
import xml.etree.ElementTree as ET
from dataclasses import dataclass, field
from pathlib import Path

sys.dont_write_bytecode = True

import md_to_html as site


DEFAULT_OUTPUT = site.ROOT / "atryum-docs.pdf"
LOGO_SVG = site.ROOT / "assets" / "atryum-logo.svg"

PAGE_SIZES = {
    "letter": (612.0, 792.0),
    "a4": (595.28, 841.89),
}

TEXT_PRIMARY = (0.149, 0.165, 0.165)
TEXT_SUBTLE = (0.349, 0.380, 0.384)
BRAND_DARK = (0.031, 0.243, 0.267)
BRAND_MID = (0.063, 0.361, 0.400)
BRAND_LIGHT = (0.384, 0.761, 0.808)
BRAND_PINK = (0.871, 0.149, 0.494)
INK_DARK = (0.016, 0.110, 0.129)
INK_LIGHT = (0.918, 0.965, 0.969)


@dataclass
class TextItem:
    x: float
    y: float
    text: str
    font: str
    size: float
    color: tuple[float, float, float] = TEXT_PRIMARY


@dataclass
class RuleItem:
    x: float
    y: float
    width: float
    color: tuple[float, float, float] = (0.878, 0.906, 0.906)


@dataclass
class RectItem:
    x: float
    y: float
    width: float
    height: float
    fill: tuple[float, float, float] | None = None
    stroke: tuple[float, float, float] | None = None


@dataclass
class PathItem:
    commands: list[tuple[str, tuple[float, ...]]]
    fill: tuple[float, float, float] | None = None
    stroke: tuple[float, float, float] | None = None
    stroke_width: float = 1.0


@dataclass
class PdfPage:
    width: float
    height: float
    items: list[TextItem | RuleItem | RectItem | PathItem] = field(default_factory=list)


@dataclass
class DocPage:
    path: Path
    title: str
    subtitle: str
    kicker: str
    body_lines: list[str]


def ordered_pages() -> list[Path]:
    pages = sorted(site.DRAFTS.rglob("*.md"))
    root_pages = sorted(
        [page for page in pages if len(page.relative_to(site.DRAFTS).parts) == 1],
        key=lambda path: site.numeric_sort_key(path.stem),
    )

    grouped: dict[str, list[Path]] = {}
    for page in pages:
        rel = page.relative_to(site.DRAFTS)
        if len(rel.parts) > 1:
            grouped.setdefault(rel.parts[0], []).append(page)

    ordered = list(root_pages)
    for directory in sorted(grouped, key=site.numeric_sort_key):
        ordered.extend(sorted(grouped[directory], key=lambda path: site.numeric_sort_key(path.stem)))
    return ordered


def read_doc_page(path: Path) -> DocPage:
    lines = path.read_text(encoding="utf-8").splitlines()

    try:
        title_line = next(index for index, line in enumerate(lines) if line.startswith("# "))
    except StopIteration as exc:
        raise ValueError(f"{path.relative_to(site.DRAFTS)}: missing H1 title") from exc

    title = clean_inline(lines[title_line].lstrip("# ").strip(), include_urls=False)

    subtitle_index = title_line + 1
    while subtitle_index < len(lines) and not lines[subtitle_index].strip():
        subtitle_index += 1
    if subtitle_index >= len(lines) or lines[subtitle_index].startswith("#"):
        raise ValueError(f"{path.relative_to(site.DRAFTS)}: missing one-line intro")

    subtitle = clean_inline(lines[subtitle_index].strip())
    body_lines = lines[:title_line] + lines[subtitle_index + 1 :]
    while body_lines and not body_lines[0].strip():
        body_lines.pop(0)

    rel = path.relative_to(site.DRAFTS)
    kicker = "Documentation"
    if len(rel.parts) > 1:
        kicker = f"Documentation / {site.directory_label(rel.parts[0])}"

    return DocPage(
        path=path,
        title=title,
        subtitle=subtitle,
        kicker=kicker,
        body_lines=body_lines,
    )


def clean_inline(text: str, *, include_urls: bool = True) -> str:
    text = re.sub(r"</?span[^>]*>", "", text)

    def link_replacement(match: re.Match[str]) -> str:
        label = clean_inline(match.group(1), include_urls=False)
        url = match.group(2).strip()
        if include_urls and url.startswith(("http://", "https://", "mailto:")):
            return f"{label} ({url})"
        return label

    text = re.sub(r"\[([^\]]+)\]\(([^)]+)\)", link_replacement, text)
    text = re.sub(r"`([^`]*)`", r"\1", text)
    text = re.sub(r"\*\*([^*]+)\*\*", r"\1", text)
    text = re.sub(r"(?<!\*)\*([^*]+)\*(?!\*)", r"\1", text)
    return html.unescape(text).strip()


def color_from_hex(value: str | None) -> tuple[float, float, float] | None:
    if not value or value == "none":
        return None
    value = value.strip().lstrip("#")
    if len(value) != 6:
        return None
    return tuple(int(value[index : index + 2], 16) / 255 for index in (0, 2, 4))


def svg_path_tokens(path_data: str) -> list[str]:
    return re.findall(
        r"[A-Za-z]|[-+]?(?:\d*\.\d+|\d+\.?)(?:[eE][-+]?\d+)?",
        path_data,
    )


def is_svg_command(token: str) -> bool:
    return len(token) == 1 and token.isalpha()


def parse_svg_path(path_data: str) -> list[tuple[str, tuple[float, ...]]]:
    tokens = svg_path_tokens(path_data)
    commands: list[tuple[str, tuple[float, ...]]] = []
    index = 0
    command = ""
    current_x = 0.0
    current_y = 0.0

    def read_number() -> float:
        nonlocal index
        value = float(tokens[index])
        index += 1
        return value

    while index < len(tokens):
        if is_svg_command(tokens[index]):
            command = tokens[index]
            index += 1

        if not command:
            raise ValueError("SVG path data starts without a command")

        relative = command.islower()
        upper = command.upper()

        if upper == "Z":
            commands.append(("Z", ()))
            command = ""
            continue

        if upper == "M":
            first_pair = True
            while index < len(tokens) and not is_svg_command(tokens[index]):
                x = read_number()
                y = read_number()
                if relative:
                    x += current_x
                    y += current_y
                current_x, current_y = x, y
                commands.append(("M" if first_pair else "L", (x, y)))
                first_pair = False
            command = "l" if relative else "L"
            continue

        if upper == "L":
            while index < len(tokens) and not is_svg_command(tokens[index]):
                x = read_number()
                y = read_number()
                if relative:
                    x += current_x
                    y += current_y
                current_x, current_y = x, y
                commands.append(("L", (x, y)))
            continue

        if upper == "H":
            while index < len(tokens) and not is_svg_command(tokens[index]):
                x = read_number()
                if relative:
                    x += current_x
                current_x = x
                commands.append(("L", (current_x, current_y)))
            continue

        if upper == "V":
            while index < len(tokens) and not is_svg_command(tokens[index]):
                y = read_number()
                if relative:
                    y += current_y
                current_y = y
                commands.append(("L", (current_x, current_y)))
            continue

        if upper == "C":
            while index < len(tokens) and not is_svg_command(tokens[index]):
                x1 = read_number()
                y1 = read_number()
                x2 = read_number()
                y2 = read_number()
                x = read_number()
                y = read_number()
                if relative:
                    x1 += current_x
                    y1 += current_y
                    x2 += current_x
                    y2 += current_y
                    x += current_x
                    y += current_y
                current_x, current_y = x, y
                commands.append(("C", (x1, y1, x2, y2, x, y)))
            continue

        raise ValueError(f"Unsupported SVG path command: {command}")

    return commands


def transformed_svg_path(
    commands: list[tuple[str, tuple[float, ...]]],
    *,
    x: float,
    y: float,
    scale: float,
    viewbox_height: float,
) -> list[tuple[str, tuple[float, ...]]]:
    def point(svg_x: float, svg_y: float) -> tuple[float, float]:
        return x + (svg_x * scale), y + ((viewbox_height - svg_y) * scale)

    transformed: list[tuple[str, tuple[float, ...]]] = []
    for command, values in commands:
        if command in {"M", "L"}:
            transformed.append((command, point(values[0], values[1])))
        elif command == "C":
            x1, y1 = point(values[0], values[1])
            x2, y2 = point(values[2], values[3])
            x3, y3 = point(values[4], values[5])
            transformed.append(("C", (x1, y1, x2, y2, x3, y3)))
        else:
            transformed.append((command, values))
    return transformed


def add_svg_logo(layout: PdfLayout, *, x: float, y: float, width: float) -> None:
    root = ET.fromstring(LOGO_SVG.read_text(encoding="utf-8"))
    _, _, view_width, view_height = [float(value) for value in root.attrib["viewBox"].split()]
    scale = width / view_width

    for child in root:
        if not child.tag.endswith("path"):
            continue
        stroke_width = float(child.attrib.get("stroke-width", "1")) * scale
        layout.page.items.append(
            PathItem(
                transformed_svg_path(
                    parse_svg_path(child.attrib["d"]),
                    x=x,
                    y=y,
                    scale=scale,
                    viewbox_height=view_height,
                ),
                fill=color_from_hex(child.attrib.get("fill")),
                stroke=color_from_hex(child.attrib.get("stroke")),
                stroke_width=stroke_width,
            )
        )


def is_table_row(line: str) -> bool:
    stripped = line.strip()
    return stripped.startswith("|") and stripped.endswith("|")


def is_table_separator(line: str) -> bool:
    stripped = line.strip().strip("|")
    if not stripped:
        return False
    cells = [cell.strip() for cell in stripped.split("|")]
    return all(re.fullmatch(r":?-{3,}:?", cell or "-") for cell in cells)


def list_item_match(line: str) -> re.Match[str] | None:
    return re.match(r"^(\s*)([-*]|\d+\.)\s+(.*)$", line)


def dedent_continuation(lines: list[str]) -> list[str]:
    non_empty = [len(line) - len(line.lstrip(" ")) for line in lines if line.strip()]
    if not non_empty:
        return lines
    indent = min(non_empty)
    return [line[indent:] if len(line) >= indent else line for line in lines]


def approximate_text_width(text: str, size: float, font: str) -> float:
    if font == "F3":
        return len(text) * size * 0.60

    width = 0.0
    for char in text:
        if char in "il.,:;|'! ":
            width += 0.26
        elif char in "mwMW@#%&":
            width += 0.82
        elif char.isupper():
            width += 0.64
        else:
            width += 0.52
    return width * size


def wrap_text_to_width(text: str, width: float, size: float, font: str) -> list[str]:
    text = re.sub(r"\s+", " ", text.strip())
    if not text:
        return []

    average_char_width = max(size * (0.60 if font == "F3" else 0.52), 1)
    rough_width = max(12, int(width / average_char_width))
    wrapped: list[str] = []

    for line in textwrap.wrap(
        text,
        width=rough_width,
        break_long_words=False,
        break_on_hyphens=False,
    ):
        current = line
        while approximate_text_width(current, size, font) > width and " " in current:
            words = current.split(" ")
            candidate_words: list[str] = []
            while words and approximate_text_width(" ".join(candidate_words + [words[0]]), size, font) <= width:
                candidate_words.append(words.pop(0))
            if not candidate_words:
                break
            wrapped.append(" ".join(candidate_words))
            current = " ".join(words)
        if current:
            wrapped.append(current)
    return wrapped


class PdfLayout:
    def __init__(self, page_size: tuple[float, float]) -> None:
        self.width, self.height = page_size
        self.margin = 54.0
        self.bottom = 56.0
        self.top = self.height - self.margin
        self.content_width = self.width - (2 * self.margin)
        self.pages: list[PdfPage] = []
        self.y = self.top
        self.new_page()

    @property
    def page(self) -> PdfPage:
        return self.pages[-1]

    def new_page(self) -> None:
        self.pages.append(PdfPage(self.width, self.height))
        self.y = self.top

    def ensure_space(self, required: float) -> None:
        if self.y - required < self.bottom:
            self.new_page()

    def add_rule(self, gap_before: float = 8.0, gap_after: float = 14.0) -> None:
        self.ensure_space(gap_before + gap_after + 1)
        self.y -= gap_before
        self.page.items.append(RuleItem(self.margin, self.y, self.content_width))
        self.y -= gap_after

    def add_rect(
        self,
        x: float,
        y: float,
        width: float,
        height: float,
        *,
        fill: tuple[float, float, float] | None = None,
        stroke: tuple[float, float, float] | None = None,
    ) -> None:
        self.page.items.append(RectItem(x, y, width, height, fill=fill, stroke=stroke))

    def add_text(
        self,
        text: str,
        *,
        x: float,
        y: float,
        font: str = "F1",
        size: float = 10.5,
        color: tuple[float, float, float] = TEXT_PRIMARY,
    ) -> None:
        self.page.items.append(TextItem(x, y, text, font, size, color))

    def centered_x(self, text: str, size: float, font: str = "F1") -> float:
        return (self.width - approximate_text_width(text, size, font)) / 2

    def add_wrapped(
        self,
        text: str,
        *,
        font: str = "F1",
        size: float = 10.5,
        color: tuple[float, float, float] = TEXT_PRIMARY,
        indent: float = 0.0,
        gap_before: float = 0.0,
        gap_after: float = 7.0,
        line_height: float | None = None,
        prefix: str = "",
        align: str = "left",
    ) -> None:
        if not text and not prefix:
            return

        line_height = line_height or size * 1.38
        x = self.margin + indent
        prefix_width = approximate_text_width(prefix, size, font) if prefix else 0.0
        text_width = self.content_width - indent - prefix_width
        lines = wrap_text_to_width(text, text_width, size, font)
        if not lines:
            lines = [""]

        self.ensure_space(gap_before + (line_height * len(lines)) + gap_after)
        self.y -= gap_before
        for index, line in enumerate(lines):
            self.y -= line_height
            if index == 0 and prefix:
                self.page.items.append(TextItem(x, self.y, prefix, font, size, color))
            text_x = x + (prefix_width if prefix else 0)
            if align == "center" and not prefix:
                text_x = (self.width - approximate_text_width(line, size, font)) / 2
            elif align == "right" and not prefix:
                text_x = self.width - self.margin - approximate_text_width(line, size, font)
            self.page.items.append(TextItem(text_x, self.y, line, font, size, color))
        self.y -= gap_after

    def add_code(self, lines: list[str], *, indent: float = 0.0) -> None:
        size = 8.7
        line_height = 11.8
        x = self.margin + indent
        width = self.content_width - indent
        chars_per_line = max(20, int(width / (size * 0.60)))
        self.ensure_space(18)
        self.y -= 5

        for raw_line in lines or [""]:
            expanded = raw_line.expandtabs(4)
            chunks = textwrap.wrap(
                expanded,
                width=chars_per_line,
                replace_whitespace=False,
                drop_whitespace=False,
                break_long_words=True,
                break_on_hyphens=False,
            ) or [""]
            for chunk in chunks:
                self.ensure_space(line_height + 3)
                self.y -= line_height
                self.page.items.append(TextItem(x, self.y, chunk.rstrip(), "F3", size, BRAND_DARK))
        self.y -= 8


def render_markdown_body(layout: PdfLayout, lines: list[str], *, indent: float = 0.0) -> None:
    index = 0
    while index < len(lines):
        line = lines[index]
        stripped = line.strip()

        if not stripped:
            index += 1
            continue

        if stripped == ":::":
            index += 1
            callout_lines: list[str] = []
            while index < len(lines) and lines[index].strip() != ":::":
                callout_lines.append(lines[index])
                index += 1
            if index < len(lines):
                index += 1
            layout.add_wrapped("Note", font="F2", size=10, color=BRAND_DARK, indent=indent, gap_before=8, gap_after=2)
            render_markdown_body(layout, callout_lines, indent=indent + 14)
            continue

        if stripped.startswith("```"):
            index += 1
            code_lines: list[str] = []
            while index < len(lines) and not lines[index].strip().startswith("```"):
                code_lines.append(lines[index])
                index += 1
            if index < len(lines):
                index += 1
            layout.add_code(code_lines, indent=indent)
            continue

        if line.startswith("#"):
            level = len(line) - len(line.lstrip("#"))
            if 2 <= level <= 4:
                text = clean_inline(line.lstrip("# ").strip(), include_urls=False)
                if level == 2:
                    layout.add_wrapped(text, font="F2", size=15, color=BRAND_DARK, indent=indent, gap_before=14, gap_after=5)
                elif level == 3:
                    layout.add_wrapped(text, font="F2", size=12.2, color=BRAND_DARK, indent=indent, gap_before=10, gap_after=4)
                else:
                    layout.add_wrapped(text, font="F2", size=10.8, color=BRAND_DARK, indent=indent, gap_before=8, gap_after=3)
                index += 1
                continue

        if is_table_row(line) and index + 1 < len(lines) and is_table_separator(lines[index + 1]):
            header = " | ".join(clean_inline(cell.strip(), include_urls=False) for cell in line.strip().strip("|").split("|"))
            layout.add_wrapped(header, font="F2", size=9.2, color=BRAND_DARK, indent=indent, gap_before=5, gap_after=2)
            index += 2
            while index < len(lines) and is_table_row(lines[index]):
                row = " | ".join(clean_inline(cell.strip()) for cell in lines[index].strip().strip("|").split("|"))
                layout.add_wrapped(row, font="F1", size=8.8, color=TEXT_PRIMARY, indent=indent + 8, gap_after=2)
                index += 1
            layout.y -= 4
            continue

        match = list_item_match(line)
        if match:
            index = render_list(layout, lines, index, indent=indent)
            continue

        paragraph_lines = [line.strip()]
        index += 1
        while index < len(lines):
            next_line = lines[index]
            if (
                not next_line.strip()
                or next_line.strip() == ":::"
                or next_line.strip().startswith("```")
                or next_line.startswith("#")
                or is_table_row(next_line)
                or list_item_match(next_line)
            ):
                break
            paragraph_lines.append(next_line.strip())
            index += 1
        paragraph = clean_inline(" ".join(paragraph_lines))
        layout.add_wrapped(paragraph, size=10.4, color=TEXT_PRIMARY, indent=indent, gap_after=6)


def render_list(layout: PdfLayout, lines: list[str], index: int, *, indent: float) -> int:
    while index < len(lines):
        line = lines[index]
        if not line.strip():
            index += 1
            continue

        match = list_item_match(line)
        if not match:
            break

        base_indent = len(match.group(1).replace("\t", "    "))
        marker = match.group(2)
        content = match.group(3).strip()
        if re.match(r"\[x\]\s+", content):
            prefix = "[x] "
            content = re.sub(r"\[x\]\s+", "", content)
        elif marker.endswith("."):
            prefix = f"{marker} "
        else:
            prefix = "- "

        layout.add_wrapped(clean_inline(content), size=10.2, indent=indent, prefix=prefix, gap_after=3)
        index += 1

        continuation: list[str] = []
        while index < len(lines):
            next_line = lines[index]
            if not next_line.strip():
                continuation.append(next_line)
                index += 1
                continue
            next_match = list_item_match(next_line)
            next_indent = len(next_match.group(1).replace("\t", "    ")) if next_match else None
            if next_match and next_indent <= base_indent:
                break
            if next_line.startswith(" ") or next_line.startswith("\t"):
                continuation.append(next_line)
                index += 1
                continue
            break

        if continuation:
            render_markdown_body(layout, dedent_continuation(continuation), indent=indent + 18)

    layout.y -= 3
    return index


def render_cover(layout: PdfLayout, title: str) -> None:
    logo_width = 190.0
    logo_height = logo_width * (51 / 173)
    logo_x = (layout.width - logo_width) / 2
    logo_y = layout.height - 152
    add_svg_logo(layout, x=logo_x, y=logo_y, width=logo_width)

    layout.y = logo_y - 48
    layout.add_wrapped(
        "The control plane for agent actions.",
        font="F2",
        size=31,
        color=BRAND_DARK,
        gap_after=18,
        line_height=36,
        align="center",
    )
    layout.add_wrapped(
        "Self-hosted and lightweight. Every tool your agents call passes through one place to be approved, denied, and logged.",
        size=12.5,
        color=TEXT_SUBTLE,
        gap_after=34,
        line_height=17,
        indent=58,
        align="center",
    )

    card_width = min(500, layout.content_width)
    card_height = 54
    card_x = (layout.width - card_width) / 2
    card_y = layout.y - card_height
    layout.add_text("INSTALL", x=card_x, y=card_y + card_height + 13, font="F2", size=8.5, color=(0.643, 0.686, 0.686))
    layout.add_rect(card_x, card_y, card_width, card_height, fill=INK_DARK, stroke=BRAND_DARK)
    command = "curl -fsSL https://github.com/validmind/atryum/raw/main/install_atryum.sh | bash"
    layout.add_text("$", x=card_x + 18, y=card_y + 21, font="F3", size=9.6, color=BRAND_LIGHT)
    layout.add_text(command, x=card_x + 31, y=card_y + 21, font="F3", size=8.2, color=INK_LIGHT)
    layout.add_rect(card_x + card_width - 70, card_y + 14, 50, 24, fill=(0.075, 0.176, 0.192), stroke=(0.180, 0.294, 0.314))
    layout.add_text("Copy", x=card_x + card_width - 58, y=card_y + 22, font="F2", size=8.8, color=(0.847, 0.969, 0.980))

    layout.y = card_y - 48
    button_text = "Quickstart"
    button_width = 92
    button_height = 28
    button_x = (layout.width - button_width) / 2
    button_y = layout.y - button_height
    layout.add_rect(button_x, button_y, button_width, button_height, fill=BRAND_LIGHT, stroke=BRAND_LIGHT)
    layout.add_text(button_text, x=layout.centered_x(button_text, 9.6, "F2"), y=button_y + 10, font="F2", size=9.6, color=INK_DARK)

    layout.y = button_y - 74
    layout.add_wrapped(title, font="F2", size=12, color=BRAND_DARK, gap_after=5, align="center")
    layout.add_wrapped("PDF manual generated from website/md-drafts", size=9.5, color=TEXT_SUBTLE, gap_after=3, align="center")
    layout.add_wrapped("https://atryum.org", size=9.5, color=TEXT_SUBTLE, gap_after=3, align="center")


def render_toc(layout: PdfLayout, docs: list[DocPage], starts: dict[Path, int] | None = None) -> None:
    layout.add_wrapped("Contents", font="F2", size=20, color=BRAND_DARK, gap_after=12)
    previous_section = ""
    for doc in docs:
        section = doc.kicker
        if section != previous_section:
            layout.add_wrapped(section, font="F2", size=8.8, color=TEXT_SUBTLE, gap_before=7, gap_after=3)
            previous_section = section

        page_text = ""
        if starts is not None:
            page_text = f"  {starts[doc.path]}"
        layout.add_wrapped(f"{doc.title}{page_text}", size=10.5, color=TEXT_PRIMARY, gap_after=2)


def render_doc(layout: PdfLayout, doc: DocPage) -> int:
    layout.new_page()
    start_page = len(layout.pages)
    layout.add_wrapped(doc.kicker.upper(), font="F2", size=8.5, color=BRAND_MID, gap_after=7)
    layout.add_wrapped(doc.title, font="F2", size=24, color=BRAND_DARK, gap_after=10, line_height=29)
    layout.add_wrapped(doc.subtitle, size=11.2, color=TEXT_SUBTLE, gap_after=12, line_height=15.5)
    layout.add_rule(gap_before=2, gap_after=10)
    render_markdown_body(layout, doc.body_lines)
    return start_page


def build_pages(docs: list[DocPage], page_size: tuple[float, float], title: str) -> list[PdfPage]:
    starts: dict[Path, int] | None = None
    final_pages: list[PdfPage] = []

    for _ in range(4):
        layout = PdfLayout(page_size)
        render_cover(layout, title)
        layout.new_page()
        render_toc(layout, docs, starts)

        next_starts: dict[Path, int] = {}
        for doc in docs:
            next_starts[doc.path] = render_doc(layout, doc)

        final_pages = layout.pages
        if starts == next_starts:
            break
        starts = next_starts

    return final_pages


def pdf_number(value: float) -> str:
    return f"{value:.3f}".rstrip("0").rstrip(".")


def pdf_text(text: str) -> str:
    encoded = text.encode("cp1252", errors="replace")
    return f"<{encoded.hex().upper()}>"


def page_content_stream(page: PdfPage) -> bytes:
    operations: list[str] = []
    for item in page.items:
        if isinstance(item, PathItem):
            operations.append("q")
            if item.fill is not None:
                r, g, b = item.fill
                operations.append(f"{pdf_number(r)} {pdf_number(g)} {pdf_number(b)} rg")
            if item.stroke is not None:
                r, g, b = item.stroke
                operations.append(f"{pdf_number(r)} {pdf_number(g)} {pdf_number(b)} RG {pdf_number(item.stroke_width)} w")
            for command, values in item.commands:
                if command == "M":
                    operations.append(f"{pdf_number(values[0])} {pdf_number(values[1])} m")
                elif command == "L":
                    operations.append(f"{pdf_number(values[0])} {pdf_number(values[1])} l")
                elif command == "C":
                    operations.append(
                        f"{pdf_number(values[0])} {pdf_number(values[1])} "
                        f"{pdf_number(values[2])} {pdf_number(values[3])} "
                        f"{pdf_number(values[4])} {pdf_number(values[5])} c"
                    )
                elif command == "Z":
                    operations.append("h")
            if item.fill is not None and item.stroke is not None:
                operations.append("B")
            elif item.fill is not None:
                operations.append("f")
            elif item.stroke is not None:
                operations.append("S")
            operations.append("Q")
            continue

        if isinstance(item, RectItem):
            operations.append("q")
            if item.fill is not None:
                r, g, b = item.fill
                operations.append(f"{pdf_number(r)} {pdf_number(g)} {pdf_number(b)} rg")
            if item.stroke is not None:
                r, g, b = item.stroke
                operations.append(f"{pdf_number(r)} {pdf_number(g)} {pdf_number(b)} RG")
            operations.append(
                f"{pdf_number(item.x)} {pdf_number(item.y)} "
                f"{pdf_number(item.width)} {pdf_number(item.height)} re"
            )
            if item.fill is not None and item.stroke is not None:
                operations.append("B")
            elif item.fill is not None:
                operations.append("f")
            else:
                operations.append("S")
            operations.append("Q")
            continue

        if isinstance(item, RuleItem):
            r, g, b = item.color
            operations.append(
                f"q {pdf_number(r)} {pdf_number(g)} {pdf_number(b)} RG "
                f"{pdf_number(item.x)} {pdf_number(item.y)} m "
                f"{pdf_number(item.x + item.width)} {pdf_number(item.y)} l S Q"
            )
            continue

        r, g, b = item.color
        operations.append(
            f"BT /{item.font} {pdf_number(item.size)} Tf "
            f"{pdf_number(r)} {pdf_number(g)} {pdf_number(b)} rg "
            f"1 0 0 1 {pdf_number(item.x)} {pdf_number(item.y)} Tm "
            f"{pdf_text(item.text)} Tj ET"
        )
    return "\n".join(operations).encode("ascii")


def add_footers(pages: list[PdfPage]) -> None:
    total = len(pages)
    for index, page in enumerate(pages, start=1):
        label = f"{index} / {total}"
        size = 8.0
        x = page.width - 54.0 - approximate_text_width(label, size, "F1")
        page.items.append(TextItem(x, 30.0, label, "F1", size, TEXT_SUBTLE))


def write_pdf(pages: list[PdfPage], output: Path) -> None:
    add_footers(pages)

    objects: list[bytes | None] = [None]
    objects.extend(
        [
            None,
            None,
            b"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>",
            b"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica-Bold /Encoding /WinAnsiEncoding >>",
            b"<< /Type /Font /Subtype /Type1 /BaseFont /Courier /Encoding /WinAnsiEncoding >>",
        ]
    )

    page_object_ids: list[int] = []
    for page in pages:
        stream = page_content_stream(page)
        content_id = len(objects)
        objects.append(b"<< /Length " + str(len(stream)).encode("ascii") + b" >>\nstream\n" + stream + b"\nendstream")

        page_id = len(objects)
        page_object_ids.append(page_id)
        page_object = (
            f"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 {pdf_number(page.width)} {pdf_number(page.height)}] "
            f"/Resources << /Font << /F1 3 0 R /F2 4 0 R /F3 5 0 R >> >> "
            f"/Contents {content_id} 0 R >>"
        ).encode("ascii")
        objects.append(page_object)

    kids = " ".join(f"{page_id} 0 R" for page_id in page_object_ids)
    objects[1] = b"<< /Type /Catalog /Pages 2 0 R >>"
    objects[2] = f"<< /Type /Pages /Count {len(page_object_ids)} /Kids [{kids}] >>".encode("ascii")

    output.parent.mkdir(parents=True, exist_ok=True)
    data = bytearray(b"%PDF-1.4\n%\xE2\xE3\xCF\xD3\n")
    offsets = [0]
    for object_id, content in enumerate(objects[1:], start=1):
        if content is None:
            raise RuntimeError(f"PDF object {object_id} was not populated")
        offsets.append(len(data))
        data.extend(f"{object_id} 0 obj\n".encode("ascii"))
        data.extend(content)
        data.extend(b"\nendobj\n")

    xref_offset = len(data)
    data.extend(f"xref\n0 {len(objects)}\n".encode("ascii"))
    data.extend(b"0000000000 65535 f \n")
    for offset in offsets[1:]:
        data.extend(f"{offset:010d} 00000 n \n".encode("ascii"))
    data.extend(
        f"trailer\n<< /Size {len(objects)} /Root 1 0 R >>\nstartxref\n{xref_offset}\n%%EOF\n".encode("ascii")
    )

    output.write_bytes(data)


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Generate the Atryum documentation PDF.")
    parser.add_argument(
        "--output",
        type=Path,
        default=DEFAULT_OUTPUT,
        help=f"PDF output path (default: {DEFAULT_OUTPUT.relative_to(site.ROOT.parent)})",
    )
    parser.add_argument(
        "--paper",
        choices=sorted(PAGE_SIZES),
        default="letter",
        help="PDF page size (default: letter)",
    )
    parser.add_argument(
        "--title",
        default="Atryum Documentation",
        help="Title shown on the PDF cover page",
    )
    return parser.parse_args()


def display_path(path: Path) -> Path:
    try:
        return path.relative_to(site.ROOT.parent)
    except ValueError:
        return path


def main() -> None:
    args = parse_args()
    docs = [read_doc_page(path) for path in ordered_pages()]
    pages = build_pages(docs, PAGE_SIZES[args.paper], args.title)
    write_pdf(pages, args.output)
    print(f"Wrote {display_path(args.output)}")


if __name__ == "__main__":
    main()
