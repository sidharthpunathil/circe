<div align="center">

# Skillbuilder

**Your own registry for Claude agent skills.**
Write them in the browser, group them into folders, install them into any project with one command.

[![Deploy on Railway](https://railway.com/button.svg)](https://railway.com/new/template?template=https%3A%2F%2Fgithub.com%2Fsidharthpunathil%2Fskillbuilder)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/go-1.26-00ADD8.svg)](go.mod)

<img src="docs/screenshot.png" alt="Skillbuilder home page" width="820">

</div>

---

## What this is

[Agent Skills](https://code.claude.com/docs/en/skills) are folders containing a `SKILL.md`
file that teach Claude how to do something. They pile up fast, they live scattered across
machines and repos, and there's no good way to say *"give this project those six skills."*

Skillbuilder is a small self-hosted app that fixes that:

- **Write and edit skills** in a proper editor, with validation against the real
  `SKILL.md` rules.
- **Group them into folders.** A folder is the unit you install.
- **Install with one command** — `skillbuilder my-folder` drops every skill in it into
  `./.claude/skills/`.
- **Import from anywhere** — your local `~/.claude/skills`, or any public GitHub repo.
  Content is copied in, so it stays yours even if the source repo disappears.
- **No database.** Everything is plain files on disk, so the storage directory *is* your
  backup. Commit it to git if you like.

It's two Go binaries and a directory of markdown. That's the whole system.

## Deploy it

### One click

[![Deploy on Railway](https://railway.com/button.svg)](https://railway.com/new/template?template=https%3A%2F%2Fgithub.com%2Fsidharthpunathil%2Fskillbuilder)

Set **`SKILLBUILDER_PASSWORD`** when prompted, and attach a **volume mounted at `/data`**
so your skills survive redeploys. That's it.

It's cheap to run: the image is ~15 MB, it idles at around 20 MB of RAM and effectively
zero CPU, and it needs no database — just a small volume. Enable **App Sleeping** and it
costs close to nothing when you aren't using it.

### Docker

```bash
docker run -d -p 8080:8080 \
  -v skillbuilder-data:/data \
  -e SKILLBUILDER_PASSWORD=pick-something-good \
  ghcr.io/sidharthpunathil/skillbuilder:latest
```

### From source

```bash
git clone https://github.com/sidharthpunathil/skillbuilder
cd skillbuilder
make build && ./bin/skillbuilder-server   # http://127.0.0.1:8080
```

`make build` downloads the Tailwind standalone binary and compiles both binaries. There
is no Node dependency at build time or runtime, and templates and assets are embedded, so
the result is a single self-contained binary.

> **Running it anywhere public? Set `SKILLBUILDER_PASSWORD`.** Without it, anyone who can
> reach the URL can edit and delete your skills. The server warns loudly on startup if you
> bind to a non-loopback address without one.

## Use it

Create a folder on the site, tick the skills that belong in it, then in any project:

```bash
skillbuilder my-folder        # pick which of its skills to install
skillbuilder my-folder -y     # install all of them
skillbuilder add design-taste # install a single skill
```

Skills land in `.claude/skills/<name>/` and are recorded in `skillbuilder.json`, so
`skillbuilder pull` restores the same set on any machine. Local edits are never clobbered
— `pull` holds back anything you've changed unless you pass `--force`.

Point the CLI at your deployment once:

```bash
skillbuilder config set registry https://your-app.up.railway.app
skillbuilder config set token   your-password
```

### Commands

| Command | What it does |
| --- | --- |
| `skillbuilder <folder>` | Choose skills from a folder and install them. `-y` takes all. |
| `skillbuilder add <skill>…` | Install specific skills; no args opens a picker. |
| `skillbuilder list` | Show installed skills and whether they've drifted. `--remote` lists the registry. |
| `skillbuilder search <query>` | Search the registry. |
| `skillbuilder pull` | Re-download everything in the lockfile. |
| `skillbuilder remove <skill>…` | Delete skills and drop them from the lockfile. |
| `skillbuilder push <dir>` | Publish a local skill directory, companion files included. |
| `skillbuilder config` | Show or set the registry URL and token. |

Flags: `-d/--dir` (install location, remembered after the first install), `--force`,
`--dry-run`, `--registry`.

### Without the CLI

The server publishes a [Claude Code marketplace manifest](https://code.claude.com/docs/en/plugin-marketplaces),
so folders install with stock Claude Code and no extra tooling:

```
/plugin marketplace add https://your-app.up.railway.app
```

## Configure

Everything is environment variables. Nothing is baked into the binary.

| Variable | Default | What it does |
| --- | --- | --- |
| `PORT` | `8080` | Listen port. Most hosts inject this. |
| `SKILLBUILDER_ADDR` | `127.0.0.1:8080` | Full listen address, if you'd rather set it directly. |
| `SKILLBUILDER_DATA` | `./data` | Where skills and folders are stored. |
| `SKILLBUILDER_PASSWORD` | — | Enables auth. **Set this for any public deployment.** |
| `SKILLBUILDER_USER` | any | Optional basic-auth username. |
| `SKILLBUILDER_BASE_URL` | — | Public origin, if behind a proxy or custom domain. |
| `SKILLBUILDER_SEED_REPO` | — | On first boot, when empty, import every skill from this repo. |
| `SKILLBUILDER_SEED_FOLDER` | — | Put the seeded skills into a folder with this name. |
| `GITHUB_TOKEN` | — | Raises the GitHub rate limit (60/hr unauthenticated) and reaches private repos. |

Seeding only runs when the registry is empty, so redeploys never overwrite your edits.

## How data is stored

```
data/
├── skills/
│   └── my-skill/
│       ├── SKILL.md          # YAML frontmatter + markdown body
│       └── references/…      # optional companion files
├── bundles/
│   └── my-folder.json        # { name, title, description, skills: [...] }
└── sources.json              # where each imported skill came from
```

A skill directory here is byte-for-byte what gets installed into a project, so you can
also just copy one into `~/.claude/skills/` by hand.

Frontmatter round-trips losslessly. Keys the app doesn't model — `argument-hint`,
`user-invokable`, anything else you've added — are preserved rather than dropped, and
`allowed-tools` keeps whichever form it was authored in. The parser is tested against a
library of 116 real-world skills.

## HTTP API

Everything the CLI does is available under `/api/v1`:

```
GET  /api/v1/skills?q=&tag=            list and search
GET  /api/v1/skills/{name}             one skill, with body and folder membership
POST /api/v1/skills                    create
PUT  /api/v1/skills/{name}             update
GET  /api/v1/bundles                   list folders
GET  /api/v1/bundles/{name}            manifest with skills expanded
GET  /api/v1/bundles/{name}/archive.tar.gz
GET  /api/v1/download?skill=a&skill=b  tar.gz of specific skills
GET  /api/v1/health                    unauthenticated, for health checks
```

## Develop

```bash
make dev      # server with live template reload
make watch    # rebuild CSS on change (run alongside make dev)
make test     # go test ./... -race
make lint     # go vet + gofmt check
```

Contributions are welcome — issues and pull requests both. If you're changing the UI,
`make dev` reloads templates on every request so you don't need to restart.

## Security notes

The server binds to loopback by default and rejects cross-origin mutations, because a page
in your browser can otherwise reach a local port. Bundled-file paths are validated against
traversal and symlink escapes, archive extraction rejects Zip-Slip entries, and markdown is
rendered with raw HTML escaped so an imported third-party skill can't inject script into
your registry.

Found something? Open an issue.

## Credits

The interface design is heavily inspired by **[animations.dev](https://animations.dev/) by
[Emil Kowalski](https://emilkowal.ski/)** — the warm neutral palette, the way surfaces
recede instead of lifting, the restraint with the amber accent, and the idea that hierarchy
should come from weight and brightness rather than size. If you build interfaces, his
course is worth your time.

The skill format is Anthropic's [Agent Skills](https://code.claude.com/docs/en/skills).

## License

MIT — see [LICENSE](LICENSE).
