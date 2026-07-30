# Self-hosting a registry

The server is a single Go binary. It stores everything as plain files and needs no
database.

## Railway

Click the deploy button in the [README](../README.md), then:

1. Set **`SKILLBUILDER_PASSWORD`**. Without it, anyone who finds the URL can edit and
   delete your skills.
2. Attach a **volume mounted at `/data`**, or you lose everything on redeploy.

Optionally set `SKILLBUILDER_SEED_REPO` to populate a fresh registry from a GitHub repo
on first boot.

Costs are small: a ~15 MB image, ~20 MB of RAM at idle, effectively no CPU, and a volume
measured in megabytes. Turn on **App Sleeping** and an idle registry costs close to
nothing — a Go binary cold-starts in about 100 ms.

## Docker

```bash
docker run -d -p 8080:8080 \
  -v skillbuilder-data:/data \
  -e SKILLBUILDER_PASSWORD=pick-something-good \
  ghcr.io/sidharthpunathil/skillbuilder:latest
```

## Anywhere else

The binary is self-contained — templates, stylesheet and fonts are embedded.

```bash
SKILLBUILDER_PASSWORD=… SKILLBUILDER_DATA=/var/lib/skillbuilder ./skillbuilder-server
```

Put it behind a reverse proxy for TLS and set `SKILLBUILDER_BASE_URL` to the public
origin so install commands and share links render correctly.

## Environment

| Variable | Default | Purpose |
| --- | --- | --- |
| `PORT` | `8080` | Listen port. Most hosts inject this. Binds `0.0.0.0` when set. |
| `SKILLBUILDER_ADDR` | `127.0.0.1:8080` | Full listen address. |
| `SKILLBUILDER_DATA` | `./data` | Storage directory. |
| `SKILLBUILDER_PASSWORD` | — | Enables authentication. |
| `SKILLBUILDER_USER` | any | Optional basic-auth username. |
| `SKILLBUILDER_BASE_URL` | — | Public origin, behind a proxy. |
| `SKILLBUILDER_SEED_REPO` | — | Import from this repo when the registry is empty. |
| `SKILLBUILDER_SEED_FOLDER` | — | Folder to put seeded skills in. |
| `GITHUB_TOKEN` | — | Raises GitHub's 60/hr limit and reaches private repos. |

## Backups

`data/` is the whole thing:

```
data/
├── skills/<name>/SKILL.md
├── bundles/<name>.json
├── bundles/<name>/CLAUDE.md
├── sources.json      # provenance of imported skills
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
  and `/s/…` plus `/api/v1/share/…` — which serve *only* resources you explicitly shared.
- Cross-origin mutations are rejected, bundled-file paths are checked against traversal
  and symlink escapes, archive extraction rejects Zip-Slip entries, and markdown renders
  with raw HTML escaped so an imported third-party skill can't inject script.
