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
# Usage: scripts/cover-gate.sh [MIN_TOTAL]   (default 90 — the GREEN bar; aim 95%+)
set -uo pipefail

# Founder policy: the GREEN bar is 90% (ratcheted up from 85 once every package cleared
# 90); the aim is 95%+. Bypass a local push only in a genuine emergency with
# COVER_GATE_SKIP=1; CI has no bypass.
GREEN_BAR=90
MIN_TOTAL="${1:-$GREEN_BAR}"
PROFILE="${COVER_PROFILE:-cover.out}"
MOD="github.com/rogerai-fyi/roger"

# Every package's floor is the 90% GREEN bar (aim 95%). No per-package exemptions — the
# founder's rule is no green below the bar. Honestly-untestable glue (main()/serve loops)
# must be refactored to be testable rather than exempted.
floor_for() { echo "$GREEN_BAR"; }

# --- Postgres-aware coverage -------------------------------------------------
# postgres.go is ~half of internal/store; without a real DB its money path counts
# as 0% and the package can NEVER reach the bar. If no DSN is supplied and a
# container runtime is present, spin up a throwaway Postgres (schema `rogerai`,
# which prod provisions out-of-band) so the gate honestly exercises the SQL path.
# Self-contained: works on a dev box (podman) and on CI runners (docker).
PG_CT=""
RUNTIME=""
cleanup_pg() { [ -n "$PG_CT" ] && "$RUNTIME" rm -f "$PG_CT" >/dev/null 2>&1; return 0; }
trap cleanup_pg EXIT
if [ -z "${ROGERAI_TEST_DATABASE_URL:-}" ]; then
  command -v podman >/dev/null 2>&1 && RUNTIME=podman
  [ -z "$RUNTIME" ] && command -v docker >/dev/null 2>&1 && RUNTIME=docker
  if [ -n "$RUNTIME" ]; then
    PG_CT="rogerai-covergate-pg"
    "$RUNTIME" rm -f "$PG_CT" >/dev/null 2>&1
    echo "[cover] starting throwaway Postgres ($RUNTIME) for the store money path…" >&2
    if "$RUNTIME" run -d --name "$PG_CT" -e POSTGRES_PASSWORD=test -e POSTGRES_DB=roger_test \
        -p 5466:5432 docker.io/library/postgres:16 >/dev/null 2>&1; then
      # pg_isready can flip true while the postgres entrypoint is still creating
      # POSTGRES_DB on its temporary bootstrap server, so polling it then firing a
      # one-shot CREATE SCHEMA races (and used to swallow the failure, leaving the
      # store tests to die with "schema rogerai does not exist"). Instead, retry the
      # CREATE SCHEMA itself until it succeeds against the real roger_test DB — that
      # is the readiness signal — and fail loudly if it never does.
      schema_ok=0
      for _ in $(seq 1 60); do
        if "$RUNTIME" exec "$PG_CT" psql -U postgres -d roger_test \
            -c "CREATE SCHEMA IF NOT EXISTS rogerai;" >/dev/null 2>&1; then
          schema_ok=1; break
        fi
        sleep 1
      done
      if [ "$schema_ok" != 1 ]; then
        echo "[cover] ERROR: test Postgres never became ready / could not create the rogerai schema" >&2
        exit 1
      fi
      export ROGERAI_TEST_DATABASE_URL="postgres://postgres:test@localhost:5466/roger_test?sslmode=disable"
    else
      echo "[cover] WARNING: could not start a test Postgres; postgres.go will count as uncovered" >&2
      PG_CT=""
    fi
  else
    echo "[cover] WARNING: no podman/docker and no ROGERAI_TEST_DATABASE_URL — postgres.go will count as uncovered" >&2
  fi
fi

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
