# Atryum documentation

Static site for [atryum.ai](https://atryum.ai) — a marketing homepage plus documentation built from Markdown.

## Directory layout

```
website/
├── index.html              # Homepage (manual)
├── md-drafts/              # Documentation source (edit these)
├── documentation/          # Generated HTML (do NOT edit)
├── partials/               # Shared HTML fragments (docs-nav.html is generated)
├── assets/                 # CSS, JS, images
└── scripts/md_to_html.py   # Markdown → HTML converter
```

## Author documentation

1. Add or edit a file under `md-drafts/`.

    - **Filename convention:** Prefix files and folders with a number to control nav order, then use dashes for the slug — for example `1_quickstart.md`, `1_integrations/2_connect-agents.md`.
    - Generated HTML paths keep those numeric prefixes. Nav link text comes from each page's `#` H1. Folder prefixes are removed from nav section headings only (`1_integrations/` → **integrations**).
    - Follow the **[ValidMind style guide](https://docs.validmind.ai/about/contributing/style-guide/style-guide.html)** for prose.

2. Run `make docs` from the repo root.

3. Commit the `.md` source and the generated HTML.

### Page structure

Each page starts with a title and a required one-line intro:

```markdown
# Page title

One-line intro shown under the title on the page.

## First section
```

The `#` heading becomes the page title. The first non-empty line after it becomes the intro (`docs-intro`). The build fails if that line is missing. Everything after the intro line is body content.

### File placement

| Location | Result |
| --- | --- |
| `md-drafts/1_quickstart.md` | `documentation/1_quickstart.html` |
| `md-drafts/1_integrations/2_connect-agents.md` | `documentation/1_integrations/2_connect-agents.html` |

Numeric prefixes control nav order. Folder prefixes are removed from section headings only (`1_integrations/` → **integrations**). Generated HTML paths keep the numbered source names.

### Cross-links

Link to other docs with relative `.md` paths. Use the numbered source filenames — the build resolves them to the correct HTML paths:

```markdown
See [Rules](3_rules.md) and [Connect agents](1_integrations/2_connect-agents.md).
```

External links use full URLs as usual.

### Callouts

Wrap content in `:::` fences:

```markdown
:::
To learn more, refer to **[Invocations](2_invocations.md)**.
:::
```

### Code blocks

Fenced blocks get copy buttons automatically:

````markdown
```bash
./atryum setup demo
```
````

### Checklists

```markdown
- [x] First completed step
- [x] Second completed step
```

### Small caps

Use inline HTML for UI labels:

```markdown
The <span style="font-variant: small-caps;">status</span> column shows the result.
```

### Tables

Standard pipe tables are supported.

## Build documentation website

From the repo root:

```bash
make docs
```

This regenerates:

- `documentation/**/*.html` — Mirrors the structure of `md-drafts/`
- `partials/docs-nav.html` — Documentation dropdown links in the header

Commit both the Markdown sources and the generated files. CI runs `make docs` on deploy and fails if the output is out of date.

### Preview website locally

From the repo root:

```bash
make preview-docs
```

`make preview-docs` runs `make docs` automatically before starting the server.

- Open [http://localhost:8000/](http://localhost:8000/) for the homepage.
- Partials such as `docs-nav.html` are loaded with JavaScript — a normal refresh picks up nav changes after `make docs`. Hard-refresh if you changed `includes.js` or CSS.

### Navigation logic

The Documentation dropdown is generated from the draft tree:

1. **Root pages first** — sorted by numeric prefix (`1_quickstart.md`, `2_invocations.md`, …).
2. **Subdirectories next** — sorted by folder numeric prefix (`1_integrations/`, …).
3. **Pages within each section** — sorted by their numeric prefix.

Section headings use the folder name with the numeric prefix removed (`1_integrations/` → **integrations**). Nav link URLs and generated HTML paths keep the numbered source filenames — nav link text comes from each page's `#` H1.

Use **`NAV_LABEL_OVERRIDES`** in `scripts/md_to_html.py` only when a menu label should differ from the page `#` title (otherwise the H1 is used).

## Do NOT edit

These files are generated or shared sitewide — change the source instead:

| File | Edit instead |
| --- | --- |
| `documentation/**/*.html` | `md-drafts/**/*.md` |
| `partials/docs-nav.html` | Add/reorder pages in `md-drafts/` using numeric prefixes |

These are maintained manually (or usually, agentically...):

- `index.html` — homepage (including install command)
- `partials/header.html`, `footer.html`
- `assets/style.css`, `assets/includes.js`, `assets/install.css`
- `install_atryum.sh` (repo root) — install script referenced by the homepage and Quickstart
