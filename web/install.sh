#!/bin/sh
# =====================================================================
# RogerAI installer - a two-way radio for GPUs.
#   curl -fsSL https://rogerai.fyi/install.sh | sh
#
# Downloads the right `rogerai` binary for your OS/arch from GitHub
# releases and drops it in ~/.local/bin. POSIX sh, no dependencies
# beyond curl-or-wget + tar/unzip-where-needed. Degrades gracefully
# with a clear message if a release/asset is missing.
# =====================================================================
set -eu

REPO="bownux/rogerai"
BIN="rogerai"
INSTALL_DIR="${ROGERAI_INSTALL_DIR:-$HOME/.local/bin}"
# Override the version with:  ROGERAI_VERSION=v0.2.0 sh install.sh
VERSION="${ROGERAI_VERSION:-latest}"

# ---- pretty output (only if stderr is a TTY) ------------------------
if [ -t 2 ]; then
  C_RESET="$(printf '\033[0m')"; C_DIM="$(printf '\033[2m')"
  C_VOLT="$(printf '\033[38;5;99m')"; C_OK="$(printf '\033[38;5;42m')"
  C_ERR="$(printf '\033[38;5;203m')"; C_BOLD="$(printf '\033[1m')"
else
  C_RESET=""; C_DIM=""; C_VOLT=""; C_OK=""; C_ERR=""; C_BOLD=""
fi
say()  { printf '%s\n' "$*" >&2; }
info() { printf '%s•%s %s\n' "$C_VOLT" "$C_RESET" "$*" >&2; }
ok()   { printf '%s✓%s %s\n' "$C_OK" "$C_RESET" "$*" >&2; }
die()  { printf '%s✗ %s%s\n' "$C_ERR" "$*" "$C_RESET" >&2; exit 1; }

say ""
say "  ${C_BOLD}${C_VOLT}RogerAI${C_RESET} ${C_DIM}- a two-way radio for GPUs${C_RESET}"
say ""

# ---- detect downloader ---------------------------------------------
if command -v curl >/dev/null 2>&1; then
  dl()      { curl -fsSL "$1" -o "$2"; }
  dl_stdout(){ curl -fsSL "$1"; }
elif command -v wget >/dev/null 2>&1; then
  dl()      { wget -qO "$2" "$1"; }
  dl_stdout(){ wget -qO- "$1"; }
else
  die "need curl or wget to install RogerAI."
fi

# ---- detect OS ------------------------------------------------------
os="$(uname -s 2>/dev/null || echo unknown)"
case "$os" in
  Linux)   OS="linux" ;;
  Darwin)  OS="darwin" ;;
  MINGW*|MSYS*|CYGWIN*|Windows_NT) OS="windows" ;;
  *) die "unsupported OS: $os. See https://github.com/$REPO/releases" ;;
esac

# ---- detect arch ----------------------------------------------------
arch="$(uname -m 2>/dev/null || echo unknown)"
case "$arch" in
  x86_64|amd64)        ARCH="amd64" ;;
  aarch64|arm64)       ARCH="arm64" ;;
  *) die "unsupported architecture: $arch. See https://github.com/$REPO/releases" ;;
esac

EXT=""
[ "$OS" = "windows" ] && EXT=".exe"
ASSET="${BIN}-${OS}-${ARCH}${EXT}"
info "platform: ${C_BOLD}${OS}/${ARCH}${C_RESET}"

# ---- resolve version ------------------------------------------------
if [ "$VERSION" = "latest" ]; then
  info "resolving latest release…"
  # follow the redirect from /releases/latest to read the tag
  REDIR="$(curl -fsSLI -o /dev/null -w '%{url_effective}' \
            "https://github.com/$REPO/releases/latest" 2>/dev/null || true)"
  TAG="${REDIR##*/tag/}"
  case "$TAG" in
    ""|*github.com*|*releases*) TAG="" ;;   # no published release yet
  esac
else
  TAG="$VERSION"
fi

if [ -z "${TAG:-}" ]; then
  say ""
  say "  ${C_ERR}No published release found for ${REPO} yet.${C_RESET}"
  say ""
  say "  RogerAI is brand new - releases are on their way. In the meantime:"
  say "    ${C_DIM}# build from source (needs Go)${C_RESET}"
  say "    git clone https://github.com/$REPO && cd rogerai"
  say "    go build -o ~/.local/bin/$BIN ./cmd/rogerai"
  say ""
  say "  Watch ${C_VOLT}https://github.com/$REPO/releases${C_RESET} for prebuilt binaries."
  say ""
  exit 1
fi

URL="https://github.com/$REPO/releases/download/$TAG/$ASSET"
info "version:  ${C_BOLD}${TAG}${C_RESET}"

# ---- download to a temp file ---------------------------------------
TMP="$(mktemp -d 2>/dev/null || mktemp -d -t rogerai)"
trap 'rm -rf "$TMP"' EXIT INT TERM
OUT="$TMP/$BIN$EXT"

info "downloading ${ASSET}…"
if ! dl "$URL" "$OUT"; then
  say ""
  say "  ${C_ERR}Couldn't download ${ASSET} for ${TAG}.${C_RESET}"
  say "  That build may not exist for your platform yet."
  say "  Browse what's available: ${C_VOLT}https://github.com/$REPO/releases/$TAG${C_RESET}"
  say ""
  exit 1
fi

# sanity check: non-empty file
[ -s "$OUT" ] || die "downloaded file is empty - aborting."

# ---- install --------------------------------------------------------
mkdir -p "$INSTALL_DIR"
chmod +x "$OUT" 2>/dev/null || true
DEST="$INSTALL_DIR/$BIN$EXT"
mv -f "$OUT" "$DEST"
ok "installed ${C_BOLD}${BIN}${C_RESET} → ${DEST}"

# ---- PATH hint ------------------------------------------------------
case ":$PATH:" in
  *":$INSTALL_DIR:"*) : ;;
  *)
    say ""
    say "  ${C_DIM}note:${C_RESET} ${INSTALL_DIR} isn't on your PATH. Add it:"
    case "${SHELL##*/}" in
      fish) say "    fish_add_path $INSTALL_DIR" ;;
      *)    say "    echo 'export PATH=\"$INSTALL_DIR:\$PATH\"' >> ~/.profile && . ~/.profile" ;;
    esac
    ;;
esac

say ""
ok "roger that. run ${C_BOLD}${C_VOLT}${BIN}${C_RESET} to go on air or tune in."
say ""
