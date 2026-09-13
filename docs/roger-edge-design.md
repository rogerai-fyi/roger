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
| Sessions recorded from `roger use` and the guest-operator proxy | **Not built** (the TUI's own turns are; see 12.8) |
| Edge view in the web console | **Not built** |
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

`roger use` and the guest operators are NOT wired, and it is not a gap in the layer: they are
served by the local proxy in `internal/client.copyRelayResponse`, which already forwards the
same receipt header, so the seam is one callback wide. What is missing is the ATTRIBUTION - who
the caller was - and inventing that without a spec would be guessing at exactly the field the
approved scenarios say must be honest. It wants its own spec and its own approval.

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
