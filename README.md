<div align="center">

<img src="docs/logo.jpg" alt="" width="84" height="84" style="border-radius:20px">

# Circe

**Your own registry for Claude agent skills, rules and MCP servers.**
Write them in the browser, group them into folders, install a folder into any project with one command.

[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/go-1.26-00ADD8.svg)](go.mod)

<img src="docs/screenshot.png" alt="Circe home page" width="820">

</div>

---

## What this is

[Agent Skills](https://code.claude.com/docs/en/skills) are folders containing a `SKILL.md`
file that teach Claude how to do something. They pile up fast, they live scattered across
machines and repos, and there's no good way to say *"give this project those six skills."*

Circe is a small self-hosted app that fixes that. It holds three kinds of thing, because a
project needs three different things from you:

| | What it is | Where it lands in a project |
| --- | --- | --- |
| **Skills** | A procedure the agent can follow | `./.claude/skills/` |
| **Rules** | How you work, and what to avoid | `CLAUDE.md` / `AGENTS.md` |
| **MCP servers** | A live connection to something outside the repo | `.mcp.json` |

- **Write and edit** in a proper editor, with validation against the real `SKILL.md` rules.
- **Group them into folders.** A folder holds any mix of the three, and is the unit you
  install. A project needs a procedure, a house style and a database connection at the
  same time, so one folder carries all of it.
- **Install with one command**: `circe my-folder` installs everything in the folder, each
  kind where it belongs. `circe skills`, `circe rules` and `circe mcp` narrow it to one
  kind when that is all you want.
- **Import from anywhere**: a repo on any git host (GitHub, GitLab, Bitbucket, Codeberg,
  self-hosted Gitea/Forgejo), a pasted `SKILL.md` or `.mcp.json`, your local
  `~/.claude/skills`, or a share code from someone else's registry. Content is copied in,
  so it stays yours even if the source disappears.
- **Docs in the app.** A Docs tab with every command already pointed at your own
  deployment, so nothing has to be adapted before it is run.
- **Sync to GitHub.** Connect a repository and push: every skill, rule, server and folder
  is written there as ordinary markdown and JSON, and committed. Pull brings it back. Your
  library becomes something you can read in a browser, review in a pull request, and
  restore on any machine.
- **Your content stays plain.** Storage is a small embedded database, but every row keeps
  the exact document it came from, and `circe-server export` writes the whole registry back
  out as a directory of markdown and JSON. Nothing here is a format you'd need this program
  to read.

It's two Go binaries, one file of data, and no service to run beside it.

## Deploy it

### One click

[![Deploy on Railway](https://railway.com/button.svg)](https://railway.com/new/template?template=https%3A%2F%2Fgithub.com%2Fsidharthpunathil%2Fcirce)

Set **`CIRCE_PASSWORD`** when prompted, and attach a **volume mounted at `/data`**
so your skills survive redeploys. That's it.

It's cheap to run: the image is about 30 MB, it idles at around 50 MB of RAM and
effectively zero CPU, and there is no database server to run beside it, just a small
volume. Enable **App Sleeping** and it costs close to nothing when you aren't using it.

### Docker

```bash
docker run -d -p 8080:8080 \
  -v circe-data:/data \
  -e CIRCE_PASSWORD=pick-something-good \
  ghcr.io/sidharthpunathil/circe:latest
```

### From source

```bash
git clone https://github.com/sidharthpunathil/circe
cd circe
make build && ./bin/circe-server   # http://127.0.0.1:8080
```

`make build` downloads the Tailwind standalone binary and compiles both binaries. There
is no Node dependency at build time or runtime, and templates and assets are embedded, so
the result is a single self-contained binary.

> **Running it anywhere public? Set `CIRCE_PASSWORD`.** Without it, anyone who can
> reach the URL can edit and delete your skills. The server warns loudly on startup if you
> bind to a non-loopback address without one.

## Install the CLI

```bash
curl -fsSL https://raw.githubusercontent.com/sidharthpunathil/circe/main/install.sh | sh
```

macOS and Linux, `amd64` and `arm64`. Or `go install github.com/sidharthpunathil/circe/cmd/circe@latest`,
or `make build` from a clone. Full options in [docs/install.md](docs/install.md).

## Use it

Create a folder on the site, tick the skills, rules and servers that belong in it, then in
any project:

```bash
circe my-folder        # install the whole folder, picking as you go
circe my-folder -y     # take all of it, no prompts
circe skills my-folder # only its skills
circe add design-taste # install a single skill
```

Skills land in `.claude/skills/<name>/` and are recorded in `circe.json`, so
`circe pull` restores the same set on any machine. Local edits are never clobbered:
`pull` holds back anything you've changed unless you pass `--force`. Rules merge into
`CLAUDE.md` and `AGENTS.md`, servers into `.mcp.json`, each into the project's own file
and without disturbing what you put there.

Point the CLI at your deployment once:

```bash
circe login https://your-app.up.railway.app
```

It prompts for the passcode, checks it against the registry, and only then saves, so a typo
fails here rather than somewhere confusing later. `circe logout` forgets it.

### Commands

| Command | What it does |
| --- | --- |
| `circe <folder>` | Install everything in a folder: its skills, its rules, its MCP servers. `-y` takes all. |
| `circe skills <folder>` | Only the skills. No args lists the folders that have some. |
| `circe rules <folder>` | Only the rules, merged into this project's CLAUDE.md / AGENTS.md. No args lists them. |
| `circe mcp <folder>` | Only the MCP servers, merged into this project's `.mcp.json`. No args lists them. |
| `circe add <skill>…` | Install specific skills; no args opens a picker. |
| `circe list` | Show installed skills and whether they've drifted. `--remote` lists the registry. |
| `circe search <query>` | Search the registry. |
| `circe pull` | Re-download everything in the lockfile. |
| `circe remove <skill>…` | Delete skills and drop them from the lockfile. |
| `circe push <dir>` | Publish one skill, or every skill in a directory. |
| `circe import <code>` | Install from someone else's share code: no setup needed. |
| `circe login [url]` | Connect to a registry, verifying the token before saving. |
| `circe config` | Show the resolved registry, config and install paths. |

Flags: `-d/--dir` (where skills go, remembered after the first install), `-o/--out` (which
config the servers merge into), `--context` (how `CLAUDE.md` is applied), `--force`,
`--dry-run`, `--registry`.

### Without the CLI

The server publishes a [Claude Code marketplace manifest](https://code.claude.com/docs/en/plugin-marketplaces),
so folders install with stock Claude Code and no extra tooling:

```
/plugin marketplace add https://your-app.up.railway.app
```

## Getting a local library into a hosted registry

The **Local directory** import reads the filesystem of whatever machine the registry runs
on: which for a hosted deployment is a container that cannot see your laptop. Push from
your machine instead:

```bash
circe login https://your-app.up.railway.app
circe push ~/.claude/skills          # every skill in there
```

`push` publishes one skill if the directory has a `SKILL.md`, otherwise every subdirectory
that does. Companion files come along; existing skills are left alone unless you pass
`--force`. Directories that aren't part of a skill: `.venv`, `node_modules`,
`__pycache__`, `.git`: are excluded, which matters because a skill that vendors a
virtualenv can be hundreds of megabytes.

## Rules

Skills teach Claude *how to do something*. Rules tell it *how you work*: the text that ends
up in a project's `CLAUDE.md` or `AGENTS.md`.

Write rules on the **Rules** tab, add them to a folder, then:

```bash
circe rules house-style
```

Each rule picks which files it belongs in, so a rule can go to `CLAUDE.md`, `AGENTS.md`, or
both. They merge into the project's own files inside a marked block, so anything already
there is untouched and re-running replaces only that block. The folder's own `CLAUDE.md`
goes into that same block, ahead of the rules.

The bare command installs the lot, and the three verbs narrow it:

```bash
circe frontend-kit          # everything in the folder
circe skills frontend-kit   # what it can do
circe rules  frontend-kit   # how you work
circe mcp    frontend-kit   # what it can reach
```

Run a verb with no folder and it lists the folders that have something of that kind.

Because the rules and the folder's own instructions share one block,
`circe skills <folder>` does not write that block when the folder has rules: it says so
and leaves the file alone, rather than replacing the block with half of itself.

## MCP servers

A skill teaches the agent a procedure. An MCP server gives it something to reach: a
database, an API, a browser, your issue tracker.

Add a server on the **MCP** tab: give it a command or a URL, and name the environment
variables it needs. Or paste a `.mcp.json` you already have on the Import page and take the
servers out of it. Put the ones you want in a folder, then:

```bash
circe mcp essentials
```

That merges them into the project's `.mcp.json`. `circe essentials` does the same and
installs whatever else that folder holds. Servers the project already has are
reported and left alone rather than replaced, unless you pass `--force`, and any top level
keys Circe does not model are carried through untouched. Re-running when nothing has
changed does not rewrite the file.

**Your keys are not stored here.** Circe records the *name* of each variable a server
needs, never its value, and the generated config refers to it as `${VAR}` so the value
comes from your own shell. After installing, the CLI tells you which variables are still
unset:

```
2 added in .mcp.json
set these before the servers will start:
  DATABASE_URI                  postgres
  GITHUB_PERSONAL_ACCESS_TOKEN  github · optional
```

This also means a share code can never leak a token.

## Sharing

Everyone self-hosts, so there is no central index: a share is a code your registry mints
for one folder or skill:

1. Open a folder or skill and hit **Create share code**.
2. Send someone the `sb_…` code, or the link.
3. They paste it into **Import → From a code**, or run `circe import sb_…`.

The content is copied into their registry, so their copy keeps working even if yours goes
away. Sharing is opt-in per resource and revocable: **Stop sharing** kills the code
immediately. A password-protected registry stays private: the only public endpoints are
the ones serving resources you explicitly shared.

## Project instructions

A folder can carry a `CLAUDE.md` and an `AGENTS.md`. Installing it merges them into the
project's own files inside a marked block:

```markdown
<!-- circe:start my-folder -->
<!-- Managed by circe. Edits inside this block are replaced on reinstall. -->

  …the folder's instructions…

<!-- circe:end my-folder -->
```

Anything you wrote outside the block is untouched, re-installing replaces only the block,
and several folders can contribute to one file. Use `--context=skip` to leave the file
alone, or `--context=overwrite` to replace it wholesale.

The folder's rules go in the same block, after its instructions, because a folder should
say one thing to the agent rather than two.

## Settings

Access tokens can be set in the UI at **Settings** instead of redeploying. An environment
variable of the same name always wins, so deployment config is never silently overridden.

Tokens are kept apart from everything else, in their own table that nothing exporting or
syncing ever reads. So a token cannot be swept into a repository by a push, an export or a
share code, and that is a property of where it is stored rather than a filter someone has
to remember to write.

## Configure

Everything is environment variables. Nothing is baked into the binary.

| Variable | Default | What it does |
| --- | --- | --- |
| `PORT` | `8080` | Listen port. Most hosts inject this. |
| `CIRCE_ADDR` | `127.0.0.1:8080` | Full listen address, if you'd rather set it directly. |
| `CIRCE_DATA` | `./data` | Where skills and folders are stored. |
| `CIRCE_PASSWORD` | none | Enables auth. **Set this for any public deployment.** |
| `CIRCE_USER` | any | Optional basic-auth username. |
| `CIRCE_BASE_URL` | none | Public origin, if behind a proxy or custom domain. |
| `CIRCE_SEED_REPO` | none | On first boot, when empty, import every skill from this repo. |
| `CIRCE_SEED_FOLDER` | none | Put the seeded skills into a folder with this name. |
| `CIRCE_NO_DASHBOARD` | none | Set to `1` to remove the PocketBase dashboard at `/_/` and its API. |
| `GITHUB_TOKEN` | none | Raises the GitHub rate limit (60/hr unauthenticated) and reaches private repos. Also what sync commits with. |

Seeding only runs when the registry is empty, so redeploys never overwrite your edits.

### Coming from Skillbuilder

This project used to be called Skillbuilder. The rename is handled for you:

- `SKILLBUILDER_*` variables are still read, so an existing deployment keeps running
  without being reconfigured. Add the `CIRCE_*` names when convenient.
- A project's `skillbuilder.json` lockfile is read and upgraded to `circe.json` on the
  next write.
- `~/.skillbuilder/config.json` is read if `~/.circe/config.json` does not exist yet.
- Blocks already written into a `CLAUDE.md` with the old marker are still recognised, so
  reinstalling replaces them rather than adding a second copy.
- Sessions and share codes are unaffected: the values they are built from were deliberately
  left alone.
- Rule folders and MCP folders are folded into ordinary folders on first boot, so
  `circe rules <name>` and `circe mcp <name>` keep working on the same names.

One thing needs a manual step, because the format allows no alias. If you added this
registry to Claude Code as a plugin marketplace, re-add it:

```
/plugin marketplace remove skillbuilder
/plugin marketplace add https://your-app.up.railway.app
```

## How data is stored

Skills, rules, servers and folders live in a [PocketBase](https://pocketbase.io) database
inside the data directory. PocketBase is a single Go library rather than a service, so this
is still one binary and one volume, with a dashboard at `/_/` for looking at the raw data.

```
data/
├── data.db          # the registry
├── auxiliary.db     # request logs
└── storage/         # companion files that ship with a skill
```

What matters more than the storage is what each row holds. **Every record keeps the exact
document it came from**: the whole `SKILL.md`, the whole rule markdown, the whole server
JSON. Nothing is shredded into columns and reassembled, so writing the registry back out is
a copy rather than an export format:

```bash
circe-server export -out ./my-library
```

```
my-library/
├── skills/
│   └── my-skill/
│       ├── SKILL.md          # YAML frontmatter + markdown body
│       └── references/…      # optional companion files
├── rules/
│   └── house-style.md        # frontmatter + the text that lands in CLAUDE.md
├── mcps/
│   └── github.json           # one MCP server, and the variables it needs
├── bundles/
│   ├── my-folder.json        # { name, title, description, skills, rules, servers }
│   └── my-folder/CLAUDE.md   # instructions the folder ships with
└── sources.json              # where each imported skill came from
```

A skill directory here is byte-for-byte what gets installed into a project, so you can copy
one into `~/.claude/skills/` by hand. `circe-server import -in ./my-library` reads the same
layout back, and it is exactly what [syncing](#sync-with-github) commits. Access tokens and
share links are never written to either.

**Coming from an older build?** Two upgrades happen on first boot, both silent and neither
destructive. Folders used to come in three parallel kinds (bundles for skills, rulesets for
rules, mcpsets for servers) and are now one folder, so `data/rulesets` and `data/mcpsets`
are folded into `data/bundles`. Then the whole directory tree, which is how every earlier
release stored things, is read into the database. **The tree itself is left exactly where it
is**, so nothing is lost and an older binary still finds everything it expects.

Frontmatter round-trips losslessly. Keys the app doesn't model: `argument-hint`,
`user-invokable`, anything else you've added: are preserved rather than dropped, and
`allowed-tools` keeps whichever form it was authored in. The parser is tested against a
library of 116 real-world skills.

## Sync with GitHub

Point the registry at a repository once, on **Settings**, and then it is two buttons.

**Push** writes everything out in the layout above and commits it. The commit contains
exactly what the registry holds, so deleting a skill here deletes it there. Anything else
in the repository, a README, a LICENSE, a workflow, is carried through untouched, so the
target can be a repository you also use for other things. Pushing twice with nothing
changed makes no commit at all.

**Pull** reads the repository back in, replacing what is here. That is the direction that
loses work, so it never happens on its own and always asks first.

You need a GitHub token with **Contents: read and write** on the repository, set on the
same page. Private repositories are fine, and usually the right answer. Commits go through
GitHub's API rather than a `git` binary, because the deployed image is `scratch` and has
none.

Two things are never committed: access tokens, because a token in a repository is a token
that has leaked, and share codes, because a share code is a bearer capability that works
without your password.

## HTTP API

Everything the CLI does is available under `/api/v1`:

```
GET  /api/v1/skills?q=&tag=            list and search
GET  /api/v1/skills/{name}             one skill, with body and folder membership
POST /api/v1/skills                    create
PUT  /api/v1/skills/{name}             update
GET  /api/v1/rules?q=                  list and search rules
GET  /api/v1/rules/{name}              one rule, with its body and targets
GET  /api/v1/mcps?q=                   list and search MCP servers
GET  /api/v1/mcps/{name}               one MCP server
GET  /api/v1/bundles                   list folders
GET  /api/v1/bundles/{name}            manifest: skills, rules and servers expanded
GET  /api/v1/bundles/{name}/archive.tar.gz
GET  /api/v1/download?skill=a&skill=b  tar.gz of specific skills
GET  /api/v1/health                    unauthenticated, for health checks
```

PocketBase's own REST API is mounted alongside it at `/api/collections/…`, and its
dashboard at `/_/`. Both sit behind `CIRCE_PASSWORD`: a request without it is turned away
before PocketBase sees it, so the dashboard's sign-in page is not reachable by a stranger
either. Past that door its superuser account applies as a second lock, and the registry's
collections have no public read rule. Set `CIRCE_NO_DASHBOARD=1` to remove both entirely.

## Develop

```bash
make dev      # server with live template reload
make watch    # rebuild CSS on change (run alongside make dev)
make test     # go test ./... -race
make e2e      # drive the real site in a browser (needs Playwright)
make lint     # go vet + gofmt check
make export   # write the registry out as plain files
```

`make e2e` builds the server, starts it on a scratch data directory and walks Chromium
through the flows a person actually performs. It needs `pip install playwright &&
playwright install chromium`, and nothing else in the project does: there is still no Node
or Python in the build.

Contributions are welcome: issues and pull requests both. If you're changing the UI,
`make dev` reloads templates on every request so you don't need to restart.

## Security notes

The server binds to loopback by default and rejects cross-origin mutations, because a page
in your browser can otherwise reach a local port. Companion-file paths are validated against
traversal, archive extraction rejects Zip-Slip entries, and markdown is rendered with raw
HTML escaped so an imported third-party skill can't inject script into your registry.

The PocketBase dashboard at `/_/` reads the same database the registry does, so it is
behind the same password rather than beside it: without `CIRCE_PASSWORD` a request never
reaches PocketBase at all, and its own sign-in page cannot be probed. Its superuser account
is then a second lock on a door strangers cannot get to, there is no such account until you
make one, and the link that makes the first one goes to the server log rather than to the
internet. `CIRCE_NO_DASHBOARD=1` removes the door entirely.

PocketBase is pinned to an exact version, and a test fails if that moves. It is pre-1.0 and
has changed its API across minor releases, so upgrading is a decision with a checklist
rather than something a dependency bot should do on your behalf.

Found something? Open an issue.

## Documentation

- [Installing the CLI](docs/install.md)
- [Self-hosting a registry](docs/self-hosting.md)
- [CLI reference](docs/cli.md)
- [HTTP API](docs/api.md)

## The name and the mark

Circe is the enchantress in the Odyssey who transforms what passes through her
hands, which is roughly what a registry does to a project.

The mark is a detail from **George Romney's _Lady Hamilton as Circe_, c. 1782**,
in the [Tate](https://www.tate.org.uk/) collection. Romney died in 1802, so the
painting is in the public domain; the file here comes from
[Wikimedia Commons](https://commons.wikimedia.org/wiki/File:George_Romney_-_Lady_Hamilton_as_Circe.jpg).

An oil portrait turns to mud at 16px, so a geometric fallback mark covers the
browser tab at that one size: a ring of four arcs with the fifth part taken out
and set at the centre, which reads as the name, as a registry of parts, and as
one thing transformed. It lives at [`web/static/img/ring.svg`](web/static/img/ring.svg).

## Credits

The interface design is heavily inspired by **[animations.dev](https://animations.dev/) by
[Emil Kowalski](https://emilkowal.ski/)**: the warm neutral palette, the way surfaces
recede instead of lifting, the restraint with the amber accent, and the idea that hierarchy
should come from weight and brightness rather than size. If you build interfaces, his
course is worth your time.

The skill format is Anthropic's [Agent Skills](https://code.claude.com/docs/en/skills).

## License

MIT: see [LICENSE](LICENSE).
