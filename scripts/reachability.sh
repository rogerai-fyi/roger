#!/usr/bin/env bash
# reachability.sh — what is in the binary but nothing in the binary calls.
#
# WHY THIS EXISTS. Coverage says a function is EXERCISED. It does not say anything is
# USING it. The two come apart in the worst possible way: a helper written, tested to
# 100%, and wired to nothing looks healthier on every dashboard than the code that
# actually runs. Three of these have shipped here already —
#
#   * `storeFor`, the Tower's entire durable-storage wiring. Tested, called by nothing. A
#     Tower configured with the durable profile silently kept state on local disk: the one
#     deployment the profile exists for, and nothing failed.
#   * `ExpireLease`, which turned out to be a rename rather than a fix — same body, same
#     test-only callers, still in the binary.
#   * a whole admission-state gate whose edit targeted a pre-refactor type name, so it
#     compiled, passed, and gated nothing.
#
# A grep-based sweep will not find these, and worse, it lies in both directions: an earlier
# one counted a DOC COMMENT naming a symbol as a production caller. This asks the
# toolchain instead — golang.org/x/tools/cmd/deadcode builds the real call graph from every
# main() and reports what it cannot reach.
#
# Every finding is either a bug to fix or a decision to record in the allowlist beside this
# script, WITH ITS REASON. Anything unreachable and unexplained fails the check.
#
# WHAT THIS CANNOT SEE, measured rather than assumed. deadcode does NOT report an exported
# method whose receiver type is instantiated in production - it conservatively treats such a
# method as potentially reachable. Confirmed with a controlled experiment: an uncalled plain
# func on a live type's package IS reported, an uncalled exported method on *Registry is NOT.
# Unexported methods and methods of never-instantiated types are reported normally.
#
# That blind spot hid a production-fatal bug. The whole certificate-and-lease RENEWAL path -
# Core side and Tower side - was written, tested, and connected to no route, so every Tower
# would have stopped working 24 hours after enrolling. `make reach` passed throughout.
#
# So the sweep below is run as well: exported methods in the Tower tree with no non-test
# caller anywhere. It is a grep and it is cruder than a call graph - an interface-dispatched
# call may read as absent - which is why its findings are a QUESTION rather than a failure,
# and why it prints instead of exiting non-zero.
#
# Usage: scripts/reachability.sh          (fails on any unexplained unreachable function)
#        scripts/reachability.sh --list   (prints everything unreachable, allowlisted or not)
#        scripts/reachability.sh --methods (the exported-method sweep deadcode cannot do)
set -uo pipefail

cd "$(dirname "$0")/.."
ALLOW="scripts/reachability-allow.txt"

BIN="$(command -v deadcode || echo "$(go env GOPATH)/bin/deadcode")"
if [ ! -x "$BIN" ]; then
  echo "[reach] installing golang.org/x/tools/cmd/deadcode…" >&2
  GOFLAGS=-mod=mod go install golang.org/x/tools/cmd/deadcode@latest || {
    echo "[reach] SKIP: could not install deadcode (offline?)" >&2; exit 0; }
  BIN="$(go env GOPATH)/bin/deadcode"
fi

# Reachable from any main(). NOT -test: a function reachable only from a test is exactly
# what this is looking for.
found="$("$BIN" ./... 2>/dev/null | sed -E 's/^(.*): unreachable func: (.*)$/\1|\2/')"

