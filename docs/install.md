# Installing the CLI

The `circe` CLI installs skills from a registry into a project. It talks to a
registry over HTTP, so it works against a local one or a hosted one.

## Quick install

```bash
curl -fsSL https://raw.githubusercontent.com/sidharthpunathil/circe/main/install.sh | sh
```

Detects your OS and CPU, downloads the matching release binary, verifies its checksum,
and puts it somewhere on your `PATH`.

Options:

```bash
# a specific version
VERSION=v0.1.0 curl -fsSL .../install.sh | sh

# a directory you choose (no sudo)
INSTALL_DIR=~/.local/bin curl -fsSL .../install.sh | sh
```

Supported: macOS and Linux, on `amd64` and `arm64`. Windows users can build from source
or use WSL.

## With Go

```bash
go install github.com/sidharthpunathil/circe/cmd/circe@latest
```

Puts the binary in `$(go env GOPATH)/bin`.

## From source

```bash
git clone https://github.com/sidharthpunathil/circe
cd circe
make build          # ./bin/circe and ./bin/circe-server
```

`make build` fetches the Tailwind standalone binary and compiles both binaries. No Node
is needed at build or run time.

## Verify

```bash
circe --version
```

## Connect to a registry

```bash
circe login https://your-registry.example.com
```

It prompts for the passcode, checks it against the registry, and only saves once it
works: so a typo fails immediately instead of surfacing later. Settings live in
`~/.circe/config.json`; `circe logout` forgets the token.

Without a registry of your own you can still use a share code someone sent you:

```bash
circe import sb_…
```

## Uninstall

```bash
rm "$(command -v circe)"
rm -rf ~/.circe
```

Skills already installed into a project live in that project's `.claude/skills/` and are
unaffected.
