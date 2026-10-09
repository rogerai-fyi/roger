#!/bin/sh
# tools/edgesim: a simulated machine's life. The hub roots a local Edge and runs; a member waits until
# the harness has enrolled it, then runs - exactly `roger edge run`, as a Pi's boot service would.
set -e
CRT="$HOME/.config/rogerai/edge/node.crt"
case "$1" in
hub)
	if [ ! -s "$CRT" ]; then
		roger edge authority local "${EDGESIM_NAME:-sim-hub}"
		roger edge enroll "${EDGESIM_NAME:-sim-hub}"
	fi
	exec roger edge run
	;;
member)
	until [ -s "$CRT" ]; do sleep 1; done
	exec roger edge run
	;;
desk)
	# A machine a person drives: joined like any member, but its Edge runs inside roger's screen
	# (edgesim tui), the way a laptop's does - not as a boot service.
	trap 'exit 0' TERM INT
	while :; do sleep 3600 & wait $!; done
	;;
*)
	exec "$@"
	;;
esac
