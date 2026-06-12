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

    Filename convention: `use-dashes-please.md` (Align more or less with guide title. Follow the **[ValidMind style guide](https://docs.validmind.ai/about/contributing/style-guide/style-guide.html)**.)

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
| `md-drafts/quickstart.md` | `documentation/quickstart.html` |
| `md-drafts/integrations/connect-agents.md` | `documentation/integrations/connect-agents.html` |

Subdirectories appear in the nav as lowercase section headings (for example, `integrations/` → **integrations**). The page kicker shows `Documentation / integrations`.

### Cross-links

Link to other docs with relative `.md` paths. The build resolves them to the correct HTML paths:

```markdown
See [Rules](../rules.md) and [Connect agents](integrations/connect-agents.md).
```

External links use full URLs as usual.

### Callouts

Wrap content in `:::` fences:

```markdown
:::
To learn more, refer to **[Invocations](invocations.md)**.
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
make preview
```

Or equivalently:

```bash
python3 -m http.server 8000 --directory website
```

- Open [http://localhost:8000/](http://localhost:8000/) for the homepage and [http://localhost:8000/documentation/quickstart.html](http://localhost:8000/documentation/quickstart.html) for docs.
- Hard-refresh after CSS or JS changes.

### Navigation logic

The Documentation dropdown is generated from the draft tree. To control order, edit `scripts/md_to_html.py`:

- **`ROOT_NAV_ORDER`** — top-level pages (default: Quickstart, Invocations, Rules)
- **`SUBDIR_NAV_ORDER`** — pages within a subdirectory
- **`NAV_LABEL_OVERRIDES`** — optional menu labels when they should differ from the page `#` title (otherwise the H1 is used)

New pages and subdirectories are picked up automatically. Unlisted pages sort alphabetically after any explicit order.

### Quickstart install block

The Quickstart page replaces the `### Download Atryum` section with the shared install partial from `partials/install-commands.html`. Edit install commands there — **not in the generated HTML.**

## Do NOT edit

These files are generated or shared sitewide — change the source instead:

| File | Edit instead |
| --- | --- |
| `documentation/**/*.html` | `md-drafts/**/*.md` |
| `partials/docs-nav.html` | Add/reorder pages in `md-drafts/` and `md_to_html.py` nav config |

These are maintained manually (or usually, agentically...):

- `index.html` — homepage
- `partials/header.html`, `footer.html`, `install-commands.html`
- `assets/style.css`, `assets/includes.js`, `assets/install.css`
