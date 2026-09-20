# Roger Edge - design

Status: DESIGN. Founder rulings of 2026-09-11 recorded inline. Nothing in this document
describes shipped behavior unless the "What exists today" table says so.

## 1. What Roger Edge is

Roger Edge is the layer that makes the machines you already own one addressable fleet.

Whatever runs `roger` is a node on your Edge: the workstation serving a model, the Pi on the
bench, the Jetson in the cabinet, the ESP32 next to the sensor, the Mac in the other room. A
node joins once, is named, declares what it can do, and from then on it is reachable and
controllable from a single view - a `roger edge` command surface, an Edge screen in the TUI,
and the same topology in the web console.

Two sentences separate this from a deployment tool. **A node does not have to host a model to
belong**: a sensor that only produces readings and a relay board that only accepts commands are
first-class members. And **the point of membership is coordination**: your models and agents
address the fleet, so a classifier on a board can escalate to a Wave model, and an agent can
act on a device, through the same authorized path that carries a relay today.

The microcontroller task-model line - a small classifier distilled from Wave Pico, quantized to
int8, living in a board's flash - is one product inside this layer, not the whole of it. Roger
Edge detects; Wave reasons.

## 2. Why this is not a second network

RogerAI already runs a relay fabric: Stations dial out to a broker, Towers are self-hosted
relays with enrollment, certificates, admission and signed inventory, and every relayed request
is authorized, metered and receipted. Roger Edge is a **new addressing and control surface over
that same fabric**, not a parallel one.

Concretely, it reuses:

| Existing thing | What Edge uses it for |
|---|---|
| The dial-out tunnel (`node-dials-out long-poll`) | Reach. A device behind NAT needs no port forwarding and no VPN. |
| Tower enrollment + PKI (`internal/towercore/{enroll,cert,admit}`) | Identity. A device joins the way a Tower does. |
| Grants (`internal/store/grant.go`) | Authority. An agent acting on a device presents a scoped grant. |
| Receipts + the ledger | Audit. A device action is accountable the same way a relay is. |
| `internal/localplane` | The LAN-facing plane, already bindable to private ranges. |

The founder's ruling on transport: **tunnel now, transparent proxy later.** Reach and control
ride the existing dial-out tunnel first, because it works through NAT, needs no root, and works
on macOS and iOS - platforms a kernel-level mesh cannot reach. A transparent per-connection
proxy, so that unmodified software can address a peer by name, is a later opt-in for hosts that
want it, not a prerequisite for the fleet.

## 3. The node model

A node is identified by a keypair it generates itself and an enrollment signed by the account
that owns it. It is addressed by a short name the owner chooses, and it declares capabilities.

```
node
  id            stable, derived from the node's public key
  account       the owner; every node on one Edge shares it
  name          owner-chosen, unique within the account
  kind          host | board | mobile
  capabilities  a declared set, verified where verifiable
  transports    how it can be reached, in preference order
```

Capabilities are the vocabulary the rest of the system routes and reasons on:

| Capability | Meaning | Verified how |
|---|---|---|
| `serve` | Hosts a model and can take relayed inference | The existing serving probe |
| `classify` | Runs a task model locally against a fixed label set | A canary sample with a known label |
| `sense` | Produces readings on a schedule or on demand | A read returns a well-formed sample |
| `actuate` | Accepts commands that change the world | Declared only; never inferred |
| `relay` | Forwards for nodes that cannot be reached directly | Reachability from a second node |
| `operate` | Runs an agent that can act on other nodes | Declared, and gated by grant |

`actuate` is deliberately never inferred. A node that can move something says so explicitly, and
the owner confirms it at enrollment, because the cost of a wrong guess is physical.

## 4. Enrollment

One path, three ergonomics. The differences are how the secret crosses to the device, not what
the device ends up holding.

0. **The authority comes first.** An Edge has exactly one root, and the owner chooses where it
   lives: Core (the default, zero setup, one online moment per node) or a machine the owner
   designates as a LOCAL authority (the airgap answer, where Core is never contacted at all).
   One certificate shape and one verification path either way, so a node can never tell which
   kind signed its peer. Founder ruling 2026-09-12: an Edge must be able to form on a network
   that has never had internet, so the local authority is a first-class path, not a fallback.
   The local authority is Core-free BY CONSTRUCTION, enforced by a dependency-graph test, the
   way `roger-tower-local` already is.

