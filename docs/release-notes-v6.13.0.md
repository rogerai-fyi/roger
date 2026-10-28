# v6.13.0 - the rack: your Edge as a fleet you can see, and act on

Roger Edge grows a fleet manager. This release is the machine side: the screens, the
health rules and the alerts, and a private feed the Roger app on your phone pulls from.
Then it grows the rest of a fleet manager: an inventory, tags and group rules, a timeline, a
redacted log tail, and the actions that start, stop and restart a roger on another machine.

Draft. v6.12.0 was already cut, so this is numbered v6.13.0; confirm that when the release is made.

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

## The serving probe records WHICH bands it proved

A boolean could only say "something answered", so an instance advertising two models was marked as
serving both when one had been proven - and the fleet would dispatch it the band it could not serve.
The record now keeps the bands the probe actually answered for. Only those route, a subset of them
needs no fresh probe, and a band outside them still does. That also ends a churn this machine really
has: an on-air set that gains and drops a band from pass to pass used to re-prove itself every
thirty seconds, and now holds its verdict. A record written before the probe kept a band list falls
back to what the instance advertises, so an older Edge routes exactly as it did.

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

It cannot run a command, a script or a shell. The only processes it touches are rogers the owner
already named there. The actions are START one that is down, STOP one that is running, RESTART one,
and LOGS, the redacted tail. Rebooting and shutting a machine down are refused by name, so nothing
pretends they exist.

A stop signals nothing it cannot vouch for, because signalling by number is how a process id gets
reused under you. It signals only when the registration naming the pid is still beating, the pid is
not the process answering the request, and the pid's own command line still names a roger. It sends
SIGTERM and never SIGKILL: a roger that will not leave is reported, not forced. A restart that finds
the roger still there does not start a second one on top of it, and says so rather than reporting a
restart that did not happen. A start waits briefly and says whether the agent really joined, not
merely that it was launched. `roger edge maintain <node> start|stop|restart <agent>`, and the fleet
screen says which units have maintenance on.

On a machine running several rogers you can now address the one you mean: `roger edge maintain
<node>/<roger> stop <agent>`. A node's recorded face is whichever roger observed itself last, so a
request for the machine could land on the very roger you named and be refused as itself. Each roger
records its own face port now, and an instance address dials that - the same node, the same
certificate, so the pin is unchanged and nothing new is trusted. A roger the node does not run, or an
older one that reports no face of its own, is refused by name rather than quietly sent to the machine.
"." and "self" mean this machine there too, so `./roger` is how you address one roger on the machine
you are sitting at.

Running the actions for real found three things tests had not. A spawned agent inherited this
process's pinned face port, so it could never bind and exited at once while the owner was told it had
launched; a child is given its own face now. Nothing ever waited for that child, so an agent that
died left a zombie for the life of the roger that spawned it; children are reaped, and one that dies
within seconds is logged. And the roger that may never be a target is the one this process
REGISTERED, not the name a new roger would take - guessing it refused a start for the very agent the
owner had named.

## The log tail, with secrets removed

The feature an operator reaches for most: a unit is behaving oddly and you want its last hundred
lines without walking to it or opening a session. A roger now keeps its own recent lines in memory -
a tee on the standard logger, so the lines still reach wherever they went before - and a member can
read them with `roger edge logs <node> [--lines N]`.

Redaction is the whole risk, and it happens on the serving side, on the way out. Log lines on this
fleet really do carry credentials: an upstream key sits on the command line of every share process on
this machine today. Before a line leaves the process, API keys by their shapes, bearer tokens,
anything after key=, token=, secret=, password= or authorization:, and any long high-entropy run are
replaced by a marker that says something was removed. Because it happens on the read, it also covers
lines logged before any of this existed, and no flag asks for the raw text. An ordinary line survives
whole, which is pinned by tests.

It opens no new authenticated path. The tail is a maintenance action on the existing invoke path, so
it inherits the mutual TLS, the grant's signature, the revocation check, the caller match, the
capability scope, the owner's switch and the replay cache, and adds no authorization code of its own.
It is not a file read either: the only thing served is the process's own ring, so no path comes from
the caller. Proposed in features/edge/logs.feature.

## Schedules: an action left standing

The last of the list: the ad-hoc maintenance an operator wants standing rather than remembered.
"Restart the watcher on every production box each night." `roger edge schedule add restart watcher
--scope tag:production --every 24h`, or `--once` for a job that runs at the next opportunity and
retires itself. The list shows each job, its scope, its cadence, when it last ran and what came of it,
and a run appears on the events timeline against the unit it touched.

A schedule is only a clock in front of what the owner could already do by hand, and never a way round
it. A run on another machine takes exactly the path a typed maintain takes: that machine's own
maintenance switch, an authority-signed grant scoped to maintain and that node, the mutual TLS, the
caller matched to its certificate, the revocation check, the replay cache. A grant that expires or a
switch turned off stops it dead. A run on the machine the schedule lives on is done locally, with no
face and no grant, because it is the owner's own machine and it is the same thing `roger edge agent
resume` does at the keyboard. Either way, nothing is pushed to anybody and no new path exists.

One roger per machine runs the schedules, the first live instance by name. Every roger makes the same
pass, and if each ran them they would race for a job and take turns being the target of their own - a
live run did exactly that. When the runner's own name IS the target the refusal says which roger tried,
so the reason is on the page rather than guessed at.

The failures got as much attention as the happy path. A run that cannot be made is recorded and waited
out to the next cadence, never retried in a tight loop. A unit with no face is recorded as dark rather
than failed, because nothing was wrong with the job. A refusal is recorded in the words the face used.
One run at a time, a cadence floor so a schedule cannot become a hammer, and a fleet-wide job never
targets the machine it runs on. A log tail is not schedulable: a tail nobody reads is only a copy of
somebody's log lying around.

## Say what the fleet should look like

The half of a fleet manager Edge still lacked: a desired state, and a drift report. An owner can
expect four things of a unit, each a fact the fleet already collects over that node's own certificate,
so none of it needs new data or a new path: a roger version floor, a band it should have on air, that
it runs an agent, and that maintenance is on there. Each is scoped like an alert rule - the whole
fleet, a tag, or one node - and the most specific scope wins, with the tightest of two groups applying
so a group can never loosen what another asked.

`roger edge expect` says what is expected and how many units differ; `roger edge drift` names each
unit, what was expected and what is there. A unit selected on the fleet screen carries a DRIFT row.

Two honest boundaries. UNKNOWN IS NOT DRIFT: a member that has never been dialled reports no software
and no instances, and "we have not heard" is not "it is wrong" - it reads as unknown and is not
counted. And nothing here writes another machine's configuration: the fleet reports, and the owner
closes the gap. Making a member pull the intended state and apply it to itself needs a distribution
channel and is its own slice, which the spec says plainly rather than implying it exists.

## Console parity

The browser tab carries what the terminal shows: the events timeline as its own list with a run of the
same thing read once and counted, a mark on a node whose owner turned maintenance on, what this Edge is
expected to look like with how many units differ from each, where it does not hold, and what has been
left standing with its last outcomes. The words are chosen on the server, as the rest of the tab's are,
so the two surfaces cannot tell the owner different things. An unknown is marked as unknown there too,
never painted as a wrong.

One honesty fix that came out of it: on a machine that is not the authority, an empty set of
expectations does not mean the owner asked for nothing. It says this machine holds none and that they
are kept where they were typed, rather than claiming nothing is expected of the Edge.

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