# --- the exported-method sweep -------------------------------------------------------
methods_sweep() {
  local found=0
  for pkg in internal/relay internal/station internal/tower internal/towerjoin \
             internal/towerobj internal/towerstore internal/towercore/*/; do
    pkg=${pkg%/}; [ -d "$pkg" ] || continue
    grep -hoE '^func \([a-zA-Z_]+ \*?[A-Za-z0-9_]+\) [A-Z][A-Za-z0-9_]*' "$pkg"/*.go 2>/dev/null \
      | sed -E 's/^func \([a-zA-Z_]+ \*?([A-Za-z0-9_]+)\) //' | sort -u | while read -r m; do
      [ -z "$m" ] && continue
      n=$(grep -rn "\.$m(" --include='*.go' . 2>/dev/null \
            | grep -v '_test.go:' | grep -v "^\./$pkg/.*func (" | wc -l)
      if [ "$n" -eq 0 ] && ! grep -qxF "method $pkg.$m" "$ALLOW" 2>/dev/null; then
        echo "NO-CALLER    $pkg.$m"
        found=1
      fi
    done
  done
  return $found
}

if [ "${1:-}" = "--methods" ]; then
  methods_sweep
  exit 0
fi

if [ "${1:-}" = "--list" ]; then
  printf '%s\n' "$found"
  exit 0
fi

# The allowlist matches on FUNCTION NAME QUALIFIED BY PACKAGE PATH, not on line number:
# keying it to a line would make every edit above a function look like a new finding, and
# a check that cries wolf gets disabled.
fail=0
while IFS='|' read -r loc fn; do
  [ -z "$fn" ] && continue
  pkg="$(dirname "${loc%%:*}")"
  key="$pkg.$fn"
  if grep -qxF "$key" "$ALLOW" 2>/dev/null; then continue; fi
  echo "UNREACHABLE  $key"
  echo "             at $loc"
  fail=1
done <<< "$found"

# STALE ALLOWLIST LINES. An entry that no longer matches anything deadcode reports is a
# line that does nothing - the symbol was deleted, or it became reachable. Either way the
# line is inert, and an inert line is worse than no line: it sits there pre-authorizing a
# name, so if that name ever comes back the check waves it through and nobody is told.
#
# That is not hypothetical. `internal/towercore/attach.memStore.BySessionKey` was deleted
# with the session-key uniqueness rule it served, and its allowlist entry stayed behind -
# silently guaranteeing that re-adding the exact lookup the removal was careful to delete
# would pass this gate. An audit found it by reading, which is the wrong way to find it.
#
# Advisory rather than fatal: a stale line breaks nothing today, and this check's own
# header says a gate that cries wolf gets disabled. Loud enough to fix, quiet enough to
# ignore during an unrelated push.
#
# MATCH THE WHOLE KEY, exactly as the loop above builds it. The first version of this check
# compared the allowlist line's suffix after the last dot against the findings - and a
# finding for a method carries its receiver (`cmd/rogerai-broker.memStore.markInflightBatch`),
# so `|Admit` never matched `|memStore.Admit` and EVERY method entry was reported stale
# whether it existed or not. It appeared to work because the one entry it was written to
# catch had genuinely been deleted; it would have flagged that line either way. A check that
# is right for the wrong reason is the thing this whole script exists to stop.
found_keys=""
while IFS='|' read -r loc fn; do
  [ -z "$fn" ] && continue
  found_keys="${found_keys}$(dirname "${loc%%:*}").$fn
"
done <<< "$found"
stale=""
while IFS= read -r line; do
  case "$line" in ''|'#'*|'method '*) continue ;; esac
  if ! printf '%s' "$found_keys" | grep -qxF "$line"; then
    stale="${stale}${line}\n"
  fi
done < "$ALLOW"
if [ -n "$stale" ]; then
  echo
  echo "[reach] allowlist lines that no longer match anything — delete them, or the name"
  echo "        they pre-authorize comes back unnoticed:"
  printf "%b" "$stale" | sed 's/^/        /'
  echo
fi

# Advisory, printed after the hard check so it cannot be mistaken for one. These are the
# methods deadcode structurally cannot rule on; each is a question, not a verdict.
sweep="$(methods_sweep || true)"
if [ -n "$sweep" ]; then
  echo
  echo "[reach] exported methods with no non-test caller — deadcode cannot see these."
  echo "        Each is a question: wire it up, delete it, or record it in $ALLOW as"
  echo "        'method <pkg>.<Name>' with the reason."
  printf '%s\n' "$sweep"
  echo
fi

if [ "$fail" = 0 ]; then
  echo "[reach] PASS — every unreachable function is accounted for in $ALLOW"
else
  cat >&2 <<'EOT'

[reach] FAIL — the functions above are in the binary and nothing in the binary calls them.

Each one is a question with two honest answers:
  * It should be called and is not  -> that is the bug. Wire it up.
  * It exists for a reason that is not production (a reference implementation held
    against a durable store, a test seam, a fail-closed placeholder for a route that
    does not exist yet) -> add it to scripts/reachability-allow.txt WITH THE REASON.

"It is tested" is not one of the answers. Every example above was tested.
EOT
fi
exit "$fail"
