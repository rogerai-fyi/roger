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
# Usage: scripts/reachability.sh          (fails on any unexplained unreachable function)
#        scripts/reachability.sh --list   (prints everything unreachable, allowlisted or not)
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