1. **Self-enroll** - the node runs `roger` and the owner is logged in there. It generates keys
   and signs its own join with the account key it already holds. This is the `roger share`
   self-attach pattern that already exists.
2. **Token enroll** - for a device that cannot hold an account key. The owner mints a
   short-lived, single-use, capability-scoped token on a machine that is logged in, and the
   device presents it once to trade for its certificate.
3. **Flash enroll** - for boards. The token is written into the image at flash time so the
   board is a member on first boot and never ships a shared secret.

Rules that hold in all three: the device generates its own private key and never transmits it;
the join is scoped to one account; the certificate carries the account and node identity so a
node from one account can never be addressed by another; and enrollment is revocable from any
logged-in surface.

## 5. Discovery

Two ways a node is found, in this order.

**On the LAN, directly.** Nodes advertise over mDNS as `_rogerai._tcp` with TXT records naming
the node id, the account, the declared capabilities and the fingerprint of the certificate the
node will present. The fingerprint is the point: mDNS is unauthenticated, so the advertisement
is a hint about where to look, never proof of who is there. A LAN connection pins the expected
certificate and fails closed on a mismatch.

**Through the relay, always.** A node that is not on this network, or is behind a NAT that
mDNS cannot cross, is reached over the tunnel it already holds open to a relay. This path has
no discovery problem because the relay knows which nodes are connected to it.

LAN discovery is an optimization for latency and for working with no internet at all. It is
never the only path, and the fleet view is identical either way.

## 6. One protocol, two encodings

An ESP32 and a Jetson speak the same message set. They do not speak the same bytes.

The failure mode being avoided is concrete and observable in the market: a product that gives
microcontrollers their own protocol ends up with a host tool that dials the wrong port at the
wrong device class and rejects its own factory certificate, and the boards become second-class
citizens nobody can enroll. So the contract here is a single schema and a single command set,
with an encoding chosen by what the node can afford:

- **Hosts** (Linux, macOS, anything with a real TCP stack) carry the schema as JSON over the
  existing tunnel, which is what the relay already speaks.
- **Boards** carry the same schema in a compact binary framing, sized for kilobytes of RAM.

Commands, in the smallest set that supports the fleet:

```
hello        identity, capabilities, versions          every node, on connect
heartbeat    liveness plus a little health              every node, periodically
describe     the node's detail for the fleet view       on demand
read         take a sample from a `sense` node          on demand or scheduled
invoke       run a declared action on an `actuate` node authorized, audited
classify     ask a `classify` node for a label          on demand
escalate     hand a sample up to a Wave model           board to broker
deploy       place an artifact on a node                later, spec of its own
```

`escalate` is the line that matters. It is how a board that cannot name what it is seeing gets
an answer from a model that can, and it is the reason this layer belongs to RogerAI rather than
to a deployment tool: the escalation lands in a market that already knows how to price, route
and receipt it.

## 7. Control, and the authority behind it

An agent acting on the fleet is the whole point, and it is also where an edge product is most
likely to do something regrettable. The rule here is that **a device action is an authorized
call, not a local socket**.

Every `invoke` carries a grant: scoped to an account, to named nodes, to named actions, with a
rate and an expiry. The node verifies the grant chain before it acts. The action is receipted.
Revoking the grant stops it everywhere, immediately.

This is deliberately stricter than the prevailing pattern, where an on-device agent is handed a
local admin socket whose only gate is an entitlement declared by the app itself, with the
accepted consequence that a badly-prompted agent can brick the device. RogerAI already has
grants, scopes and signed requests, so the strict version costs nothing to adopt and is the
default.

## 8. The fleet view and the topology

The Edge screen answers one question at a glance: what is on my Edge, and how is it connected?

It draws a graph. Nodes are boxes with a name, a kind glyph and capability marks. Edges are the
transports actually in use: a solid line for a LAN-direct link, a dimmer line for a path through
a relay, and the relay itself drawn as a node so a hop is visible rather than implied. Liveness
animates - a heartbeat pulses along the edge it arrived on, so a healthy fleet is visibly busy
and a silent node is visibly silent.

It renders in a terminal, so it obeys the same constraints the rest of the TUI does: it is
honest at 80 columns, it never claims a link it has not seen, and a node whose last heartbeat
aged out is drawn dark rather than dropped, because a fleet that silently loses members is worse
than one that shows a dead box.

The same model feeds the web console. Apple platforms come after both.

## 9. Naming, resolved

