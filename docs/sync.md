# Sync, export and getting your content back

Circe keeps skills, rules, servers and folders in an embedded database. That is a storage
decision, not a lock: every record holds the exact document it came from, so the registry
can be written back out as ordinary markdown and JSON at any moment, and that is what both
the export command and the GitHub sync do.

There are two ways out, and they produce the same thing.

## The layout

Whatever writes it, the result is the directory layout Circe has always used:

```
skills/
└── my-skill/
    ├── SKILL.md          # YAML frontmatter + markdown body
    └── references/…      # whatever else the skill ships
rules/
└── house-style.md        # frontmatter + the text that lands in CLAUDE.md
mcps/
└── github.json           # one MCP server, and the variables it needs
bundles/
├── my-folder.json        # { name, title, description, skills, rules, servers }
└── my-folder/CLAUDE.md   # instructions the folder ships with
sources.json              # where each imported skill came from
```

A skill directory here is byte-for-byte what gets installed into a project, so copying one
into `~/.claude/skills/` by hand works.

**Two things are never written.** Access tokens, because a token in a repository is a token
that has leaked. Share codes, because a share code is a bearer capability: whoever holds it
can read that folder without your password. Both stay in the database.

## Export

```bash
circe-server export -out ./my-library
circe-server import -in ./my-library -force
```

`export` reads the data directory (`-data`, or `$CIRCE_DATA`, default `./data`) and writes
the tree above. `import` reads it back; without `-force` it skips anything already there and
tells you how many it skipped.

This is also the upgrade and downgrade path. A data directory from a release before the
database was introduced is a tree in exactly this layout, and the first boot of this version
reads it in, leaving the tree itself untouched.

## Sync with GitHub

Connect a repository once, on **Settings**, and after that it is two buttons.

### Setting it up

1. Create a repository. Private is fine, and usually right.
2. Create a GitHub token with **Contents: read and write** on it. A fine-grained token
   scoped to that one repository is the right shape.
3. Paste the token into **Settings**, under GitHub.
4. Paste the repository into the sync box. `owner/name`, a full URL or an SSH remote all
   work. Optionally set a branch, and a subdirectory if the repository holds other things.

### Push

Push writes the whole registry out in the layout above and commits it.

- The commit is **the complete state**, so a skill deleted here is deleted there. It is not
  a merge with whatever was in the repository before.
- Anything the registry does not own is carried through untouched. A README, a LICENSE, a
  workflow, another project living in the same repository: all left exactly as they were.
  Only `skills/`, `rules/`, `mcps/`, `bundles/` and `sources.json` are replaced.
- Pushing when nothing has changed makes **no commit at all**, so the history stays a record
  of real changes rather than of button presses.
- Nothing is ever force-pushed. The commit's parent is the branch head as it was read at the
  start, so if someone else pushed in between, GitHub refuses the update and you are told to
  pull first rather than having their work overwritten.

The default commit message describes what the registry holds, so the log reads as a series
of states. Write your own in the box next to the button when a change deserves an
explanation.

### Pull

Pull reads the repository back in, replacing what is here. It is the direction that loses
work, so it never happens on a timer and always asks first. Anything you have edited in the
browser and not yet pushed is gone.

### Why it works this way

Commits go through GitHub's API rather than a `git` binary. The deployed image is `scratch`:
no git, no shell, nowhere to put a working copy. The same reasoning is why importing a
repository downloads a tarball instead of cloning one.

Blob ids are computed locally, so a file that has not changed is referenced rather than
uploaded again. A push that changes nothing is three API calls, not one per file.

Sync writes to GitHub only. Importing works with GitLab, Bitbucket, Codeberg and any
Gitea or Forgejo instance, because those all expose a plain tarball endpoint; writing
commits needs each host's own API, and only GitHub's is implemented.

## Which one should I use

Use sync if you want history, an off-machine copy, and the ability to review a change to
your own library as a diff. Use export if you want a directory right now, to feed to
something else, or to move to another machine without involving a network.

Neither is a substitute for a backup of the data directory itself, which is the only thing
that also carries your tokens and share codes. See
[self-hosting.md](self-hosting.md#backups).
