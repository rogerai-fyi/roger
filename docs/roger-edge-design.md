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
| LAN discovery of roger instances (mDNS) | **Not built** |
| A node class for devices that host no model | **Not built** |
| Capability declaration and verification | **Not built** |
| The Edge message set and its board encoding | **Not built** |
| `roger edge` CLI surface | **Not built** |
| Edge screen and topology graph in the TUI | **Not built** |
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

Steps 1 to 3 are the founder-approved build scope. Steps 4 onward need spec sign-off first.
