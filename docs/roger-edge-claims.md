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

## Designed, not built

These are specified in `docs/roger-edge-design.md` section 13. They may be described as where
Roger Edge is going. They may not be described as something it does.

| Claim | Spec | State |
|---|---|---|
| Several rogers can run on one machine, each named and addressable, and the fleet routes to the process rather than the box. | `instances.feature` | awaiting approval |
| Capabilities belong to the running process, so a machine stops offering what its process stopped providing. | `instances.feature` | awaiting approval |
| You can always see whether a turn stayed on your own hardware or went out to the market. | `mode.feature` | awaiting approval |
| Local is the default, and `local-only` provably never leaves your Edge. | `mode.feature` | awaiting approval |
| Your fleet is one inference pool: a Pi can use a Jetson's model over the LAN, with no broker and no internet. | `local_inference.feature` | not written |
| An agent on one machine can delegate to the agent on another, under a scoped, revocable, receipted grant. | `control.feature` | not written |
| A sensor, a model and an agent are the same kind of citizen on one fabric: one address, one trust root, one grant, one receipt. | `control.feature` | not written |

## Intended, no spec

Boards and the compact encoding, microcontroller firmware, a trained on-device classifier, the
Apple surfaces. Each is its own decision in the design doc's order of work. None of these is
claimed anywhere today, and `features/web/playbox_edge_honesty.feature` exists to keep it that
way for the classifier in particular.

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
