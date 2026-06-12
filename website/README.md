# Atryum documentation

Static site for [atryum.ai](https://atryum.ai) — a marketing homepage plus documentation built from Markdown.

## Directory layout

```
website/
├── index.html              # Homepage (manual)
├── md-drafts/              # Documentation source (edit these)
├── documentation/          # Generated HTML (do NOT edit)
├── partials/               # Shared HTML fragments
├── assets/                 # CSS, JS, images
└── scripts/md_to_html.py   # Markdown → HTML converter
```

## Author documentation

1. Add or edit a file under `md-drafts/`.

    - **Filename convention:** Prefix files and folders with a number to control nav order, then use dashes for the slug — for example `1_quickstart.md`, `1_integrations/2_connect-agents.md`. Numeric prefixes are stripped from generated HTML paths and nav labels.
    - Follow the **[ValidMind style guide](https://docs.validmind.ai/about/contributing/style-guide/style-guide.html)** for prose.

2. Run `make docs` from the repo root.

3. Commit the `.md` source and the generated HTML.

### Page structure

Each page starts with a title and subtitle:

```markdown
# Page title

One-line intro shown under the title on the page.

## First section
```

The `#` heading becomes the page title and the first paragraph becomes the intro. Everything after that is body content.

### File placement

| Location | Result |
| --- | --- |
| `md-drafts/1_quickstart.md` | `documentation/1_quickstart.html` |
| `md-drafts/1_integrations/2_connect-agents.md` | `documentation/1_integrations/2_connect-agents.html` |

Numeric prefixes control nav order. Folder prefixes are removed from section headings only (`1_integrations/` → **integrations**). Generated HTML paths keep the numbered source names.

### Cross-links

Link to other docs with relative `.md` paths. Use the numbered source filenames; the build resolves them to the correct HTML paths:

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

Or equivalently:

```bash
python3 -m http.server 8000 --directory website
```

- Open [http://localhost:8000/](http://localhost:8000/) for the homepage.
- Partials such as `docs-nav.html` are loaded with JavaScript — a normal refresh picks up nav changes after `make docs`. Hard-refresh if you changed `includes.js` or CSS.

### Navigation logic

The Documentation dropdown is generated from the draft tree:

1. **Root pages first** — sorted by numeric prefix (`1_quickstart.md`, `2_invocations.md`, …).
2. **Subdirectories next** — sorted by folder numeric prefix (`1_integrations/`, …).
3. **Pages within each section** — sorted by their numeric prefix.

Section headings use the folder name with the numeric prefix removed (`1_integrations/` → **integrations**). Nav links and generated HTML paths keep the numbered source filenames.

Use **`NAV_LABEL_OVERRIDES`** in `scripts/md_to_html.py` only when a menu label should differ from the page `#` title (otherwise the H1 is used).

### Quickstart install block

The Quickstart page replaces the `### Download Atryum` section with the shared install partial from `partials/install-commands.html`. Edit install commands there — **not in the generated HTML.**

## Do NOT edit

These files are generated or shared sitewide — change the source instead:

| File | Edit instead |
| --- | --- |
| `documentation/**/*.html` | `md-drafts/**/*.md` |
| `partials/docs-nav.html` | Add/reorder pages in `md-drafts/` using numeric prefixes |

These are maintained manually (or usually, agentically...):

- `index.html` — homepage
- `partials/header.html`, `footer.html`, `install-commands.html`
- `assets/style.css`, `assets/includes.js`, `assets/install.css`
