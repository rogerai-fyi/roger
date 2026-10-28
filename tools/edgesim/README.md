# edgesim - simulated Raspberry Pis

Containers that stand in for Raspberry Pis on an Edge: a simulated authority (`hub`) and N simulated
Pis, each on its own network stack, joined exactly the way a real Pi joins - the authority allows the
Pi's key, then the Pi enrolls - and kept running with `roger edge run`, as a Pi's boot service does.

They run Debian Trixie (what Raspberry Pi OS Trixie is built on) and the x86_64 build of this branch,
so they simulate a Pi's ROLE on an Edge - joining, being discovered, monitored, controlled, dropping
off the network and coming back - not its CPU. `ROGERAI_EDGE_MACHINE` names the board.

Needs rootless podman.

```sh
tools/edgesim/edgesim up 3            # a hub and pi-1..pi-3
tools/edgesim/edgesim status          # the hub's view of its Edge
tools/edgesim/edgesim fleet           # the hub's live fleet window
tools/edgesim/edgesim cut pi-3        # take pi-3 off the network
tools/edgesim/edgesim heal pi-3       # put it back (it rejoins on its own)
tools/edgesim/edgesim reboot pi-2     # a power cycle
tools/edgesim/edgesim roger pi-1 model status
tools/edgesim/edgesim desk            # a member you drive by hand, joined like a Pi
tools/edgesim/edgesim tui desk        # roger's screen on it (its Edge runs while the screen is open)
tools/edgesim/edgesim down            # remove everything, volumes too
```

Each Pi joins with the one-time code `roger edge authority allow` prints on the hub, as an owner would
copy it (features/edge/enrollment_root_trust.feature). `EDGESIM_ROGER=/path/to/roger` runs a prebuilt
roger in every machine instead of building one, `EDGESIM_PREFIX=<word>` names a second simulated Edge
so it never touches the first, and `EDGESIM_PUBLISH=1` publishes the hub's authority on this host at
`EDGESIM_PUBLISH_PORT` (default 39019) so a phone on your network can join it by address.

Two roger processes on one machine share mDNS: Go's multicast listener sets SO_REUSEADDR, and roger
turns multicast loopback back on (Go turns it off), so a second instance with its own config and face
port (`XDG_CONFIG_HOME`, `ROGERAI_EDGE_BIND`) is discovered like any other member.

## Beside a physical Pi: host mode

The containers form an Edge of their own. To put simulated Pis on the Edge rooted on THIS machine, the
real one a physical Pi joined, run them as plain processes instead. No root, no containers:

```sh
tools/edgesim/edgesim host up 3          # sim-pi-1..3, each joined with an invite from your authority
tools/edgesim/edgesim host status        # their processes, then your authority's `roger edge`
tools/edgesim/edgesim host reboot sim-pi-2
tools/edgesim/edgesim host roger sim-pi-1 model status
tools/edgesim/edgesim host down          # stop them, forget them on your authority, remove their state
```

The authority must be running (roger open on this machine) and must be this branch's build:
`EDGESIM_AUTHORITY_ROGER=/path/to/roger` names it, `EDGESIM_AUTHORITY_CONFIG=<dir>` points at a
different authority's config, and `EDGESIM_AT=<address>:<port>` overrides the address invites name.
Each simulated Pi keeps its config, log and pid under `~/.local/state/edgesim/` and faces on port
`44100 + n`, on every address, so the physical Pi dials it too.

What host mode gives up: no memory or CPU limit, no network cut (`reboot` is the drop it has), and the
host's GPUs are visible, so a simulated Pi here judges models against this machine. Every simulated
Pi is named `sim-pi-*` so it never passes for a real board on your Edge.

`EDGESIM_MODEL` sets the board name (default `Raspberry Pi 5 Model B Rev 1.0`). Each Pi is held to a Pi 5's
resources - `EDGESIM_MEMORY` (default `8g`) and `EDGESIM_CPUS` (default `4`) - and cannot see the host's GPUs.

The simulated network may have no internet (rootless networks share one namespace whose outbound path
podman chose itself). `edgesim seed <pi>` copies this machine's CPU llama.cpp runtime and its models into
a Pi, so serving and switching models can be exercised anyway.

What it found (all fixed):

- A machine enrolled as `pi-1` was listed under its hardware word. The name in an enrollment the
  authority accepted is now the name the member is first seen under.
- A member whose network interface went away and came back never reappeared until roger restarted.
  Discovery now rejoins changed interfaces and advertises the addresses a machine has now.
- roger judged a model against the host's memory inside a container held to far less; a container's
  memory limit is now the RAM a model can use.
- Asked from afar, roger ran the fit tool of a GPU build that was not installed.
- A Pi cut and healed got a new address, advertised it, and was never seen again: its face kept
  listening only on the address it started with. The face now follows address changes.
- A model shared with `roger --lan share` on a Pi never reached its Edge: a band detected as "chat"
  skipped the record the Edge face reads.
- Two roger processes on one machine never heard each other (host mode): Go's multicast listener turns
  loopback off. roger's listener turns it on, and a machine hearing its own advert skips it. Turning it
  on showed a second fault: on Linux one packet sent on three interfaces was heard nine times, by every
  socket. Each socket now hears only the interface it joined.

The simulated Edge is self-contained: rootless containers live on a network the host cannot see, so
they do not join an Edge rooted on the host, and a host roger that enrolls into the simulated hub
(through the published port) is never seen: members are found by mDNS and verified by dialing their
face, and neither crosses the boundary. A phone is the exception, because it pushes its presence to
the authority instead of being dialed. Host mode (above) is the way to mix real and simulated machines
on one Edge.
