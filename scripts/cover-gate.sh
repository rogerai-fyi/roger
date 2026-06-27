#!/usr/bin/env bash
# cover-gate.sh — the RogerAI coverage gate (spec-first TDD; see TDD-WORKFLOW.md).
#
# Enforces three rules, in one `go test` pass:
#   (1) NO package without coverage — any package that has Go source but no tests, or
#       0.0% statement coverage, FAILS the gate. (The founder's rule: every package we
#       ship must be exercised.)
#   (2) Per-package FLOORS for the critical/money/auth packages — ratcheted UP over time
#       as coverage grows; a regression below the floor FAILS.
#   (3) A module-wide TOTAL floor, also ratcheted up.
#
# Self-coverage is used (each package's OWN tests cover it) — the honest per-package lens.
# Usage: scripts/cover-gate.sh [MIN_TOTAL]   (default 59 = today's self-coverage; ratchet up)
set -uo pipefail

MIN_TOTAL="${1:-59}"
PROFILE="${COVER_PROFILE:-cover.out}"
MOD="github.com/rogerai-fyi/roger"

# Per-package floors (module-relative path). Start at/below today's measured self-coverage
# so the gate passes now; RAISE these as tests land (especially internal/store once the
# Postgres money path is covered → target 90+).
floor_for() {
  case "$1" in
    internal/protocol)  echo 80 ;;   # ed25519 signing, pricing, receipts
    internal/store)     echo 40 ;;   # ledger/billing/payouts — RAISE to 90 after Postgres tests
    cmd/rogerai-broker) echo 70 ;;   # relay/settle/auth/moderation/multi-instance
    internal/node)      echo 64 ;;
    internal/detect)    echo 85 ;;
    internal/tokenizer) echo 80 ;;
    *)                  echo 0  ;;
  esac
}

echo "[cover] running full suite with coverage…" >&2
if ! out="$(go test -covermode=atomic -coverprofile="$PROFILE" ./... 2>&1)"; then
  echo "$out"
  echo "[cover] FAIL: the test suite did not pass"
  exit 1
fi

fail=0
while IFS= read -r line; do
  case "$line" in
    *"[no test files]"*)
      pkg="$(printf '%s' "$line" | grep -oE "$MOD/[^ ]+" | sed "s#$MOD/##")"
      [ -z "$pkg" ] && continue
      echo "FAIL  $pkg  — NO TEST FILES (every package must have coverage)"; fail=1 ;;
    *"coverage:"*)
      pkg="$(printf '%s' "$line" | grep -oE "$MOD/[^ 	]+" | sed "s#$MOD/##")"
      [ -z "$pkg" ] && continue
      printf '%s' "$line" | grep -q "\[no statements\]" && continue   # nothing to cover
      pct="$(printf '%s' "$line" | grep -oE 'coverage: [0-9.]+%' | grep -oE '[0-9.]+')"
      [ -z "$pct" ] && continue
      fl="$(floor_for "$pkg")"
      if awk -v p="$pct" 'BEGIN{exit !(p+0==0)}'; then
        echo "FAIL  $pkg  ${pct}%  — ZERO coverage"; fail=1
      elif awk -v p="$pct" -v f="$fl" 'BEGIN{exit !(p+0 < f+0)}'; then
        echo "FAIL  $pkg  ${pct}% < ${fl}% floor"; fail=1
      else
        suffix=""; [ "$fl" != 0 ] && suffix=" (>=${fl}%)"
        echo "ok    $pkg  ${pct}%${suffix}"
      fi ;;
  esac
done <<< "$out"

total="$(go tool cover -func="$PROFILE" | awk '/^total:/{gsub("%","",$3);print $3}')"
if awk -v t="$total" -v m="$MIN_TOTAL" 'BEGIN{exit !(t+0 < m+0)}'; then
  echo "FAIL  total ${total}% < ${MIN_TOTAL}%"; fail=1
else
  echo "ok    total ${total}% (>=${MIN_TOTAL}%)"
fi

if [ "$fail" = 0 ]; then echo "[cover] gate PASS"; else echo "[cover] gate FAIL — raise coverage or fix tests"; fi
exit "$fail"