"Edge" currently means three unrelated things in this codebase, which is a trap for anyone
reading it later:

| Today | From now |
|---|---|
| Tower **edge dispatch** - the routing path where a Tower self-attaches Stations and edge attempts are placed (`toweredgeattach.go`, `edgeload.go`) | Keeps its internal name, becomes **never user-facing**. User-visible strings say "relay dispatch". |
| `towercore/fleet` - a read model of which Stations are routable right now, for supply routing | Keeps its name. It is a **supply** view, account-agnostic. |
| **Roger Edge** - the microcontroller task-model line, per the website | Becomes the **layer**. The classifier line is one product inside it. |
| (new) **Edge fleet** - the account-scoped set of a given owner's nodes | The thing the Edge screen and `roger edge` show. |

The two "fleet" notions are genuinely different and both earn their name: one is "whose supply
can serve this model", the other is "what machines do I own". They are never joined.

## 10. What exists today

| Piece | State |
|---|---|
| Relay fabric, dial-out tunnel, market, receipts | Shipped |
| Towers: enrollment, certificates, admission, signed inventory | Shipped |
| Stations serving models, `roger share`, `roger tower` | Shipped |
| Grants with scope, rate and expiry | Shipped |
| `internal/localplane` LAN-bindable consumer plane | Shipped |
| LAN discovery of roger instances (mDNS) | Shipped on `wt/roger-edge` |
| A node class for devices that host no model | Shipped on `wt/roger-edge` |
| Capability declaration and verification | Shipped on `wt/roger-edge` |
| Enrollment: self-enroll, Core or local authority, revocation | Shipped on `wt/roger-edge` |
| The Edge message set and its board encoding | **Not built** |
| `roger edge` CLI surface | Shipped on `wt/roger-edge` |
| Edge screen and topology graph in the TUI | Shipped on `wt/roger-edge` |
| The session layer: sessions drawn on the graph, attributed, counted, faded | Shipped on `wt/roger-edge` |
| A classifying node's contract (task class, fixed framing, label set) on the record | Shipped on `wt/roger-edge` |
| Sessions recorded from `roger use` and the guest-operator proxy, attributed and mirrored across processes; `roger edge sessions` | Shipped on `wt/roger-edge` (`features/edge/session_attribution.feature`) |
| Edge view in the web console (EDGE tab: graph, list fallback, detail, adopt, sessions; console chat turns recorded as sessions) | Shipped on `wt/roger-edge` |
| The empty Edge as a STATUS (THIS MACHINE / AUTHORITY / DISCOVERY + both ways to add a node) on TUI, console and `roger edge`; an unstarted host is never drawn as "the only node" | Shipped on `wt/roger-edge` (`features/edge/empty_edge.feature`) |
| Microcontroller firmware, any on-device classifier artifact | **Not built** |

## 11. Order of work

1. **Discovery and the node record.** LAN discovery with certificate pinning, and the
   account-scoped node record with capabilities. Nothing is controllable yet; the fleet becomes
   visible. (Specs: `features/edge/discovery.feature`, `features/edge/node_identity.feature`.)
2. **The Edge screen.** The topology graph over whatever discovery found, plus relay-connected
   nodes the broker already knows about. (`features/edge/topology_view.feature`.)
3. **`roger edge` CLI.** `list`, `describe`, `name`, `forget`, and the read-only half of the
   fleet. (`features/edge/cli.feature`.)
4. **Enrollment.** The three ergonomics over one certificate path.
   (`features/edge/enrollment.feature`.)
5. **The message set.** `hello`, `heartbeat`, `describe`, `read`, `classify`, `escalate` on the
   host encoding. (`features/edge/protocol.feature`.)
6. **Control.** `invoke` with grants and receipts. (`features/edge/control.feature`.)
7. **Boards.** The compact encoding, then firmware, then a classifier artifact. Each is its own
   spec and its own decision to make.

Steps 1 to 4 are BUILT and green on `wt/roger-edge`, and so is the SESSION LAYER of section
12 (`features/edge/sessions.feature`, approved 2026-09-12: 27 scenarios green). Step 5 (the
message set, `features/edge/protocol.feature`) and step 6 (control, `invoke` with grants,
`features/edge/control.feature`) are still to be written and built - the session layer draws
what those paths will produce, and needs neither of them to draw what the relay already
produces today. Step 7 (boards) is each its own decision.

## 12. Sessions, agents, and the escalation chain

