#!/bin/sh
# Muster installer: curl -fsSL https://raw.githubusercontent.com/maauri/muster/main/install.sh | sh
#
# Downloads the release binary for this OS/arch, verifies its SHA-256
# against the release's SHA256SUMS, and installs it without sudo when
# possible. Environment overrides:
#   MUSTER_VERSION   tag to install (default: latest)
#   MUSTER_BASE_URL  release download base (default: GitHub Releases)
#   MUSTER_BIN_DIR   install directory (default: /usr/local/bin if writable, else ~/.local/bin)
set -eu

REPO="maauri/muster"
VERSION="${MUSTER_VERSION:-latest}"
if [ -n "${MUSTER_BASE_URL:-}" ]; then
	BASE="$MUSTER_BASE_URL"
elif [ "$VERSION" = latest ]; then
	BASE="https://github.com/$REPO/releases/latest/download"
else
	BASE="https://github.com/$REPO/releases/download/$VERSION"
fi

say() { printf '%s\n' "$*" >&2; }
die() { say "muster install: $*"; exit 1; }

case "$(uname -s)" in
	Darwin) os=darwin ;;
	Linux) os=linux ;;
	*) die "unsupported OS $(uname -s); download manually from https://github.com/$REPO/releases" ;;
esac
case "$(uname -m)" in
	x86_64 | amd64) arch=amd64 ;;
	arm64 | aarch64) arch=arm64 ;;
	*) die "unsupported CPU $(uname -m)" ;;
esac
asset="muster-$os-$arch"

if command -v curl >/dev/null 2>&1; then
	fetch() { curl -fsSL "$1" -o "$2"; }
elif command -v wget >/dev/null 2>&1; then
	fetch() { wget -q "$1" -O "$2"; }
else
	die "need curl or wget"
fi
if command -v sha256sum >/dev/null 2>&1; then
	sha() { sha256sum "$1" | cut -d' ' -f1; }
else
	sha() { shasum -a 256 "$1" | cut -d' ' -f1; }
fi

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT INT TERM

say "Downloading $asset ($VERSION)..."
fetch "$BASE/$asset" "$tmp/muster" || die "download failed: $BASE/$asset"
fetch "$BASE/SHA256SUMS" "$tmp/SHA256SUMS" || die "checksum download failed"

want="$(grep " $asset\$" "$tmp/SHA256SUMS" | cut -d' ' -f1)"
[ -n "$want" ] || die "no checksum for $asset in SHA256SUMS"
got="$(sha "$tmp/muster")"
[ "$want" = "$got" ] || die "checksum mismatch for $asset (want $want, got $got); not installing"
chmod 755 "$tmp/muster"

if [ -n "${MUSTER_BIN_DIR:-}" ]; then
	dir="$MUSTER_BIN_DIR"
elif [ -w /usr/local/bin ]; then
	dir=/usr/local/bin
else
	dir="$HOME/.local/bin"
fi
mkdir -p "$dir"
mv "$tmp/muster" "$dir/muster"
say "Installed $("$dir/muster" version) to $dir/muster"

case ":$PATH:" in
	*":$dir:"*) ;;
	*) say "" ; say "Add $dir to your PATH:  export PATH=\"$dir:\$PATH\"" ;;
esac

say ""
say "Next:"
say "  First device:   muster service install     (or just: muster agent)"
say "  Other devices:  muster join && muster service install"
say "  Problems?       muster doctor"
