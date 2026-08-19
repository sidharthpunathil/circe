# CLI reference

`circe` installs a folder from a registry into a project, and publishes local skills
back up to it.

A folder holds any mix of skills, rules and MCP servers. The bare command installs all of
it; the three verbs narrow it to one kind:

```bash
circe frontend-kit          # skills, rules and servers
circe skills frontend-kit   # only the skills
circe rules  frontend-kit   # only the rules
circe mcp    frontend-kit   # only the MCP servers
```

Each kind lands where that kind belongs, and nothing else in the project is touched:

| Kind | Where it lands | How |
| --- | --- | --- |
| Skills | `.claude/skills/<name>/`, or `-d` | One directory per skill, recorded in `circe.json` |
| Rules | `CLAUDE.md`, `AGENTS.md` | Merged into a marked block, per folder |
| MCP servers | `.mcp.json`, or `-o` | Merged into `mcpServers`, leaving existing entries alone |

Rules and MCP servers used to live in folders of their own. A registry upgraded from that
build folds them into ordinary folders on its first boot, so the names you already type
keep working.

## Installing

### `circe <folder>`

Install everything in a folder. This is the command you'll use most.

```bash
circe frontend-kit        # pick as you go
circe frontend-kit -y     # take all of it
```

`circe init <folder>` is the same thing spelled out.

It reports each kind separately and closes with what the whole run did, plus any
environment variables the servers need that your shell does not have:

```
Frontend kit · 3 skills, 2 rules and 1 server
skills
  ✓ design-taste  installed
  3 installed in .claude/skills

rules
  ✓ CLAUDE.md  updated

mcp servers
  ✓ github  added
  1 added in .mcp.json
installed 3 skills, 2 rules and 1 server
set these before the servers will start:
  GITHUB_TOKEN  github
```

A folder holding nothing but skills prints exactly what it always did: no header, no
section labels, no closing summary.

| Flag | Purpose |
| --- | --- |
| `-y`, `--yes` | Take everything without prompting. Required in CI, where there's no terminal to pick with. |
| `-d`, `--dir` | Where the **skills** go. Defaults to `.claude/skills`, and is remembered afterwards. |
| `-o`, `--out` | Which config the **MCP servers** merge into. Defaults to `.mcp.json`, per run. |
| `--force` | Overwrite skills that already exist, and replace servers this project already has. |
| `--dry-run` | Report what would change, across all three kinds, without writing anything. |
| `--context` | How to apply `CLAUDE.md` and `AGENTS.md`: `merge` (default), `skip`, `overwrite`. |

`-d` and `-o` each move one kind, because the three kinds land in three different places
and there is no single directory to point at. Rules always go to `CLAUDE.md` and
`AGENTS.md` in the project root: that is where the agent reads them, so there is nothing
to choose. `-d` is remembered in `circe.json` so `pull` and `list` find the skills without
it; `-o` is not remembered, because `.mcp.json` is where Claude Code looks and pointing
somewhere else is a per-run decision.

### `circe skills <folder>`

Only the skills. Same flags as the bare command, minus `-o`.

With no folder, lists the folders that have skills in them.

If the folder also has rules, this leaves `CLAUDE.md` and `AGENTS.md` alone and says so.
The rules and the folder's own instructions share one marked block, and writing half of a
block would delete the other half on the next run.

### `circe rules <folder>`

Only the rules, merged into the project's `CLAUDE.md` and `AGENTS.md` inside a block
marked with the folder's name. Your own content is untouched, and re-running replaces that
block rather than adding a second one. Each rule chooses which of the two files it belongs
in. The folder's own `CLAUDE.md` goes into the same block, ahead of the rules.

With no folder, lists the folders that have rules in them.

Flags: `--context`, `--dry-run`.

### `circe mcp <folder>`

Only the MCP servers, merged into the project's `.mcp.json`.

Servers the project already has are reported and left exactly as they are, never replaced,
unless you pass `--force`. Top level keys Circe does not model are carried through
untouched, and a run that changes nothing does not rewrite the file. Afterwards it lists
the environment variables those servers reference and your shell does not have, because a
server with an unset variable starts and immediately dies.

With no folder, lists the folders that have MCP servers in them.

Flags: `-y`, `-o`, `--force` (which also takes the whole folder), `--dry-run`.

### `circe add <skill>…`

Install individual skills. With no arguments, opens a picker over the whole registry.

### `circe import <code>`

Install from a share code someone gave you. Needs no configuration at all: the code
carries the origin registry.

## Keeping skills current

### `circe list`

Show what's installed and whether it has drifted from the registry: `modified` if you've
edited it, `missing` if the directory is gone. `--remote` lists the registry instead.

### `circe pull`

Re-download everything in `circe.json`.

**Local edits are held back, not clobbered.** A skill you've modified is reported as
skipped; pass `--force` to overwrite it.

### `circe remove <skill>…`

Delete skills from disk and drop them from the lockfile.

## Publishing

### `circe push <dir>`

Publish local skills to the registry.

```bash
circe push ./my-skill          # one skill
circe push ~/.claude/skills    # every skill in there
```

If the directory has a `SKILL.md` it's treated as one skill; otherwise every subdirectory
that has one is published. Companion files come along. Existing skills are left alone
unless you pass `--force`.

`.git`, `node_modules`, `__pycache__`, `.venv` and `evals` are excluded: a skill that
vendors a virtualenv would otherwise be hundreds of megabytes.

| Flag | Purpose |
| --- | --- |
| `--tag` | Tag to publish with (repeatable). |
| `--force` | Replace skills that already exist. |
| `--dry-run` | Show what would be published. |

## Configuration

### `circe login [url]`

Connect to a registry. Prompts for the passcode, verifies it, and only then saves.

### `circe logout`

Forget the saved token.

### `circe config`

Show the resolved registry, config path, lockfile path and install directory.

## Where settings come from

Registry, in order of precedence:

1. `--registry` flag
2. `$CIRCE_REGISTRY`
3. `~/.circe/config.json`
4. `http://localhost:8080`

Token: `$CIRCE_TOKEN`, then the config file. There is deliberately no `--token`
flag on ordinary commands, so a token never lands in your shell history.

## The lockfile

Installing writes `circe.json` in the project root:

```json
{
  "version": 1,
  "registry": "https://skills.example.com",
  "dir": "vendor/skills",
  "bundles": ["frontend-kit"],
  "skills": {
    "design-taste": {
      "name": "design-taste",
      "bundle": "frontend-kit",
      "checksum": "sha256:…",
      "installedAt": "2026-07-30T04:12:09Z"
    }
  }
}
```

Commit it. Anyone who clones the project gets the same skills with `circe pull`.
The checksum is what makes drift detection work, and `dir` is why you only pass `-d`
once.

Only skills are recorded. Rules and MCP servers are already visible in the project's own
`CLAUDE.md` and `.mcp.json`, which are committed too, so a second copy of them here would
be one more thing to drift.

## The server's own commands

`circe-server` is normally started with no arguments and serves the registry. Two
subcommands sit in front of that, and both operate on a data directory rather than on a
running server:

```bash
circe-server export -data ./data -out ./my-library
circe-server import -data ./data -in ./my-library -force
```

`export` writes every skill, rule, server and folder out as plain markdown and JSON.
`import` reads that same layout back, skipping what already exists unless you pass
`-force`. Access tokens and share codes are never exported. See [sync.md](sync.md).
