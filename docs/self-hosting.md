# Self-hosting a registry

The server is a single Go binary. It stores everything in an embedded
[PocketBase](https://pocketbase.io) database inside its data directory, so there is still
no service to run beside it, and it can write the whole registry back out as plain files
whenever you want one.

## Railway

Click the deploy button in the [README](../README.md), then:

1. Set **`CIRCE_PASSWORD`**. Without it, anyone who finds the URL can edit and
   delete your skills.
2. Attach a **volume mounted at `/data`**, or you lose everything on redeploy.

Optionally set `CIRCE_SEED_REPO` to populate a fresh registry from a GitHub repo
on first boot.

Costs are small: an image of about 30 MB, around 50 MB of RAM at idle, effectively no CPU,
and a volume measured in megabytes. Turn on **App Sleeping** and an idle registry costs
close to nothing: a Go binary cold-starts in about 100 ms.

## Docker

```bash
docker run -d -p 8080:8080 \
  -v circe-data:/data \
  -e CIRCE_PASSWORD=pick-something-good \
  ghcr.io/sidharthpunathil/circe:latest
```

## Anywhere else

The binary is self-contained: templates, stylesheet and fonts are embedded.

```bash
CIRCE_PASSWORD=… CIRCE_DATA=/var/lib/circe ./circe-server
```

Put it behind a reverse proxy for TLS and set `CIRCE_BASE_URL` to the public
origin so install commands and share links render correctly.

## Environment

| Variable | Default | Purpose |
| --- | --- | --- |
| `PORT` | `8080` | Listen port. Most hosts inject this. Binds `0.0.0.0` when set. |
| `CIRCE_ADDR` | `127.0.0.1:8080` | Full listen address. |
| `CIRCE_DATA` | `./data` | Storage directory. |
| `CIRCE_PASSWORD` |: | Enables authentication. |
| `CIRCE_USER` | any | Optional basic-auth username. |
| `CIRCE_BASE_URL` |: | Public origin, behind a proxy. |
| `CIRCE_SEED_REPO` |: | Import from this repo when the registry is empty. |
| `CIRCE_SEED_FOLDER` |: | Folder to put seeded skills in. |
| `CIRCE_NO_DASHBOARD` |: | Set to `1` to remove the PocketBase dashboard at `/_/` and its API. |
| `CIRCE_SITE_NAME` |: | Site name used in titles and Open Graph tags. |
| `CIRCE_SITE_DESCRIPTION` |: | Default meta description. |
| `CIRCE_TWITTER_SITE` |: | `@handle` for Twitter card attribution. |
| `GITHUB_TOKEN` |: | Raises GitHub's 60/hr limit, reaches private repos, and is what sync commits with. |
| `GITLAB_TOKEN` |: | Same, for GitLab. |
| `GITEA_TOKEN` |: | Same, for Gitea/Forgejo/Codeberg. |
| `BITBUCKET_TOKEN` |: | Same, for Bitbucket. |

## Getting skills in

Four routes on the Import page, plus two that do not involve it: pulling from a repository
you have [connected for sync](sync.md), and `circe-server import -in DIR` from a directory
of files.

| Source | Works when |
| --- | --- |
| Local directory | The registry runs on the same machine as the skills. |
| Repository | Always: GitHub, GitLab, Bitbucket, Codeberg, Gitea, Forgejo. |
| From a file | Always: you paste or upload the markdown. |
| From a code | Always: someone shared a folder or skill with you. |

For a hosted registry, `circe push ~/.claude/skills` from your own machine is
usually the fastest way to move an existing library up.

## Backups

`data/` is the whole thing:

```
data/
├── data.db          # skills, rules, servers, folders, tokens, share codes
├── auxiliary.db     # request logs
└── storage/         # companion files that ship with a skill
```

Copy it, rsync it, or snapshot the volume. Restoring means putting the directory back. Stop
the server first, or copy the `-wal` and `-shm` files alongside it: this is SQLite, and half
a running database is not a backup.

If you would rather have something you can read, `circe-server export` writes every skill,
rule, server and folder out as ordinary markdown and JSON:

```bash
circe-server export -data /var/lib/circe -out ./backup
```

```
backup/
├── skills/<name>/SKILL.md
├── rules/<name>.md            # project conventions
├── mcps/<name>.json           # one MCP server
├── bundles/<name>.json        # a folder: its skills, rules and servers
├── bundles/<name>/CLAUDE.md   # instructions the folder ships with
└── sources.json               # provenance of imported skills
```

`circe-server import -in ./backup` reads it back, and this is the same layout the GitHub
sync commits, so connecting a private repository on the Settings page gives you an
off-machine backup with history for free.

Access tokens and share codes are deliberately **not** in an export. A token in a backup you
share is a token that has leaked, and a share code works without your password. They live in
`data.db` only, which is the other reason to keep that file to yourself.

**Restoring an older backup.** A directory tree taken from a previous release is still
understood: put it in place as `data/` and the first boot reads it into the database,
leaving the tree itself untouched. `rulesets/` and `mcpsets/` from even older builds are
folded into `bundles/` on the way through.

## Security

- **Set a password.** The server binds to loopback by default and warns loudly at startup
  if you bind elsewhere without one.
- Browsers sign in at `/login` and get a signed cookie. The cookie is HMAC'd with the
  password, so changing the password signs everyone out.
- The CLI authenticates with `Authorization: Bearer <password>`.
- **Only these paths are public:** `/api/v1/health`, `/login`, static assets, OG images,
  and `/s/…` plus `/api/v1/share/…`: which serve *only* resources you explicitly shared.
  PocketBase's `/_/` and `/api/collections/…` are behind `CIRCE_PASSWORD` like everything
  else, so a stranger cannot reach the dashboard's sign-in page at all.
- Cross-origin mutations are rejected, companion-file paths are checked against traversal,
  archive extraction rejects Zip-Slip entries, and markdown renders with raw HTML escaped
  so an imported third-party skill can't inject script.
- **The dashboard is behind the same password.** PocketBase serves its own admin UI at
  `/_/` and its REST API at `/api/collections/…`. Both read the same database the registry
  does, so both are gated on `CIRCE_PASSWORD` first: without it the request is refused
  before PocketBase sees it. Past that, PocketBase's superuser account applies as a second
  lock, no such account exists until you create one, and the link that creates the first
  one is printed to the server log. Set `CIRCE_NO_DASHBOARD=1` to remove it altogether.
- **PocketBase is pinned.** It is pre-1.0 and has changed its API across minor releases, so
  the version is fixed in `go.mod` and a test fails if it moves. Upgrade deliberately: read
  its changelog, then run `make test` and `make e2e`.
- Syncing needs a GitHub token with **Contents: read and write**. Give it access to the one
  repository you sync with rather than to everything.
