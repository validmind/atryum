# Atryum documentation

Static site for [atryum.org](https://atryum.org) — a marketing homepage plus documentation built from Markdown.

## Directory layout

```
website/
├── index.html              # Homepage (manual)
├── md-drafts/              # Documentation source (edit these)
├── documentation/          # Generated HTML (do NOT edit)
├── partials/               # Shared HTML fragments (docs-nav.html is generated)
├── assets/                 # CSS, JS, images
├── CNAME                   # Custom domain for GitHub Pages (manual)
└── scripts/                # Markdown → HTML/PDF converters
```

## Author documentation

1. Add or edit a file under `md-drafts/`.

    - **Filename convention:** Prefix files and folders with a number to control nav order, then use dashes for the slug — for example `1_quickstart.md`, `1_integrations/2_connect-agents.md`.
    - Generated HTML paths keep those numeric prefixes. Nav link text comes from each page's `#` H1. Folder prefixes are removed from nav section headings only (`1_integrations/` → **integrations**).
    - Follow the **[ValidMind style guide](https://docs.validmind.ai/about/contributing/style-guide/style-guide.html)** for prose.

2. Run `make docs` from the repo root.

3. Commit the `.md` source and the generated output (`documentation/**/*.html` and `partials/docs-nav.html`), as well as any other manually edited files.

### Page structure

Each page starts with a title and a required one-line intro:

```markdown
# Page title

One-line intro shown under the title on the page.

## First section
```

- The `#` heading becomes the page title. The first non-empty line after it becomes the intro (`docs-intro`). The build fails if that line is missing. Everything after the intro line is body content.
- Pages in subdirectories also show a kicker above the title — for example `Documentation / integrations` for files under `1_integrations/`.

### Cross-links

Link to other docs with relative `.md` paths. Use the numbered source filenames — the build resolves them to the correct HTML paths:

```markdown
See [Rules](3_rules.md) and [Connect agents](1_integrations/2_connect-agents.md).
```

- Unprefixed slugs such as `rules.md` also resolve when the target is unique, but prefer numbered source filenames for consistency with the repo.
- External links use full URLs as usual.

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

Completed steps only (`- [x]`). Unchecked boxes (`- [ ]`) render as ordinary list items.

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

- `documentation/**/*.html` — Mirrors the structure of `md-drafts/` (and removes stale HTML for deleted drafts)
- `partials/docs-nav.html` — Documentation dropdown links in the header

Commit both the Markdown sources and the generated files. Pushes to `main` that touch `website/**`, `Makefile`, or `.github/workflows/pages.yml` run `make docs` in CI and fail if `documentation/` or `partials/docs-nav.html` are out of date.

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

- Section headings use the folder name with the numeric prefix removed (`1_integrations/` → **integrations**).
- Doc pages also get an auto-generated **On this page** table-of-contents sidebar from `##` and `###` headings in the body.
- Use **`NAV_LABEL_OVERRIDES`** in `scripts/md_to_html.py` only when a dropdown label should differ from the page `#` title. Keys are prefix-stripped stems — for example `quickstart` for `1_quickstart.md`, or `connect-agents` for `2_connect-agents.md`.

#### Example file placement

| Location | Result |
| --- | --- |
| `md-drafts/1_quickstart.md` | `documentation/1_quickstart.html` |
| `md-drafts/1_integrations/2_connect-agents.md` | `documentation/1_integrations/2_connect-agents.html` |

## Build documentation PDF

From the repo root:

```bash
make docs-pdf
```

This generates `website/atryum-docs.pdf` from the same Markdown sources and navigation order. The PDF is ignored locally and built into the GitHub Pages artifact during deployment, so it is available at `/atryum-docs.pdf` without committing the binary file.

## Do NOT edit

These files are generated or shared sitewide — change the source instead:

| File | Edit instead |
| --- | --- |
| `documentation/**/*.html` | `md-drafts/**/*.md` |
| `partials/docs-nav.html` | Add/reorder pages in `md-drafts/` using numeric prefixes |

These are maintained manually (or usually, agentically...):

- `index.html` — homepage (including install command)
- `partials/header.html`, `footer.html`
- `assets/style.css`, `assets/includes.js`, `assets/install.css`, `assets/atryum-logo.svg`, `assets/atryum-logo-favicon.svg`
- `CNAME` — GitHub Pages custom domain (`atryum.org`)
- `install_atryum.sh` (repo root) — install script referenced by the homepage and Quickstart
