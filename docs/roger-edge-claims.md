# Roger Edge: the claims ledger

This file exists so the product, the specs, the code and anything we publish cannot quietly
drift apart. It is the list of things we say Roger Edge does, and for each one, the scenario
that proves it.

**The rule: a claim with no passing scenario is marketing.** The article, the website and any
talk about Roger Edge are written FROM this file, not from memory. If something belongs in the
article and is not here, it either becomes a scenario first or it is not said.

States: **BUILT** (a scenario proves it and it is green) · **DESIGNED** (specified, approved or
awaiting approval, not implemented) · **INTENDED** (in the design doc's order of work, no spec
yet). Nothing is published from anything but BUILT, except as a plainly-labelled roadmap.

---

## Built

| Claim | Proven by |
|---|---|
| Your machines find each other on a local network with nothing to configure. | `discovery.feature` |
| Every peer is checked against the certificate it actually serves, pinned to its key, before it is believed. | `discovery.feature`, `node_identity.feature` |
| An Edge can form and run with no internet at all: you designate a machine and it roots the fleet. | `enrollment.feature` |
| The root never travels. Only the public half is distributed. | `enrollment.feature` |
| The fleet view never draws a link it has not seen: solid is verified LAN-direct, dashed is through a relay drawn as its own node, broken is a node gone quiet. | `topology_view.feature` |
| A node that stops answering is drawn dark and KEPT, never silently dropped. | `topology_view.feature` |
| The animation is evidence: a pulse travels an edge only when that node was really heard from. | `topology_view.feature`, `console_view.feature` |
| Traffic is drawn as sessions, each attributed to whoever really opened it, derived from receipts and never invented. | `sessions.feature`, `session_attribution.feature` |
| `roger use`, the terminal's agent, a guest operator and the web console are one shape, not four mechanisms. | `session_attribution.feature` |
| A session opened by one roger is visible to every other roger on the machine. | `session_attribution.feature` |
| A device that cannot name what it sees escalates, and the escalation renders as the right call, never a fault. | `sessions.feature` |
| Routing a finding never rewrites the finding. | `sessions.feature` |
| Actuate is never inferred from behaviour. It is declared and confirmed by the owner. | `node_identity.feature` |
| An empty Edge tells you what is true about this machine and what to do next, and never says "the only node" when it could not read the fleet. | `empty_edge.feature` |
| The same fleet, sessions and words appear in the terminal, the web console and the CLI. | `console_view.feature`, `empty_edge.feature` |
| Several rogers can run on one machine, each named and addressable as node/instance, and the fleet routes to the process rather than the box. | `instances.feature` (the TUI drawing of instances lands with the patch bay) |
| Capabilities belong to the running process, so a machine stops offering what its process stopped providing. | `instances.feature` |
| A roger reports the resources it could read (GPU, GPU memory, RAM) and invents nothing it could not. | `instances.feature` |
| Every surface says what roots the Edge (LOCAL or CORE) and, on every session, whether the turn stayed on your hardware or went to the market. The two are never one label. | `mode.feature` |

## Designed, not built

These are specified in `docs/roger-edge-design.md` section 13. Where a row says BUILT
inside its state, that half is green now and may be described as working; the rest is roadmap.

| Claim | Spec | State |
|---|---|---|
| Your fleet is one inference pool: a Pi can use a Jetson's model over the LAN, with no broker and no internet, and a turn falls out to the market only when nothing here serves it. | `local_inference.feature` | the LAN serving face, the local receipt, and the proxy ladder are BUILT and green; the @later end-to-end scenarios (rung order under load, faster-peer choice, `roger use` naming the rung) land next |
| Local-only provably never leaves your Edge. | `mode.feature`, `local_inference.feature` | the refusal is BUILT and green; the structural (link-level) proof lands with control |
| An agent on one machine can delegate to the agent on another, under a scoped, revocable, receipted grant. | `control.feature` | not written |
| A sensor, a model and an agent are the same kind of citizen on one fabric: one address, one trust root, one grant, one receipt. | `control.feature` | not written |

## Intended, no spec

Boards and the compact encoding, microcontroller firmware, a trained on-device classifier, the
Apple surfaces. Each is its own decision in the design doc's order of work. None of these is
claimed anywhere today, and `features/web/playbox_edge_honesty.feature` exists to keep it that
way for the classifier in particular.

The iOS/macOS app is not yet an Edge member: it can consume the market but cannot enroll,
appear on the fleet, or use peer inference. The handoff and the wire contract it needs are in
`docs/roger-edge-ios-handoff.md` and `docs/roger-edge-enroll-wire.md`. Nothing about iOS on
the Edge may be claimed until the app ships it.

---

## Keeping this file honest

1. **Tag the scenarios.** A scenario that backs a row above carries `@claim`. That makes the
   mapping mechanical instead of a promise in prose.
2. **Check both directions.** A test asserts that every claim here names a feature file that
   exists and a scenario tagged `@claim`, and that every `@claim` scenario appears here. Drift
   fails the build rather than surfacing in a blog post.
3. **Publish from BUILT only.** Website copy and the article take their sentences from the Built
   table. The repo has been bitten once already by copy that named the layer when it meant the
   product line, and the fix was exactly this: pin what may be said to what is true.
4. **Move rows deliberately.** A row moves from Designed to Built in the same commit that turns
   its scenarios green, never before and never in a tidy-up afterwards.
