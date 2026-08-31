#!/usr/bin/env bash
# Decide which throwaway-Postgres containers the coverage gate may reclaim.
#
# Reads container names on stdin (podman/docker `ps -a --format "{{.Names}}"`) and prints
# the ones that are safe to remove. It performs no removal itself: separating the DECISION
# from the action is what makes it testable without a container runtime, and this logic has
# already shipped broken twice in ways no string-matching test could see.
#
# The rules, in order:
#   not one of ours  -> skip. Another project's container is not ours to remove.
#   other namespace  -> skip. A pid means nothing outside its own PID namespace, so two
#                       gates in separate containers sharing a docker socket must not judge
#                       each other's.
#   owner alive      -> skip. Checked owner-agnostically: `kill -0` returns EPERM for
#                       another user's process, reads as "dead", and would delete a live
#                       run's database.
#   otherwise        -> reclaim.
#
# The container's STATE is deliberately not consulted. An earlier version reclaimed any
# container that was not running, as a backstop for a recycled pid - and that rule was
# tested BEFORE the liveness check, so it short-circuited it: `run -d` reports "created"
# between create and start, and a concurrently starting gate would have deleted a live
# sibling's database in that window. Which is the exact failure this whole sweep exists to
# prevent, arrived at from a third direction.
#
# The cost of dropping it is stated rather than hidden: a container whose pid has since
# been recycled is never reclaimed and leaks. A stopped one costs only metadata; a running
# one holds its published port and its memory until somebody removes it by hand. That is
# still the right side of this trade - a leak is recoverable, deleting a running run's
# database is not - but it is a leak, not a rounding error.
set -uo pipefail

BASE="rogerai-covergate-pg"
PREFIX="$BASE-"
NS="${PG_NS:-0}"

while IFS= read -r ct; do
  ct="${ct%%[[:space:]]*}"                       # tolerate extra columns
  [ -n "$ct" ] || continue
  case "$ct" in "$BASE") ;; "$PREFIX"*) ;; (*) continue ;; esac
  rest="${ct#"$BASE"}"   # "" for the old fixed name, else "-<...>"
  rest="${rest#-}"
  # LEGACY NAMES, from before this scheme, are reclaimed on the pid alone. The fixed
  # "rogerai-covergate-pg" has no suffix and belongs to no current run at all; the
  # "<pid>" form predates the namespace segment. Neither can ever match a namespace, so
  # without this they were never reclaimed - and a RUNNING legacy orphan holds its
  # published port and its memory, which is a good deal worse than the stopped-container
  # metadata this file's trade-off is written around.
  if [ -z "$rest" ]; then echo "$ct"; continue; fi   # the old fixed name
  case "$rest" in
    *-*) ns="${rest%-*}"; owner="${rest##*-}" ;;
    *)   ns="$NS";        owner="$rest" ;;           # the old "<pid>" form
  esac
  case "$owner" in (*[!0-9]*|"") continue ;; esac
  [ "$ns" = "$NS" ] || continue
  if [ -d "/proc/$owner" ] || ps -p "$owner" >/dev/null 2>&1; then continue; fi
  echo "$ct"
done
