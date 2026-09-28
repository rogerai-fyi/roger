# v6.12.0 - the rack: your Edge as a station, with trouble calls that reach your phone

Roger Edge grows a fleet manager. This release is the machine side: the screens, the
health rules and the alerts, and a private feed the Roger app on your phone pulls from.
Draft: the version is a placeholder until the release is cut.

## The fleet screens

`roger edge fleet` (or `o` on the TUI's [3] screen) opens the Edge as a 1950s station's
equipment rack: this machine is the transmitter at the top with its carrier breathing, the
machines and devices you adopted are units patched in below, a device asking to join waits on
the bench. Every unit opens with a magic eye - a tuning tube whose iris closes as the signal
drops, red on the left when a rule fires - and carries a nine-cell S-meter. The screens are
built on Limoni, a new terminal engine that repaints only the cells that change; they run as
their own process from the TUI and hand the terminal back when you quit.

Tabs: FLEET (adopt, re-admit, rename, forget, prefer, agent on/off, bind a model and a job),
ANDON (one tile per unit - one word, one colour, one number - the rack in two seconds),
TROUBLE (the standing calls, priority first, each with why it matters and what to do; `k`
says ROGER, `s` puts a call on stand by for an hour), GRANTS (issue and revoke), SESSIONS
(the traffic your Edge carried), HEALTH (temperature, memory, disk and carrier per unit, the
rules in force, and their editor). `roger edge health --live` opens HEALTH on its own.

The palette is the radio's own: magic-eye green, dial amber, dial blue-white, and the one
red - spent only on what needs a hand. `NO_COLOR` or `ROGER_REDUCED_MOTION` stills every
breather.

## Health and trouble

Every judgement records each unit's state once a minute (THE LOG), so a unit's row reads in
words with how long: `ON AIR 3h12m`, `DARK 41m`, `TROUBLE 7m`. Rules are a metric, a
comparison and a value, fleet-wide or for one unit; sensible defaults come out of the box
(a hot CPU, a nearly full disk, a unit dark past the liveness window) and can be turned off,
never removed. A rule fires once when crossed and clears once when it recovers past a small
margin; only the authority judges, so a dead unit still reports. `roger edge alert` lists the
rules and what is firing; `roger edge alert set|off|on|rm` edits them; a call standing over a
day is STALE; ten calls in ten minutes is a PILE-UP; the SIGN-OFF line sums the shift.

## The trouble feed, for the Roger app

Alerts go to your phone, privately. The Edge works without internet, so nothing is pushed
through a cloud: your phone is a member holding a certificate this authority issued, and it
pulls `GET /edge/trouble` from the machine's face over mutual TLS - the standing calls, what
changed since it last looked, the rate and the sign-off - raises its own notifications, and
answers back: `POST /edge/trouble/roger` acknowledges, `POST /edge/trouble/standby` quiets a
call for up to eight hours, each attributed to the phone by name. A stranger, a certificate
from another authority, or a revoked member gets nothing about the fleet. Nothing ever leaves
the machine unless a member asks. `roger edge trouble [<node>] [--watch]` reads any member's
feed from a terminal the same way. Pinned by features/edge/trouble_feed.feature and
features/edge/health.feature.

## The serving probe

An instance's `serve` was CLAIMED until "the serving probe" passed, and outside a broker-registered
station nothing ever ran that probe: on an Edge with no broker every peer stayed CLAIMED, so the peer
ladder had no rung, `roger use <node>` always refused, and Ask never lit. Now the face answers
`GET /edge/probe?band=<band>` to members: it asks the server behind that band for its model list and
passes only when the server is up and lists it. No token is spent and nothing is receipted. Discovery
runs it on each instance it has just certificate-verified whose serve is still CLAIMED, and the host
runs the same check on its own household through its own upstream map. The peer's own word still
counts for nothing. Proposed in features/edge/serving_probe.feature, pinned by Go tests.

## Inventory: what each unit runs, and who is behind

Every fleet manager shows the agent version, the OS, the kernel, the architecture and the hardware
per device, and flags the devices that are BEHIND. Roger Edge showed resources but not one software
fact. Now each running roger records what it IS beside what it has: its version, the OS pretty name,
the kernel release, the architecture, the hostname and the hardware model where the machine names
one (a Jetson, a Pi, a DMI product). Nothing is invented: a fact the machine does not expose is
absent. The newest roger on the Edge is the reference, and an older one reads "behind 6.11.2" on
every surface. `roger edge inventory [--json]` is the table; the fleet screen's readout carries a
SOFTWARE row and an UPDATE row, the console's node card carries the same line under each instance,
and `roger edge describe` prints it. Proposed in features/edge/inventory.feature.

## Tags, and rules that cover a group

An Edge had two scopes for an alert rule: the whole fleet, or one node. "Warn me when any
PRODUCTION box runs hot, and leave the bench alone" could not be said. A node now carries up to
twelve tags - the owner's own words, lowercased, never read from the node itself and never a
permission. `roger edge tag <node>` shows them, `+word` adds, `-word` removes, a bare list replaces,
`--clear` empties. `roger edge list --tag production` lists the group, and the fleet table grows a
tags column only when something is tagged. An alert rule may be scoped to `tag:<tag>`, and the rule
in force for a node is its own, then the TIGHTEST rule on any tag it carries, then the fleet rule -
so a bench group can never quietly raise a production limit. The tags show on the fleet screen's
readout, the console card and the terminal's node detail. Proposed in features/edge/tags.feature.

## Maintenance: starting an agent from another machine

Monitoring without acting is half a fleet manager: the screens said a unit's agent was offline and
the owner still had to walk to the shed. This is the first remote action, and the smallest that
closes the loop - START an agent the owner already marked persistent on that machine.

Nothing here is reachable unless the owner did both of these, on two machines. On the TARGET,
`roger edge maintenance on`, which is off by default and without which the machine declares no
maintain capability and refuses every maintenance invoke, grant or not. On the CALLER, an
authority-signed grant scoped to `maintain` and to that node - a grant for operate does not reach
maintenance, because running a turn and starting a process are different powers. On top of those:
mutual TLS, the caller matched to its certificate, revocation applied from the local authority
store, one action at a time, and the action on the node's own history naming who asked.

It cannot run a command, a script or a shell. The only thing it starts is a roger the owner already
named there. Stopping, restarting and rebooting are refused by name, so nothing pretends they exist.
`roger edge maintain <node> start <agent>` is the arrow, and the fleet screen says which units have
maintenance on. Proposed in features/edge/maintenance.feature; it needs the founder's approval
before any machine is left with the switch on.

## The events timeline

An operator who finds a unit in a strange state asks what happened to it and when. The Edge recorded
plenty - enrolled, seen, went dark, renamed, re-pinned, probed, tagged, capabilities changed,
maintenance done - and the alert log recorded every crossing, but the two lived apart and neither
could be read across the fleet: `roger edge describe` showed the last five lines of one node and that
was all. Now there is one timeline, merged from both, newest first, saying when, which unit, what and
the detail. `roger edge events [--node <node>] [--since 2h] [--limit <n>] [--json]`, and an EVENTS
tab on the fleet screen where a crossing is drawn in the alarm colour and a node event quietly. It
records nothing: it is a read.

Two things came out of building it. Fourteen places appended to a node's history and nothing ever
trimmed it, so a node on a flaky network grew a record that is re-read on every command; history is
capped at the newest 200 events per node, in the one write path. And the tab strip now gives up room
before a fact does: a seventh tab pushed the alert count off a 100-column window, so a narrow
terminal shortens the inactive tab names and keeps the count. Proposed in features/edge/events.feature.

## The console

The console's EDGE tab carries the same floor: Andon tiles, the Trouble list with roger and
stand by, the sign-off line - worded on the machine, so the browser infers nothing.

## Also

- A forget or rename made from any screen or a second terminal is never undone by the running
  TUI's next save.
- The alert log's files are written under one lock, so a roger pressed while the host is
  judging is never lost.
- `roger edge health --json` keeps an absent metric absent, never a zero.
