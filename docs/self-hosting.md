# Self-hosting a registry

The server is a single Go binary. It stores everything as plain files and needs no
database.

## Railway

Click the deploy button in the [README](../README.md), then:

1. Set **`CIRCE_PASSWORD`**. Without it, anyone who finds the URL can edit and
   delete your skills.
2. Attach a **volume mounted at `/data`**, or you lose everything on redeploy.

Optionally set `CIRCE_SEED_REPO` to populate a fresh registry from a GitHub repo
on first boot.

Costs are small: a ~15 MB image, ~20 MB of RAM at idle, effectively no CPU, and a volume
measured in megabytes. Turn on **App Sleeping** and an idle registry costs close to
nothing: a Go binary cold-starts in about 100 ms.

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
| `GITHUB_TOKEN` |: | Raises GitHub's 60/hr limit and reaches private repos. |
| `GITLAB_TOKEN` |: | Same, for GitLab. |
| `GITEA_TOKEN` |: | Same, for Gitea/Forgejo/Codeberg. |
| `BITBUCKET_TOKEN` |: | Same, for Bitbucket. |

## Getting skills in

Four routes, on the Import page:

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
├── skills/<name>/SKILL.md
├── bundles/<name>.json
├── bundles/<name>/CLAUDE.md
├── rules/<name>.md   # project conventions
├── rulesets/<name>.json
├── sources.json      # provenance of imported skills
├── .credentials.json # access tokens (git-ignored, never back this up)
└── shares.json       # active share codes
```

Copy it, rsync it, or commit it to a private git repo. Restoring means putting the
directory back.

## Security

- **Set a password.** The server binds to loopback by default and warns loudly at startup
  if you bind elsewhere without one.
- Browsers sign in at `/login` and get a signed cookie. The cookie is HMAC'd with the
  password, so changing the password signs everyone out.
- The CLI authenticates with `Authorization: Bearer <password>`.
- **Only these paths are public:** `/api/v1/health`, `/login`, static assets, OG images,
  and `/s/…` plus `/api/v1/share/…`: which serve *only* resources you explicitly shared.
- Cross-origin mutations are rejected, bundled-file paths are checked against traversal
  and symlink escapes, archive extraction rejects Zip-Slip entries, and markdown renders
  with raw HTML escaped so an imported third-party skill can't inject script.
