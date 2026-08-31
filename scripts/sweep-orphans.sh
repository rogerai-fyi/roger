#!/usr/bin/env bash
# Decide which throwaway-Postgres containers the coverage gate may reclaim.
#
# Reads "<name> <state>" lines on stdin (podman/docker `ps -a --format
# "{{.Names}} {{.State}}"`) and prints the names that are safe to remove. It performs no
# removal itself: separating the DECISION from the action is what makes it testable
# without a container runtime, and this logic has already shipped broken once in a way no
# string-matching test could see - a `while IFS= read -r ct state` never split the two
# fields, so `state` was always empty, every container was skipped, and the sweep silently
# did nothing at all.
#
# The rules, in order:
#   not running      -> reclaim. A live sibling's container is running, whatever its pid
#                       now belongs to; this is the backstop for a RECYCLED pid, which
#                       would otherwise leak the container forever.
#   other namespace  -> skip. A pid means nothing outside its own PID namespace, so two
#                       gates in separate containers sharing a docker socket must not
#                       judge each other's.
#   owner alive      -> skip. Checked owner-agnostically: `kill -0` returns EPERM for
#                       another user's process, reads as "dead", and would delete a live
#                       run's database.
#   otherwise        -> reclaim.
set -uo pipefail

PREFIX="rogerai-covergate-pg-"
NS="${PG_NS:-0}"

while IFS=' ' read -r ct state; do
  [ -n "$ct" ] || continue
  case "$ct" in "$PREFIX"*) ;; (*) continue ;; esac
  rest="${ct#"$PREFIX"}"
  ns="${rest%-*}"
  owner="${rest##*-}"
  case "$owner" in (*[!0-9]*|"") continue ;; esac
  if [ "$state" != "running" ]; then echo "$ct"; continue; fi
  [ "$ns" = "$NS" ] || continue
  if [ -d "/proc/$owner" ] || ps -p "$owner" >/dev/null 2>&1; then continue; fi
  echo "$ct"
done
