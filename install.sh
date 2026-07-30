#!/bin/sh
# Install the skillbuilder CLI.
#
#   curl -fsSL https://raw.githubusercontent.com/sidharthpunathil/skillbuilder/main/install.sh | sh
#
# Downloads the release binary for this machine and puts it on your PATH.
# Override with: VERSION=v0.2.0 INSTALL_DIR=~/bin sh install.sh
set -eu

REPO="sidharthpunathil/skillbuilder"
BINARY="skillbuilder"
VERSION="${VERSION:-latest}"

# Colours only when writing to a terminal, so piped output stays clean.
if [ -t 1 ]; then
  BOLD=$(printf '\033[1m'); DIM=$(printf '\033[2m'); RED=$(printf '\033[31m')
  GREEN=$(printf '\033[32m'); RESET=$(printf '\033[0m')
else
  BOLD=''; DIM=''; RED=''; GREEN=''; RESET=''
fi

say()  { printf '%s\n' "$*"; }
dim()  { printf '%s%s%s\n' "$DIM" "$*" "$RESET"; }
ok()   { printf '  %s✓%s %s\n' "$GREEN" "$RESET" "$*"; }
die()  { printf '%serror:%s %s\n' "$RED" "$RESET" "$*" >&2; exit 1; }

need() { command -v "$1" >/dev/null 2>&1 || die "$1 is required but not installed"; }

detect_platform() {
  os=$(uname -s | tr '[:upper:]' '[:lower:]')
  arch=$(uname -m)
  case "$os" in
    linux|darwin) ;;
    *) die "unsupported operating system: $os (build from source: https://github.com/$REPO)" ;;
  esac
  case "$arch" in
    x86_64|amd64) arch=amd64 ;;
    aarch64|arm64) arch=arm64 ;;
    *) die "unsupported architecture: $arch (build from source: https://github.com/$REPO)" ;;
  esac
  printf '%s_%s' "$os" "$arch"
}

# Prefer a directory already on PATH that we can write to, so the binary is
# usable immediately without shell configuration.
choose_dir() {
  if [ -n "${INSTALL_DIR:-}" ]; then
    printf '%s' "$INSTALL_DIR"; return
  fi
  for d in /usr/local/bin "$HOME/.local/bin" "$HOME/bin"; do
    case ":$PATH:" in *":$d:"*) ;; *) continue ;; esac
    if [ -w "$d" ] 2>/dev/null; then printf '%s' "$d"; return; fi
    if [ "$d" = /usr/local/bin ] && command -v sudo >/dev/null 2>&1; then
      printf '%s' "$d"; return
    fi
  done
  printf '%s' "$HOME/.local/bin"
}

resolve_version() {
  if [ "$VERSION" != latest ]; then printf '%s' "$VERSION"; return; fi
  api="https://api.github.com/repos/$REPO/releases/latest"
  tag=$(curl -fsSL "$api" 2>/dev/null | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1)
  [ -n "$tag" ] || die "could not find a published release. Set VERSION=vX.Y.Z, or build from source."
  printf '%s' "$tag"
}

main() {
  need curl
  need tar
  need uname

  platform=$(detect_platform)
  version=$(resolve_version)
  dir=$(choose_dir)

  say ""
  say "${BOLD}Installing $BINARY $version${RESET}"
  dim "  platform    $platform"
  dim "  destination $dir/$BINARY"
  say ""

  tarball="${BINARY}_${version#v}_${platform}.tar.gz"
  url="https://github.com/$REPO/releases/download/$version/$tarball"

  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT INT TERM

  curl -fsSL "$url" -o "$tmp/$tarball" \
    || die "download failed: $url
  The release may not include a build for $platform."
  ok "downloaded"

  # Verify the checksum when the release publishes one. Not fatal if absent,
  # so an older release without checksums still installs.
  if curl -fsSL "https://github.com/$REPO/releases/download/$version/checksums.txt" -o "$tmp/checksums.txt" 2>/dev/null; then
    if command -v sha256sum >/dev/null 2>&1; then
      ( cd "$tmp" && grep " $tarball\$" checksums.txt | sha256sum -c - >/dev/null 2>&1 ) \
        && ok "checksum verified" || die "checksum mismatch for $tarball — refusing to install"
    elif command -v shasum >/dev/null 2>&1; then
      ( cd "$tmp" && grep " $tarball\$" checksums.txt | shasum -a 256 -c - >/dev/null 2>&1 ) \
        && ok "checksum verified" || die "checksum mismatch for $tarball — refusing to install"
    fi
  fi

  tar -xzf "$tmp/$tarball" -C "$tmp" || die "could not extract $tarball"
  [ -f "$tmp/$BINARY" ] || die "$BINARY not found inside the archive"
  chmod +x "$tmp/$BINARY"

  mkdir -p "$dir" 2>/dev/null || true
  if [ -w "$dir" ]; then
    mv "$tmp/$BINARY" "$dir/$BINARY"
  else
    say "  ${DIM}$dir needs elevated permissions${RESET}"
    sudo mv "$tmp/$BINARY" "$dir/$BINARY" || die "could not write to $dir. Retry with INSTALL_DIR=\$HOME/.local/bin"
  fi
  ok "installed to $dir/$BINARY"

  say ""
  if command -v "$BINARY" >/dev/null 2>&1 && [ "$(command -v "$BINARY")" = "$dir/$BINARY" ]; then
    say "$("$dir/$BINARY" --version 2>/dev/null || echo "$BINARY $version")"
    say ""
    say "Next:  ${BOLD}$BINARY login https://your-registry.example.com${RESET}"
  else
    say "${BOLD}$dir is not on your PATH.${RESET}"
    say "Add it:"
    say "  ${DIM}echo 'export PATH=\"$dir:\$PATH\"' >> ~/.zshrc && exec \$SHELL${RESET}"
  fi
  say ""
}

main "$@"
