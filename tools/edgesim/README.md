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
tools/edgesim/edgesim down            # remove everything, volumes too
```

`EDGESIM_MODEL` sets the board name (default `Raspberry Pi 5 Model B Rev 1.0`).

What it found the first time it ran (both fixed):

- A machine enrolled as `pi-1` was listed under its hardware word. The name in an enrollment the
  authority accepted is now the name the member is first seen under.
- A member whose network interface went away and came back never reappeared until roger restarted.
  Discovery now rejoins changed interfaces and advertises the addresses a machine has now.

The simulated Edge is self-contained: rootless containers live on a network the host cannot see, so
they do not join an Edge rooted on the host. Joining real and simulated machines on one Edge needs a
host-visible bridge (rootful podman), which is set up by the owner, never by this script.