Founder direction 2026-09-12: work out how `roger use`, the TUI agent and device traffic all
appear on the Edge, because an agent session is itself a connection, and the Playbox is the
picture of how the factory and the models are meant to work in unison.

### 12.1 A node is a thing. A session is a thing happening.

The fleet so far has only nouns: machines and devices. That is half the picture, and it is the
static half. What an owner actually wants to see is **who is talking to whom right now**.

So the Edge has two kinds of object:

- **Participants** are the nodes already specified: machines, boards, relays. They persist.
- **Sessions** are live exchanges between participants. They appear, carry traffic, and end.

A session is drawn as motion along the path it really took, never as a line between two boxes
that merely could talk. This is the same rule the topology already obeys for links, applied to
traffic: **the graph shows what happened, not what is possible**.

### 12.2 Every consumer is the same shape, including the agents

The insight that makes this tractable: `roger use`, the TUI's own agent, a guest operator like
opencode or hermes, the web console, and a sensor escalating a reading are **not five different
things**. Each is a participant opening a session against a band, which some station serves.

| Initiator | What it is today | On the Edge |
|---|---|---|
| `roger use` | a local OpenAI-shaped endpoint through the broker | a session from this node to the serving station |
| the TUI agent | a turn on the tuned band | a session, one per turn |
| a guest operator | opencode/hermes/aider through the local proxy | a session, attributed to the guest |
| the web console / Playbox | a browser-session relay caller | a session, attributed to the browser identity |
| a device escalating | a board that cannot name what it sees | a session, from the board, to a band |

They differ in **who initiated** and **what authority they carried**, never in shape. That is
why one view can hold all of them, and why the same receipt already covers them all.

### 12.3 Devices reach models through the contract, not through a special case

The Playbox already settled how a device and a model fit together, and Roger Edge must not
invent a second answer. From the approved `features/web/playbox_edge_honesty.feature`:

- Wave models are **contract models**: the device prompt is part of the device. Unframed they
  floor; framed they perform. **Model and prompt ship as one unit.**
- **ESCALATE is the models' strongest measured skill** and renders as a good outcome, never as a
  warning state.

So a node declaring `classify` carries its contract: the task class, the fixed framing, and the
label set it may answer with. When it cannot name what it sees, it escalates, and escalation is
a success path. On the Edge that is one session from the board to a band, and the topology draws
it travelling up the chain rather than hiding it inside the device.

The Wave Mesh ladder from `features/web/playbox_mesh_workbench.feature` is the real chain, not a
simulator conceit: a window passes through the seated tiers, each inspects, and **response
routing happens after the finding**, never instead of it. Roger Edge is what makes that ladder
real outside the browser: the board is the first rung, the escalation is a relayed session, and
where the finding goes (log, human review, policy queue) is routing, which never rewrites the
model's answer.

### 12.4 What the Edge screen shows once sessions exist

The topology gains a second layer over the same graph:

- An **active session** animates along its real path: board to relay to station, or this node
  straight to a LAN peer.
- A session is labelled by its **initiator and its band**, so "the agent is asking gpt-oss-120b"
  and "the bench sensor escalated to Wave Nano" read differently at a glance.
- An **escalation** is drawn as a good event, in the positive style, because the approved
  framing says it is the right call rather than a fault.
- A session that **failed over** shows the station it left as well as the one that served, since
  the failover already writes both receipts.
- Sessions are **ephemeral**: they fade rather than accumulate, and a still graph means a quiet
  fleet, exactly as the heartbeat rule already promises.

### 12.5 Authority is unchanged, and that is the point

Nothing here invents a new permission. A session is authorized the way traffic already is: a
consumer session against a band spends from a wallet under the existing limits, and a device
`invoke` carries a grant scoped to nodes and actions. The Edge view is a **window onto authorized
traffic**, never a new way to cause it. Anything visible in the topology was already receipted.

### 12.6 What the session layer records today, and what it does not

The layer is BUILT and its 27 scenarios are green. What it draws is derived, never bookkept:
one `edge.Sessions` ledger per account takes `edge.Traffic` - a request id and the relay's own
`protocol.UsageReceipt`s - and reads the band off the receipt's model, the station that served
off the last un-voided receipt, the station a failover LEFT off the first voided one, and a
refusal off a $0 `VoidReason` receipt. With no receipt there is no session, so there is no code
path from the view to a relay.

