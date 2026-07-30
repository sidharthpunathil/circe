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

## Folders

```
GET  /api/v1/bundles                        list
GET  /api/v1/bundles/{name}                 manifest with skills expanded
GET  /api/v1/bundles/{name}/archive.tar.gz  every skill in the folder
```

The manifest includes a `context` map of the folder's `CLAUDE.md` / `AGENTS.md`.

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
