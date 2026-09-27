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

## The console

The console's EDGE tab carries the same floor: Andon tiles, the Trouble list with roger and
stand by, the sign-off line - worded on the machine, so the browser infers nothing.

## Also

- A forget or rename made from any screen or a second terminal is never undone by the running
  TUI's next save.
- The alert log's files are written under one lock, so a roger pressed while the host is
  judging is never lost.
- `roger edge health --json` keeps an absent metric absent, never a zero.