ONE initiator is wired to real traffic today: the TUI's own turns. `internal/client` already
decoded the broker's `X-RogerAI-Receipt` for the reply footer and discarded it; it now carries
it on `ChatResult`, and `recordEdgeSession` turns it into a session. Proven live against
production on a free band.

`roger use` and the guest operators are wired too (2026-09-17, `features/edge/
session_attribution.feature`): the local proxy hands each relayed response's receipt to one
optional callback (`ProxyOptions.OnReceipt`) - from the `X-RogerAI-Receipt` header on a
non-streamed reply, and from the `: rogerai-receipt=` SSE comment the broker now emits at a
settled stream's end beside its cost comment (a stream's headers flush before any output, and
guests stream by default) - and the surface that owns the proxy decides the
attribution, never the proxy: the TUI's endpoint attributes to the GUEST'S NAME while a guest
holds the mic (exec to return) and to `roger use` otherwise; the `roger use` process attributes
to `roger use`. A session opened in one process reaches every Edge view on the machine through
the session MIRROR: each recording ledger publishes its live list to its own private file under
`<config>/rogerai/edge-sessions/`, and a viewer merges its same-account siblings - no shared
memory, no locks, nobody writes another process's file, receipts never written, the same 90 s
life, and a dead process's file removed by the first reader that finds it faded. `roger edge
sessions` (and `--json`) is the CLI's window on that same mirror, so a shell with no TUI open
still sees what the machine is carrying.

### 12.7 Pushing Roger Edge

Getting the software onto a thing has three shapes, and only the first exists today:

1. **Hosts** already have it: `roger` ships through the existing packaging, and a host joins by
   enrolling. Nothing new is needed.
2. **Single-board Linux** (Pi, Jetson) is the same binary and the same enrollment. What is
   missing is a documented path and a service unit, not a mechanism.
3. **Microcontrollers** need a firmware image, and that is the only genuinely new build. Flash
   enroll (section 4, path 3) exists precisely so a board is a member on first boot without ever
   shipping a shared secret.

Updates follow the same order: a host updates itself the way `roger` already does, a board needs
a signed image and a way to fall back if it does not come up. That rollback story is a spec of
its own and is not attempted here.

### 12.8 A naming conflict to settle

`features/web/playbox_edge_honesty.feature` is approved and states: "Wave Nano (350M) is the
trained gateway-class brain; **Roger Edge is the MCU classifier line** with no trained artifact
yet." The 2026-09-11 ruling promotes Roger Edge to the whole layer, with the classifier line as
one product inside it.

Both cannot stay literally true. The Playbox text is not wrong about the artifact - there is
still no trained classifier - but it now names the layer when it means the line. This wants a
small, deliberate correction to that spec's wording rather than a silent drift, and it is listed
here so the two do not quietly disagree.

## 13. The fabric: instances, local-first, and agents that can call each other

Founder direction 2026-09-19, verbatim: *"reach roger instance should be able to be added to
roger edge ... i want to be able to register roger on the edge, and now it's part of the edge
network, always being local by default so we should have a label clearly specifying the mode ...
say on this pc i have 2 or 3 roger instances, each with a different name, but same node ... so
the raspberry pi agent can use or communicate to the agents and ask for inference or use its
agents in some way ... i should be able to use other edge inference from any of them"*.

Sections 1 to 12 built a fleet you can SEE. This section is the fleet you can USE. It is four
moves, and they are one move: the Edge stops being a picture of the machines you own and becomes
the fabric they talk over.

### 13.1 What was wrong with the model

**A node was a machine, and that was the whole model.** One machine, one certificate, one place
on the graph, one set of capabilities. But a machine does not serve a model or run an agent. A
PROCESS does. The owner's mental model is already the right one: "on this PC I have 2 or 3 roger
instances, each with a different name".

The tree already half-knows this and the halves disagree:

- `internal/edge` calls a MACHINE a node, identified by a key it holds
  (`internal/edge/node.go`, `edgeauth.NodeID`).
- `internal/node` calls `<station>-<model>` a node id (`controller.go:586`), which is a SUPPLY
  identity: one per model a process is broadcasting.
- `internal/onair` already arbitrates between two roger processes on ONE machine, keyed on that
  supply id, and its own comment says "THE LOCK IS WHY MULTIPLE INSTANCES ARE SAFE"
  (`controller.go:961`).

So multiple instances per machine is not a new idea to be introduced. It is an existing
situation the code handles at the lock and nowhere else: the Edge cannot see it, cannot name it,
and cannot route to it. The voice-station work hit exactly this and logged it as a defect (a TUI
booth and a headless `roger share` on one machine fighting over one id, jobs black-holed). The
instance model is the fix, not an addition.

### 13.2 Node, instance, station

Three levels, named once and used everywhere:

| | what it is | how many | identity | lifetime |
|---|---|---|---|---|
| **Node** | a machine | one per machine | a certificate under the Edge's root | as long as the machine is enrolled |
| **Instance** | a running `roger` on it | several per node | a name the owner chooses, under the node | as long as the process runs |
| **Station** | an instance broadcasting one model | several per instance | `<station>-<model>`, the existing supply id | as long as that model is on air |

The capability vocabulary (serve, classify, sense, actuate, relay, operate) **belongs to
instances**. A node's capabilities are the union of its instances'. That is the correction the
whole section turns on: "this Jetson can serve" is shorthand for "a roger on this Jetson is
serving", and when that process exits the machine can no longer serve, which today the fleet has
no way to notice.

Addressing is the name when it is unique on the Edge, and `node/instance` when it is not.

**An instance is INSIDE its node's trust boundary, and gets no certificate of its own.** This is
deliberate and it is what keeps the ceremony at one per machine. An instance shares the node's
key because it is the same machine under the same owner: anything that could forge an instance
could already read the node's private half and BE the node. The node's LAN face reports which
instances it is running, over its own certificate, and that report is the only account of them
anybody believes - the same rule `describe` already follows for capabilities (section 4).

Registration is therefore not a ceremony. **A roger starting on an enrolled node registers
itself**, with a default name the owner can change, and deregisters when it exits. Joining the
Edge stays a per-machine act; being on it is per-process and automatic. The owner's ask, "I want
to be able to register roger on the edge", is answered by there being nothing to do.

### 13.3 Mode: two words that both mean "local"

The direction says "always being local by default so we should have a label clearly specifying
the mode". There are TWO things that both get called local and they must never share a label,
because one is about trust and the other is about where your prompt went:

- **ROOT** is what the Edge is rooted at: `LOCAL` (a machine the owner designated, section 4) or
  `CORE`. It answers "can this Edge form and run with no internet?"
- **ROUTE** is where one turn actually went: `local` (it stayed on the Edge, on hardware the
  owner owns) or `market` (it went out through the broker to a station that may be a stranger's).
  It answers "did this prompt leave the building?"

Every surface shows the ROOT in its header and the ROUTE on every session row. A fleet where
three turns went local and one went to the market says so, on the screen, without being asked.

**The default is local.** A new setting, `edge.prefer`, takes `local` (default: try the Edge
first, fall out to the market), `market` (the old behaviour), or `local-only` (never leave the
Edge). Under `local-only` a band no instance serves is an honest refusal naming what is missing,
never a silent trip to the market. That setting is the difference between believing your data
stayed home and being able to show it.

### 13.4 The ladder: your fleet is one inference pool

Today a turn has two possible fates: a model loaded in this very process, or the market through
the broker. There is nothing in between, which means a Pi and a Jetson on the same switch talk
to each other by way of the internet, and on an airgapped Edge they cannot talk at all. That is
the single biggest gap between what Roger Edge draws and what it is for.

Dispatch becomes a ladder, tried in order:

1. **This instance's own model**, if it has one loaded (exists today: `harness.LocalCompleter`).
2. **An Edge peer that serves that band** - an instance with `serve` VERIFIED, reachable
   LAN-direct, dialled over its own certificate and checked against its pin. No broker, no
   internet, no account lookup. Failover across peers before the rung is given up.
3. **The market**, through the broker, exactly as today - and only when the ROOT is Core-linked
   and `edge.prefer` is not `local-only`.

Rung 2 is new and is the product. It is what makes "I have Jetsons and Pis and this laptop" into
one pool instead of four lonely machines.

**Rung 2 moves no money.** The owner owns both ends: there is no counterparty, no hold, no fee,
no ledger entry. But the Edge's honesty rule ("everything drawn was already receipted",
`features/edge/sessions.feature`) still binds, so a local turn produces a **local receipt**:
the same shape as a broker receipt, signed by the SERVING NODE's key rather than the broker's,
cost zero, marked local. It exists for the view and for audit, never for settlement. This
introduces a second issuer of receipts and that is worth stating plainly rather than discovering
later. It cannot become a fee dodge by construction: rung 2 can only reach nodes enrolled under
this Edge's own root, which is to say machines the same owner already owns.

### 13.5 The four arrows

With instances addressable and rung 2 carrying inference, the last move is to let the members
ask each other for things. The capability vocabulary already anticipated it: `operate` is
defined as "runs an agent that can act on other nodes" and its verification method is already
written as "declared, and gated by grant at use time" (`internal/edge/node.go`). The endpoint
for it was reserved, not forgotten: `internal/edge/server.go` says describe is "the one endpoint
the LAN face exposes at this stage ... being asked to DO something is protocol.feature and
control.feature".

Four things can now happen over one fabric:

| | arrow | example |
|---|---|---|
| **escalate** | device to agent | a gate camera cannot name what it sees and hands the reading up (built, section 12) |
| **infer** | anything to a model | the Pi asks the Jetson's band for a completion (13.4) |
| **delegate** | agent to agent | the laptop's agent hands a long job to the workstation's agent, which has the tools and the disk |
| **act** | agent to device | an agent asks a board for a reading, or to actuate, under a grant |

The point worth writing down, because it is the whole claim: **these are not four mechanisms.**
One addressing scheme (node/instance), one trust root (the Edge's certificate), one authorization
object (a grant), one evidence shape (a receipt), one view (a session drawn on the graph). Most
stacks need a different answer for each row of that table.

The grant record needs no new fields to carry this. `store.Grant` already has `Nodes`, `Models`,
`Free`, `ExpiresAt`, `Revoked`, rate limits and caps, and a `Self` flag documented as "owner's
own boxes/agents; always $0" - which is precisely this case, written before there was a fabric
to use it on.

### 13.6 The rules that keep a fabric from becoming a botnet

A network where agents invoke agents needs its refusals specified before its features:

1. **Delegation does not launder permission.** A task arriving from a peer runs under the grant
   it carries and the receiving instance's own rules, intersected. It can never do something the
   caller could not have done, and never something the receiver would refuse locally.
2. **Actuate keeps its owner confirmation.** An agent may ask a board to act; the board still
   requires the owner's standing confirmation for that capability. A delegated task is not a
   second way in.
3. **No transitive delegation unless the grant says so.** If A delegates to B, B may not delegate
   onward by default. Amplification is the failure mode that turns a helpful fleet into a loop.
4. **Every task carries its origin and a hop count**, and an instance refuses a task whose chain
   already names it. Cycles are refused at the node, not detected by a human later.
5. **Every invocation is receipted and drawn**, including the refusals. A refused invoke is
   information; a silent one is a hole.
6. **A locally-rooted Edge contacts Core for nothing**, dispatch included. The structural test
   that proves the local authority links nothing that can reach Core (section 4) extends to the
   dispatch path.

### 13.7 What this contradicts, and what needs re-approving

Two approved things collide with this and must be settled deliberately rather than drifted past:

- **`features/edge/topology_view.feature`** specifies today's screen: rows with wires, one node
  per line. The direction asks for something "more ux friendly and novel ... like a game or
  something". A redesign supersedes those scenarios, so that spec needs re-approval rather than
  quiet replacement. The proposal is in 13.8.
- **`internal/edge/server.go`'s comment** that describe is the only endpoint the LAN face will
  expose "at this stage" is correct as written and stops being true here. It should be updated
  with the new endpoints rather than left to read as a promise that was broken.

### 13.8 The screen: a patch bay, not a diagram

The Edge screen is a list with wires drawn on it. What the product has always been, in every
other surface, is a radio station: bands, stations, tuning in, the desk, the mic, patching a
guest through. The Edge screen should be the room those words come from - **a patch bay with
signal meters** - and that is also the answer to "novel, like a game":

- **The rail**: this instance is the desk on the left. Every node is a horizontal strip.
- **Instances hang under their node**, indented, each with its own marks, so "2 or 3 rogers on
  this PC" is a thing you can see at a glance.
- **The wire** between a node and the desk keeps its earned texture (solid LAN-direct, dashed
  through a relay, broken and dim when dark) and gains a **VU meter**: a needle that jumps on
  real traffic and decays. A busy link reads busy from across the room.
- **Traffic is a packet** travelling the wire with a short trail, arriving with a small burst -
  and, as today, only on a real heartbeat or a real session. A quiet fleet is a still board.
- **Discovery is a sweep** along the rail that reveals candidates at the bottom edge.
- **Focus** re-centres the board on the selected node and dims the rest.
- **The mode badge** sits in the header: `EDGE · LOCAL ROOT · 4 nodes · 7 instances`, with the
  route mix beside it.

Mono and red, like the rest of the product. The novelty is motion, density and layout, not
colour: Ping World stays the one deliberate exception (`docs` and the screensaver ruling).

### 13.9 Order of work, corrected

The order in section 11 is not wrong so much as incomplete: it never had a rung 2, and it
assumed nodes rather than instances. Corrected, with 1 to 4 and the session layer already built:

5. **Instances.** A running roger is a member: registered, named, several per node, capabilities
   at the instance level, reported by the node's face. (`features/edge/instances.feature`.)
   BUILT 2026-09-20 for internal/edge, the CLI and the console; the TUI's drawing of instances
   lands with the patch bay (step 10). Proven live: a `roger webui` in one process appears
   under the node in `roger edge` from another, with real GPU and RAM facts, and `roger edge
   name . desk` renames it within a pass.
6. **Mode.** ROOT and ROUTE labelled everywhere, `edge.prefer`, and `local-only` that means it.
   (`features/edge/mode.feature`.) BUILT 2026-09-20 on all three surfaces: the badges, the
   route on every session, the mix in the header, `roger edge prefer`. The dispatch half
   (prefer honoured, local-only structural) lands with step 8.
7. **The message set.** `hello`, `heartbeat`, `describe`, `read`, `classify`, `escalate` on the
   host encoding. (`features/edge/protocol.feature`.)
8. **Local inference.** The ladder, the peer serve endpoint, the local receipt, failover.
   (`features/edge/local_inference.feature`.)
9. **Control and delegation.** `invoke` under a grant, the four arrows, and every refusal in
   13.6. (`features/edge/control.feature`.)
10. **The patch bay.** The screen redesign, superseding `topology_view.feature` with its
    re-approval. (`features/edge/patchbay.feature`.)
11. **Boards.** The compact encoding, then firmware, then a classifier artifact. Each its own
    decision, unchanged.

Steps 5 and 6 are foundations and are worth building before 7 to 9 depend on them. Step 8 is the
one a user would feel first, and it is the one to demo.

### 13.10 The article this is for

The founder intends to write about how agents and devices use each other on a network to build
things that were not possible before. The honest form of that claim is not "distributed
inference" (llama.cpp, exo and others do that) and not "device fleet management" (many do that).
It is the table in 13.5: **one fabric on which a sensor, a model and an agent are the same kind
of citizen**, addressable the same way, trusted the same way, authorized the same way, and
receipted the same way - and which forms with no internet at all, so the network is the owner's
rather than a vendor's.

What that unlocks, concretely, is worth writing as scenarios before it is written as prose: a
camera that escalates to a model on a machine in the next room and an agent that acts on the
result; a laptop that borrows a workstation's GPU without either of them having an account with
anyone; an agent that hands work to the machine that has the disk, the tools or the sensors it
lacks. Each of those should be a passing scenario in this spec set before it is a paragraph in
the article, which is also how the article stays true as the code moves.

### 13.11 Keeping the product, the specs, the code and the article aligned

The direction asks for "ways to make sure we are all aligned into the product". This repo
already has the right instrument and uses it in one place: `features/web/playbox_edge_honesty.
feature` pins what the website may claim about the device line, and it was written because copy
had drifted from the artifact. Generalise that.

**`docs/roger-edge-claims.md` is the claims ledger.** Every sentence we say about Roger Edge in
public, with the scenario that proves it, and a state: BUILT, DESIGNED or INTENDED. The article
and the website are written FROM that file. A claim with no passing scenario is marketing, and
the file says so at the top.

Four mechanisms, all cheap, all fitting a spec-first repo:

1. **`@claim` tags.** A scenario that backs a public claim carries the tag. The mapping becomes
   mechanical rather than a promise in prose.
2. **A both-directions check.** One test asserts every claim in the ledger names a feature file
   that exists with a `@claim` scenario, and every `@claim` scenario appears in the ledger.
   Drift fails the build instead of surfacing in a blog post.
3. **Publish from BUILT only.** Anything else is labelled roadmap, in the copy itself.
4. **Rows move in the commit that moves the code.** A claim becomes BUILT in the same commit that
   turns its scenarios green - never in a tidy-up afterwards, which is where drift is born.

The design doc's what-exists table (section 10) and the order of work (13.9) stay the engineering
view; the ledger is the product view. They are checked against each other by the same test.
