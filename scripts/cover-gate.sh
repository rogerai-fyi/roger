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
# Usage: scripts/cover-gate.sh [MIN_TOTAL]   (default 85 — the GREEN bar; aim 95%+)
set -uo pipefail

# Founder policy: nothing is GREEN below 85% coverage; the aim is 95%+. Until the backfill
# reaches the bar the gate is RED by design (start with the 0% Postgres ledger). Bypass a
# local push during the backfill with COVER_GATE_SKIP=1; CI has no bypass.
GREEN_BAR=85
MIN_TOTAL="${1:-$GREEN_BAR}"
PROFILE="${COVER_PROFILE:-cover.out}"
MOD="github.com/rogerai-fyi/roger"

# Every package's floor is the 85% GREEN bar (aim 95%). No per-package exemptions — the
# founder's rule is no green below 85%. Honestly-untestable glue (main()/serve loops) must
# be refactored to be testable rather than exempted.
floor_for() { echo "$GREEN_BAR"; }

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
