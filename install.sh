#!/bin/sh
# Install Jigz from a release binary, verified against the
# release's checksums.txt.
#
# One-liner:
#   curl -sSL https://raw.githubusercontent.com/kinged007/jigz/main/install.sh | sh
#
# Usage:
#   ./install.sh                    install to ~/.local/bin
#   ./install.sh --prefix DIR       install binaries under DIR/bin
#   ./install.sh --version TAG      install a specific release tag
#   ./install.sh --from-source      build from this checkout instead
#
# Requirements: curl, tar, and sha256sum or shasum. No Go toolchain.
#
# The engine is not installed here. `jig setup` downloads it, verifies it
# against its own release, and links it onto PATH.
set -eu

REPO="kinged007/jigz"
PREFIX="${HOME}/.local"
VERSION=""
FROM_SOURCE=0

usage() { cat <<'EOF'
Install Jigz from a release binary, verified against the
release's checksums.txt.

One-liner:
  curl -sSL https://raw.githubusercontent.com/kinged007/jigz/main/install.sh | sh

Usage:
  ./install.sh                    install to ~/.local/bin
  ./install.sh --prefix DIR       install binaries under DIR/bin
  ./install.sh --version TAG      install a specific release tag
  ./install.sh --from-source      build from this checkout instead

Requirements: curl, tar, and sha256sum or shasum. No Go toolchain.

The engine is not installed here. `jig setup` downloads it, verifies it
against its own release, and links it onto PATH.
EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    --prefix) PREFIX="$2"; shift 2 ;;
    --prefix=*) PREFIX="${1#--prefix=}"; shift ;;
    --version) VERSION="$2"; shift 2 ;;
    --version=*) VERSION="${1#--version=}"; shift ;;
    --from-source) FROM_SOURCE=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "error: unknown flag $1" >&2; exit 1 ;;
  esac
done

command -v curl >/dev/null || { echo "error: curl not found" >&2; exit 1; }
command -v tar >/dev/null || { echo "error: tar not found" >&2; exit 1; }

# $0 carries a directory only when the script was run as a file. Piped
# through curl it is just "sh", and the current directory is whatever the
# user happens to be standing in — never a source tree to build from.
SRC=""
case "$0" in
  */*) SRC="$(cd "$(dirname "$0")" && pwd)" ;;
esac

BIN_DIR="${PREFIX}/bin"

sha256_of() {
  if command -v sha256sum >/dev/null; then
    sha256sum "$1" | cut -d' ' -f1
  else
    shasum -a 256 "$1" | cut -d' ' -f1
  fi
}

HAVE_HASH=0
if command -v sha256sum >/dev/null || command -v shasum >/dev/null; then
  HAVE_HASH=1
else
  echo "error: neither sha256sum nor shasum found; cannot verify the download." >&2
  echo "       install coreutils (or shasum) and re-run, or build from source" >&2
  echo "       with --from-source and a Go toolchain." >&2
  exit 1
fi

build_from_source() {
  command -v go >/dev/null || {
    echo "error: go not found, needed to build from source" >&2; exit 1; }
  if [ -z "$SRC" ] || [ ! -f "$SRC/go.mod" ]; then
    echo "error: --from-source needs a Jigz checkout (run install.sh from it)" >&2
    exit 1
  fi
  echo ":: building Jigz from $SRC ..."
  mkdir -p "$BIN_DIR"
  (cd "$SRC" && go build -trimpath -o "$BIN_DIR/jig" ./cmd/jig)
}

case "$(uname -s)/$(uname -m)" in
  Linux/x86_64)  OS_ARCH="linux_amd64" ;;
  Linux/aarch64|Linux/arm64) OS_ARCH="linux_arm64" ;;
  Darwin/x86_64) OS_ARCH="darwin_amd64" ;;
  Darwin/arm64)  OS_ARCH="darwin_arm64" ;;
  *) echo "error: no release binary for $(uname -s)/$(uname -m)" >&2
     echo "       use --from-source from a checkout, or set JIG_ENGINE_BIN at runtime." >&2
     exit 1 ;;
esac

if [ "$FROM_SOURCE" = 1 ]; then
  build_from_source
else
  # Resolve the tag from the /releases/latest redirect so the asset name
  # can carry the version, and so the installed version is reportable. A
  # repository with no releases leaves the URL unchanged, so anything
  # that is not a version is treated as unresolved.
  if [ -z "$VERSION" ]; then
    RESOLVED="$(curl -sIL -o /dev/null -w '%{url_effective}' \
      "https://github.com/${REPO}/releases/latest" 2>/dev/null | sed 's#.*/##')" || RESOLVED=""
    case "$RESOLVED" in
      v[0-9]*|[0-9]*) VERSION="$RESOLVED" ;;
    esac
  fi
  if [ -z "$VERSION" ]; then
    echo "error: could not resolve the latest release of ${REPO}" >&2
    echo "       pass --version TAG, or use --from-source from a checkout." >&2
    exit 1
  fi

  VER="${VERSION#v}"
  ASSET="jig_${VER}_${OS_ARCH}.tar.gz"
  if [ "$VERSION" = "$VER" ]; then
    BASE="https://github.com/${REPO}/releases/download/${VERSION}"
  else
    BASE="https://github.com/${REPO}/releases/download/v${VER}"
  fi

  TMP="$(mktemp -d)"
  trap 'rm -rf "$TMP"' EXIT

  echo ":: fetching ${ASSET} ..."
  if ! curl -sSfL "${BASE}/${ASSET}" -o "${TMP}/${ASSET}"; then
    echo "error: no release asset ${ASSET} at ${BASE}" >&2
    echo "       use --from-source from a checkout, or --version TAG." >&2
    exit 1
  fi

  echo ":: verifying against checksums.txt ..."
  if ! curl -sSfL "${BASE}/checksums.txt" -o "${TMP}/checksums.txt"; then
    echo "error: ${BASE}/checksums.txt is missing; refusing an unverified install" >&2
    exit 1
  fi
  WANT="$(awk -v a="$ASSET" '$2 == a { print $1 }' "${TMP}/checksums.txt")"
  if [ -z "$WANT" ]; then
    echo "error: checksums.txt lists no ${ASSET}; refusing to install" >&2
    exit 1
  fi
  GOT="$(sha256_of "${TMP}/${ASSET}")"
  if [ "$GOT" != "$WANT" ]; then
    echo "error: checksum mismatch for ${ASSET}" >&2
    echo "       got  ${GOT}" >&2
    echo "       want ${WANT}" >&2
    exit 1
  fi
  echo ":: checksum ok"

  # The archive holds one file, jig. Extract to a staging name and
  # move it into place so a shell starting mid-install never sees a
  # half-written binary.
  tar -xzf "${TMP}/${ASSET}" -C "$TMP"
  [ -f "${TMP}/jig" ] || { echo "error: archive holds no jig" >&2; exit 1; }
  mkdir -p "$BIN_DIR"
  chmod +x "${TMP}/jig"
  mv -f "${TMP}/jig" "${BIN_DIR}/jig"
  echo ":: installed ${BIN_DIR}/jig (${VERSION})"
fi

case ":$PATH:" in
  *":$BIN_DIR:"*) ;;
  *) echo ":: add to PATH: export PATH=\"$BIN_DIR:\$PATH\"" ;;
esac

cat <<'EOF'
:: next steps (run inside the target repo):
     jig setup    # writes .jigz/config.yaml, creates labels,
                  # installs the engine and links it onto PATH
     jig          # run every WORKFLOW.*.md loop (Ctrl-C stops all)
EOF
