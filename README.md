# Skillbuilder

A personal registry for Claude agent skills. Write skills in the browser, group them
into bundles, and install them into any project from the command line.

It is two binaries and a directory of plain files:

- **`skillbuilder-server`** — a Go web app (html/template + Tailwind) that stores, edits
  and serves your skills.
- **`skillbuilder`** — a CLI that installs bundles into a project's `.claude/skills/`.
- **`data/`** — the source of truth. Ordinary directories and markdown, so committing
  `data/` to git *is* your backup.

## Quick start

```bash
make build                        # compiles CSS, then both binaries into ./bin
./bin/skillbuilder-server         # http://127.0.0.1:8080
```

Open the site and go to **Import**. Two sources:

- **Local directory** — pull in the skills you already have in `~/.claude/skills`.
  Symlinked skills are followed and copied by value, so the registry holds real content
  rather than pointers.
- **GitHub repo** — paste any repo URL (e.g. `emilkowalski/skills`) and it finds every
  `SKILL.md` in it at any depth, showing each skill's description, file count and when it
  was last touched upstream. Content is copied into `data/`, so it stays yours even if
  the repo is deleted or goes private.

Then, in any project:

```bash
skillbuilder frontend-kit          # pick from the folder interactively
skillbuilder frontend-kit -y       # install all of it
skillbuilder add design-taste      # install one skill
```

A **folder** is a named set of skills you install together — that's the unit the CLI
takes. Create one on the site, tick the skills that belong in it, and its name becomes
the install command.

Skills land in `.claude/skills/<name>/` and are recorded in `skillbuilder.json`, so
`skillbuilder pull` can refresh them later.

## How data is stored

```
data/
├── skills/
│   └── my-skill/
│       ├── SKILL.md          # YAML frontmatter + markdown body
│       └── references/…      # optional bundled files
├── bundles/
│   └── frontend-kit.json     # { name, title, description, skills: [...] }
└── sources.json              # where each imported skill came from
```

`sources.json` records the repo, commit SHA, path and upstream timestamp for anything
imported from GitHub, which is what powers the **Re-sync from source** button on a skill
page. Set `GITHUB_TOKEN` (or `GH_TOKEN`) to reach private repos and raise the API rate
limit.

A skill directory here is byte-for-byte what gets installed into a project, so you can
also just copy one into `~/.claude/skills/` by hand.

Frontmatter is parsed and rewritten losslessly: keys the app does not model (such as
`argument-hint` or `user-invokable`) are preserved rather than dropped, and
`allowed-tools` keeps whichever form it was authored in.

## CLI

| Command | What it does |
| --- | --- |
| `skillbuilder <folder>` | Choose skills from a folder and install them. `-y` takes all. |
| `skillbuilder init <folder>` | The same thing, spelled out. |
| `skillbuilder add <skill>…` | Install specific skills; no args opens a picker. |
| `skillbuilder list` | Show installed skills and whether they have drifted. `--remote` lists the registry. |
| `skillbuilder search <query>` | Search the registry. |
| `skillbuilder pull` | Re-download everything in the lockfile. Local edits are held back unless `--force`. |
| `skillbuilder remove <skill>…` | Delete skills and drop them from the lockfile. |
| `skillbuilder push <dir>` | Publish a local skill directory (including its companion files) to the registry. |
| `skillbuilder config` | Show or set the registry URL. |

Common flags: `-d/--dir` (install location, remembered in the lockfile after the first
install), `--force`, `--dry-run`, `--registry`. The registry can also come from
`SKILLBUILDER_REGISTRY` or `~/.skillbuilder/config.json`.

## Installing without the CLI

The server also publishes a Claude Code marketplace manifest, so bundles can be
installed by stock Claude Code with no extra tooling:

```
/plugin marketplace add http://127.0.0.1:8080
```

## HTTP API

Everything the CLI does is available over JSON under `/api/v1`:

```
GET  /api/v1/skills?q=&tag=            list and search
GET  /api/v1/skills/{name}             one skill, with body and bundle membership
POST /api/v1/skills                    create (used by push)
PUT  /api/v1/skills/{name}             update
GET  /api/v1/bundles                   list bundles
GET  /api/v1/bundles/{name}            manifest with skills expanded
GET  /api/v1/bundles/{name}/archive.tar.gz
GET  /api/v1/download?skill=a&skill=b  tar.gz of specific skills
GET  /api/v1/health
```

## Server flags

| Flag | Default | Purpose |
| --- | --- | --- |
| `-addr` | `127.0.0.1:8080` | Listen address. Bound to localhost by design. |
| `-data` | `./data` | Where skills and bundles live. |
| `-dev` | off | Reload templates and assets from disk on every request. |
| `-base-url` | — | Public origin, if running behind a proxy. |

Each also reads `SKILLBUILDER_ADDR`, `SKILLBUILDER_DATA` and `SKILLBUILDER_BASE_URL`.

## Notes on safety

The server binds to localhost and rejects cross-origin mutations, because a page in your
browser can otherwise reach a local port. Bundled-file paths are validated against
traversal and symlink escapes, archive extraction in the CLI rejects Zip-Slip entries,
and markdown is rendered with raw HTML escaped so an imported third-party skill cannot
inject script into your registry.

## Development

```bash
make dev      # server with live template reload
make watch    # rebuild CSS on change (run alongside `make dev`)
make test     # go test ./... -race
make lint     # go vet + gofmt check
```

Tailwind is the standalone binary — there is no Node dependency at build or run time.
Templates and assets are embedded with `go:embed`, so the built binary is self-contained.
