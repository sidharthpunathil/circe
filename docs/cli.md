# CLI reference

`skillbuilder` installs skills from a registry into a project, and publishes local skills
back up to it.

## Installing skills

### `skillbuilder <folder>`

Install a folder's skills into the current project. This is the shorthand you'll use most.

```bash
skillbuilder frontend-kit        # pick which of its skills to install
skillbuilder frontend-kit -y     # install all of them
```

`skillbuilder init <folder>` is the same thing spelled out.

| Flag | Purpose |
| --- | --- |
| `-y`, `--yes` | Install everything without prompting. Required in CI, where there's no terminal to pick with. |
| `-d`, `--dir` | Install location. Defaults to `.claude/skills`, and is remembered afterwards. |
| `--force` | Replace skills that already exist with different content. |
| `--dry-run` | Report what would change without writing. |
| `--context` | How to apply the folder's `CLAUDE.md`: `merge` (default), `skip`, `overwrite`. |

### `skillbuilder add <skill>…`

Install individual skills. With no arguments, opens a picker over the whole registry.

### `skillbuilder import <code>`

Install from a share code someone gave you. Needs no configuration at all — the code
carries the origin registry.

## Keeping skills current

### `skillbuilder list`

Show what's installed and whether it has drifted from the registry: `modified` if you've
edited it, `missing` if the directory is gone. `--remote` lists the registry instead.

### `skillbuilder pull`

Re-download everything in `skillbuilder.json`.

**Local edits are held back, not clobbered.** A skill you've modified is reported as
skipped; pass `--force` to overwrite it.

### `skillbuilder remove <skill>…`

Delete skills from disk and drop them from the lockfile.

## Publishing

### `skillbuilder push <dir>`

Publish local skills to the registry.

```bash
skillbuilder push ./my-skill          # one skill
skillbuilder push ~/.claude/skills    # every skill in there
```

If the directory has a `SKILL.md` it's treated as one skill; otherwise every subdirectory
that has one is published. Companion files come along. Existing skills are left alone
unless you pass `--force`.

`.git`, `node_modules`, `__pycache__`, `.venv` and `evals` are excluded — a skill that
vendors a virtualenv would otherwise be hundreds of megabytes.

| Flag | Purpose |
| --- | --- |
| `--tag` | Tag to publish with (repeatable). |
| `--force` | Replace skills that already exist. |
| `--dry-run` | Show what would be published. |

## Configuration

### `skillbuilder login [url]`

Connect to a registry. Prompts for the passcode, verifies it, and only then saves.

### `skillbuilder logout`

Forget the saved token.

### `skillbuilder config`

Show the resolved registry, config path, lockfile path and install directory.

## Where settings come from

Registry, in order of precedence:

1. `--registry` flag
2. `$SKILLBUILDER_REGISTRY`
3. `~/.skillbuilder/config.json`
4. `http://localhost:8080`

Token: `$SKILLBUILDER_TOKEN`, then the config file. There is deliberately no `--token`
flag on ordinary commands, so a token never lands in your shell history.

## The lockfile

Installing writes `skillbuilder.json` in the project root:

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

Commit it. Anyone who clones the project gets the same skills with `skillbuilder pull`.
The checksum is what makes drift detection work, and `dir` is why you only pass `-d`
once.
