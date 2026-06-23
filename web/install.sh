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
# dl URL FILE        download to a file (fails on HTTP errors)
# dl_stdout URL      download to stdout
# resolve_redirect URL  print the final URL after following redirects
if command -v curl >/dev/null 2>&1; then
  dl()              { curl -fsSL "$1" -o "$2"; }
  dl_stdout()       { curl -fsSL "$1"; }
  resolve_redirect(){ curl -fsSLI -o /dev/null -w '%{url_effective}' "$1" 2>/dev/null; }
elif command -v wget >/dev/null 2>&1; then
  dl()              { wget -qO "$2" "$1"; }
  dl_stdout()       { wget -qO- "$1"; }
  # --max-redirect 0 makes wget print the Location: header to stderr; grab it.
  resolve_redirect(){ wget -S --max-redirect=0 -qO /dev/null "$1" 2>&1 \
                        | awk '/^ *[Ll]ocation:/ {print $2; exit}'; }
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
  # follow the redirect from /releases/latest to read the tag (curl or wget)
  REDIR="$(resolve_redirect "https://github.com/$REPO/releases/latest" || true)"
  REDIR="${REDIR%/}"   # strip any trailing slash/CR
  REDIR="$(printf '%s' "$REDIR" | tr -d '\r')"
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

# ---- verify checksum (if the release ships checksums.txt) -----------
# Pick whatever sha256 tool the distro provides. sha256sum (coreutils:
# Debian/Ubuntu/Fedora/Arch/Gentoo/openSUSE/Bazzite), shasum -a 256
# (macOS/Perl), or sha256 (BSD). If none exist, skip with a warning
# rather than failing - the binary is still served over HTTPS.
sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | cut -d' ' -f1
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | cut -d' ' -f1
  elif command -v sha256 >/dev/null 2>&1; then
    sha256 -q "$1"
  else
    return 1
  fi
}

SUMS="$TMP/checksums.txt"
if dl "https://github.com/$REPO/releases/download/$TAG/checksums.txt" "$SUMS" 2>/dev/null \
   && [ -s "$SUMS" ]; then
  # the expected hash is the line whose filename column matches our asset
  WANT="$(awk -v f="$ASSET" '$2 == f || $2 == "*"f {print $1; exit}' "$SUMS")"
  if [ -n "$WANT" ]; then
    GOT="$(sha256_of "$OUT")" || {
      say ""
      say "  ${C_DIM}note:${C_RESET} no sha256 tool found - skipping checksum verification."
      GOT=""
    }
    if [ -n "$GOT" ]; then
      if [ "$GOT" = "$WANT" ]; then
        ok "checksum verified"
      else
        die "checksum mismatch for ${ASSET} - refusing to install. expected ${WANT}, got ${GOT}."
      fi
    fi
  else
    say "  ${C_DIM}note:${C_RESET} ${ASSET} not listed in checksums.txt - skipping verification."
  fi
else
  say "  ${C_DIM}note:${C_RESET} no checksums.txt in this release - skipping verification."
fi

# ---- install (atomic) ----------------------------------------------
# Stage into INSTALL_DIR so the final rename is on the same filesystem
# (mv across filesystems isn't atomic and can leave a half-written bin
# if interrupted). chmod first, then rename over any existing copy -
# this is idempotent, so re-running the installer just updates in place.
if ! mkdir -p "$INSTALL_DIR" 2>/dev/null; then
  die "can't create ${INSTALL_DIR}. Set ROGERAI_INSTALL_DIR to a writable directory."
fi
[ -w "$INSTALL_DIR" ] || die "${INSTALL_DIR} isn't writable. Set ROGERAI_INSTALL_DIR to a writable directory."

DEST="$INSTALL_DIR/$BIN$EXT"
STAGE="$INSTALL_DIR/.$BIN.$$.tmp"
cp "$OUT" "$STAGE" || die "failed to stage binary in ${INSTALL_DIR}."
chmod +x "$STAGE" 2>/dev/null || true
if ! mv -f "$STAGE" "$DEST"; then
  rm -f "$STAGE" 2>/dev/null || true
  die "failed to install to ${DEST}."
fi
ok "installed ${C_BOLD}${BIN}${C_RESET} → ${DEST}"

# ---- PATH hint ------------------------------------------------------
# On immutable distros (Bazzite/Silverblue/Kinoite) ~/.local/bin is on
# PATH by default, so this block stays quiet there. Elsewhere we point
# at the right rc file for the user's login shell.
case ":$PATH:" in
  *":$INSTALL_DIR:"*) : ;;
  *)
    say ""
    say "  ${C_DIM}note:${C_RESET} ${INSTALL_DIR} isn't on your PATH. Add it:"
    case "${SHELL##*/}" in
      fish)
        say "    fish_add_path $INSTALL_DIR"
        ;;
      zsh)
        rc="${ZDOTDIR:-$HOME}/.zshrc"
        say "    echo 'export PATH=\"$INSTALL_DIR:\$PATH\"' >> $rc && . $rc"
        ;;
      bash)
        # bash reads ~/.bashrc interactively; ~/.profile for login shells.
        rc="$HOME/.bashrc"; [ -f "$rc" ] || rc="$HOME/.profile"
        say "    echo 'export PATH=\"$INSTALL_DIR:\$PATH\"' >> $rc && . $rc"
        ;;
      *)
        say "    echo 'export PATH=\"$INSTALL_DIR:\$PATH\"' >> ~/.profile && . ~/.profile"
        ;;
    esac
    say "    ${C_DIM}then restart your shell (or open a new terminal).${C_RESET}"
    ;;
esac

say ""
ok "roger that. run ${C_BOLD}${C_VOLT}${BIN}${C_RESET} to go on air or tune in."
say ""
