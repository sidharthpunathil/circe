# HTTP API

Everything under `/api/v1`. Responses are JSON; errors are `{"error": "...", "message": "..."}`.

## Authentication

When `CIRCE_PASSWORD` is set, send it as a bearer token:

```bash
curl -H "Authorization: Bearer $TOKEN" https://your-registry/api/v1/skills
```

HTTP basic auth works too. `/api/v1/health` and the share endpoints never require
credentials.

## Skills

```
GET    /api/v1/skills?q=&tag=      list and search
GET    /api/v1/skills/{name}       one skill, with body and folder membership
POST   /api/v1/skills              create
PUT    /api/v1/skills/{name}       replace
```

Create and update take:

```json
{
  "name": "my-skill",
  "description": "What it does, and when the agent should use it.",
  "body": "# My Skill\n\n…",
  "tags": ["design"],
  "files": { "references/api.md": "# Reference\n" }
}
```

`name` must be lowercase letters, digits and single hyphens, 64 characters or fewer.
`description` is required, 1024 characters or fewer, and must not contain `<` or `>`.
The whole request is capped at 16 MiB.

## Rules

```
GET    /api/v1/rules?q=            list and search
GET    /api/v1/rules/{name}        one rule
```

A rule carries the text that lands in a project, and the files it belongs in:

```json
{
  "name": "house-style",
  "description": "How we write",
  "body": "Prefer plain words.\n",
  "targets": ["CLAUDE.md", "AGENTS.md"],
  "updatedAt": "2026-07-30T04:12:09Z"
}
```

`targets` may be omitted, which means `CLAUDE.md`.

## MCP servers

```
GET    /api/v1/mcps?q=             list and search
GET    /api/v1/mcps/{name}         one server
```

A server carries its transport and command, and the *names* of the environment variables
it needs, never their values.

## Folders

A folder holds any mix of skills, rules and MCP servers.

```
GET  /api/v1/bundles                        list
GET  /api/v1/bundles/{name}                 manifest, everything expanded
GET  /api/v1/bundles/{name}/archive.tar.gz  every skill in the folder
```

The listing gives each folder's `skills`, `rules` and `servers` as names. The manifest
expands all three, so one request is enough to install a folder:

```json
{
  "bundle": { "name": "frontend-kit", "title": "Frontend kit", "skills": ["design-taste"] },
  "skills":  [ { "name": "design-taste", "description": "…" } ],
  "rules":   [ { "name": "house-style", "body": "…", "targets": ["CLAUDE.md"] } ],
  "servers": [ { "name": "github", "transport": "stdio", "command": "npx" } ],
  "context": { "CLAUDE.md": "…" },
  "registry": "https://your-registry"
}
```

`context` is the folder's own `CLAUDE.md` / `AGENTS.md`. `registry` is where the client
should fetch archives from, which matters behind a proxy. `rendered` and `mcp` are also
included when the folder has rules or servers: the rule text keyed by the file it goes
into, and the `.mcp.json` the servers add up to, for clients that would rather not
assemble either themselves.

Rules and MCP servers used to have folder types of their own, served under `/rulesets` and
`/mcpsets`. Both are gone. A registry upgraded from that build folds their contents into
`/bundles` on first boot, keeping the names.

## Downloads

```
GET /api/v1/download?skill=a&skill=b   gzipped tar of specific skills
```

One top-level directory per skill.

## Shares

Public: no credentials, and only for resources explicitly shared.

```
GET /api/v1/share/{id}                   what is shared
GET /api/v1/share/{id}/archive.tar.gz    its content
```

## Health

```
GET /api/v1/health
```

```json
{ "status": "ok", "version": "v1", "skills": 42, "bundles": 3 }
```

Unauthenticated, so platform health checks work.

## Claude Code marketplace

```
GET /.claude-plugin/marketplace.json
```

Each folder appears as a plugin entry, so `/plugin marketplace add <your-url>` works with
stock Claude Code.

## PocketBase's API

The registry runs as a PocketBase application, so PocketBase's own REST API is mounted
alongside this one at `/api/collections/…`, with its dashboard at `/_/`.

It is not a second way into your registry. Both are gated on `CIRCE_PASSWORD` first, so an
unauthenticated request is refused before PocketBase sees it, and the dashboard's own
sign-in page cannot be probed by someone who does not already have the password. Past that,
none of the collections carry a public list or view rule, so every request there also needs
a PocketBase superuser, which is an account that does not exist until you create one. Set
`CIRCE_NO_DASHBOARD=1` to remove both entirely.

Circe's own `/api/v1/health` stays public either way, so platform health checks keep
working.

Everything under `/api/v1` above is Circe's own, and is what the CLI uses. It is unaffected
by any of this.
