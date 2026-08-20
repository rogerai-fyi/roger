# A minimum hardware requirement, and the two halves it has to be split into

**Status: the LOCAL half is BUILT and shipped in this change. The NETWORK half below is a
PROPOSAL, not approved, and every number in it is the founder's call.** Written 2026-08-19
against `release/v5.7.0`.

This is a separate file rather than a section of `docs/relay-selection-design.md` because
that document is owned elsewhere. It is governed by its §4.1, referenced throughout, and
should be read after it.

---

## 1. What was true before this change

There was no minimum requirement anywhere in the system, and no way for an operator to find
out whether their machine was worth putting on the network until they had already put it
there and earned nothing.

That is a bad way to learn it and it is also unfair. `roger share` on a four-core laptop
with no GPU succeeds, registers, goes on air, and is then quietly out-scored by every real
rig serving the same model, forever, with nothing anywhere telling the operator why. The
placement work landed this session made that outcome *sharper* — a slow node is now
correctly and consistently picked less — which raises the obligation to say so in advance.

## 2. Why the requirement cannot be one thing

`docs/relay-selection-design.md` §4.1 rules that supply-side capability must never be
self-declared, because a declared capability is a lever: claim the best hardware, receive
the most work. That is not hypothetical here. This session found the defect twice:

- `--region` is a string the operator types, and it was slated to feed placement;
- self-declared `hw` was moving edge placement by **2x** (`hw=""` scored 0.2500 against
  `hw="multi-gpu"` 0.5000 on identical evidence and load) until it was fixed one commit ago.

So a minimum requirement checked by **reading what a node claims** would be worse than no
requirement at all. It would add a gate whose only effect is to reward lying: every honest
node under the bar is excluded, every dishonest one walks through, and the network has
strictly less information than it started with because now the field is worth faking.

The requirement therefore splits along the line of who is being asked:

| | asks | can see | enforcement | status |
|---|---|---|---|---|
| **Local preflight** | the operator, about their own machine | everything — GPU model and count, VRAM, RAM, free disk, cores | none. It prints. | **built** |
| **Network floor** | the broker, about a node | only what it measured itself | placement weight | **proposed below** |

The local half can be rich precisely because it is powerless. Nothing it learns is
transmitted, so there is nothing to lie *to* — an operator who fakes their own preflight has
fooled themselves and changed nothing on the network. The network half can have teeth
precisely because it is poor: it looks only at signals the node cannot author.

## 3. The local half, as built

`roger share --check` runs a hardware preflight and exits. A normal `roger share` on a
machine below the bar prints one advisory line and **goes on air anyway**.

- Detection: `internal/detect/localhw.go` (parsers), `cmd/rogerai/hw_{linux,other,windows}.go`
  (platform gatherers), `internal/detect/preflight.go` (the bar and the report).
- Nothing new leaves the host, and that is tested rather than asserted:
  `cmd/rogerai/preflight_nowire_test.go` drives a real share against a recording broker with
  a sentinel machine and fails if any of its values appear in any request the node sends.
- The advertised `hw` is still the four-value privacy bucket, and is now read off the *same*
  probe the preflight ran, so the class the network sees and the report the operator reads
  cannot describe two different machines.

**It does not block, and must not become able to.** Somebody serving a small model on a
laptop to their own grant keys is a legitimate user of this software. The `--check` surface
exits non-zero below the bar, for scripting, and says in the same breath that sharing is not
blocked by it.

The local bar (advisory, and also the founder's to change): **8 GiB VRAM** — or 16 GiB
unified on Apple Silicon, which has no separate pool — **16 GiB system RAM**, **20 GiB free
disk**, **4 CPU cores**. The reasoning for each is in the constant block of
`internal/detect/preflight.go` rather than repeated here.

### What the preflight can and cannot determine, per platform

Stated plainly because the report itself says "could not determine" rather than guessing,
and a reader deciding what to trust needs the same table.

| | Linux | macOS (Apple Silicon) | macOS (Intel) | Windows |
|---|---|---|---|---|
| GPU present / count | yes (nvidia-smi, rocm-smi) | yes (integrated) | yes (system_profiler) | NVIDIA only |
| GPU model | yes | chip name via sysctl | "discrete GPU" only | yes |
| VRAM | yes (NVIDIA per-device; AMD via a second rocm-smi call) | **no such quantity** — unified with RAM, and the share macOS grants Metal is not readable | **no** — only in localized system_profiler prose | yes (NVIDIA) |
| System RAM | yes (`/proc/meminfo`) | yes (`sysctl hw.memsize`) | yes | **no** — behind a kernel32 call the standard library does not expose |
| Free disk | yes (statfs) | yes | yes | **no** — same reason |
| CPU cores | yes | yes | yes | yes |

A Windows Radeon box reports as CPU-only. That is a real detection gap, and it is worth
naming because the *same* gap already decides the advertised class: such a node has always
gone on air as `hw=cpu`, and now at least its operator is told why.

---

## 4. The network floor — the proposal

### 4.1 Which signals are eligible

Only measurements the broker made itself. Today that is, per node, in `trustState`
(`cmd/rogerai-broker/recount.go`, fed by `probe.go`'s `recordProbe`):

- `ttftMs` — EWMA time-to-first-result from broker-originated canary probes;
- `probeTPS` — EWMA clean decode tok/s, free of organic queueing;
- `probeCompleted` — the last passing canary ran to completion and produced counted output,
  which is the difference between "answered" and "finished";
- `probes` / `probeFails` — evidence count and the current failure streak.

Not eligible, now or later: `hw`, `Region`, anything in `NodeRegistration`, anything an
offer declares. Also not eligible: the local preflight's verdict. It would be trivial to
report it at registration and it must not be, for exactly the §4.1 reason — the moment a
node's own assessment of its hardware affects its traffic, that assessment stops being an
assessment.

`ttftMs` deserves one caveat carried over from §1.5 of the design doc: it is measured
**broker to node**, so it is node speed plus broker-to-node network latency. It is a fair
floor signal for the classic fabric and a poor one for the edge path, which deliberately
avoids the broker. A floor built on it should apply where it is measured and not be
generalised.

### 4.2 What the floor must NOT be built out of

`router.go` carries `tpsTarget = 120.0` and `ttftCapMs = 2000.0`. **These are the shape of a
scoring curve, not thresholds**, and they must not be promoted into policy by being read a
second time somewhere else:

- `tpsTarget` is where `speedFit`'s throughput half *saturates*. A node at 60 tok/s scores
  0.75 on that term, not "fails";
- `ttftCapMs` is where the latency half *bottoms out* — at 0.6, not at 0.

Quietly reusing either as a floor would turn a soft gradient into a hard gate that nobody
decided on, and would silently couple two knobs that exist for different reasons. If a floor
constant happens to land on the same number, it should still be its own named constant with
its own comment saying why.

### 4.3 The action: DEMOTE, never delist

**A node below the floor should have its placement score multiplied down. It should never be
removed from the candidate set.**

The reason is not gentleness, it is that a hard floor on measured performance removes the
wrong nodes:

- A slow-but-honest node may be the only node somebody's grant keys can reach. Delisting it
  turns "slow" into "no service", for people who chose free capacity precisely because they
  had no alternative.
- Measurement is noisy and the evidence is thin. A node behind a congested link, or probed
  during a model reload, or serving a genuinely large model that decodes slowly, all look
  the same as a bad machine at a single measurement.
- Demotion is already self-correcting and delisting is not. A demoted node still gets
  occasional traffic, so it keeps being measured and can climb back. A delisted node stops
  being measured, and nothing will ever un-delist it.
- The scoring product already *is* the demotion mechanism. Adding a floor multiplier to it
  is a one-line change in a function whose behaviour is understood; adding an exclusion is a
  new eligibility class with its own recovery problem.

There is one exception that already exists and should not be confused with this: a node
failing canaries outright is a liveness problem, handled by `probeFails` and the concierge
gate, and that is about whether the node *works*, not whether it is *fast*.

### 4.4 Cold start: unmeasured is neutral, not bad

M1 established this rule and the floor must not break it. An unmeasured node scores the
neutral prior (`tp := 0.75`, "tps unmeasured: neutral-positive"), and the same must hold
under a floor: **a node with no measurements is not below the floor, it is unjudged.**

Breaking this would be self-defeating in a way that is easy to miss. A node that is demoted
for having no measurements receives less traffic, and probe cadence is adaptive — a
persistently idle node's personal probe interval *doubles* each idle round toward the
ceiling. So "unmeasured means bad" would build a machine that starves new nodes of exactly
the traffic and the probes they need to stop being new. Every fresh node would be born into
a hole it could only climb out of slowly, and the newest honest operator would have the
worst experience of the network.

Concretely: the floor should require a minimum evidence count before it applies at all
(proposed below), and the transition from "unjudged" to "below the floor" should be a ramp
rather than a step, so one bad probe on the threshold does not halve a node's traffic.

### 4.5 Proposed numbers — **THE FOUNDER'S CALL**

Flagged clearly, and offered as a starting point to argue with rather than a
recommendation to adopt. I have no measured distribution of the live fleet to fit these to,
which is itself the first thing to do.

| knob | proposed | reasoning |
|---|---|---|
| `floorTPS` | **10 tok/s** | Below roughly ten tokens per second, a chat response arrives slower than a person reads, and the consumer experience is bad in a way no amount of price advantage fixes. It is also comfortably below what any GPU serving a quantized 7-8B achieves, so it separates "CPU inference or a badly overloaded box" from "a modest GPU", which is the distinction the local bar draws too. |
| `floorTTFTMs` | **8000 ms** | Four times `ttftCapMs`, deliberately: the scoring curve has already bottomed out by 2s, so a floor at the same place would be redundant with the curve. Eight seconds to first byte is where a request reads as broken rather than slow. |
| `floorPenalty` | **0.35** | The multiplier applied at or below the floor. Enough to lose nearly every power-of-two-choices draw against a healthy peer, not enough to be an exclusion: a floor node still wins when it is the only candidate, which is the grant-keys case. |
| `floorMinProbes` | **5** | The evidence count before the floor applies at all. Below it the node is unjudged and scores the existing neutral. Five completed canaries at the probe floor interval is minutes, not days, so an honestly slow node is not demoted on one unlucky measurement and is not shielded for a week either. |
| `floorRampProbes` | **5 → 15** | The penalty interpolates from 1.0 to `floorPenalty` between the minimum evidence count and three times it, so crossing the threshold is a slope rather than a cliff and a node hovering on it does not oscillate. |

Two things I would want measured before any of these are set: the live distribution of
`probeTPS` and `ttftMs` across the current fleet (a floor that catches half the fleet is a
repricing, not a floor), and how many nodes would be demoted whose traffic is
grant-funded — because that number is the actual cost of the policy and nothing else in this
document can estimate it.

### 4.6 What is deliberately not proposed

- **No floor on the edge path.** `edgeQuality` admits evidence downward only and the edge
  fleet is small; a floor there would compound with a demotion that already cannot be
  climbed out of. The edge path needs its own measurement loop before it needs a floor.
- **No VRAM, RAM or disk floor on the network side, ever.** They are unmeasurable from the
  broker and therefore could only be self-declared. They belong to the local half and stay
  there.
- **No user-visible label.** "Below the floor" must not become a badge in `/market` or
  `/discover`. It is a noisy internal judgement about a machine, and publishing it would
  make a transient measurement into a reputational fact the operator cannot answer.
- **No effect on price.** A slow node is free to be cheap; the router's `priceMod` already
  handles that trade, and folding the floor into pricing would double-count it.

### 4.7 Where it would go, and why it is not here

`cmd/rogerai-broker/router.go` (`speedFit` or a new `floorFactor` in the score product) and
`cmd/rogerai-broker/probe.go` (nothing new to collect — the signals already exist). Both are
owned by another agent in this session and were not touched. Nothing in section 4 is
implemented; this document is the specification, and it should not be built until the
numbers in 4.5 are the founder's rather than mine.

---

## 5. The Tower is a different machine with different requirements

A Tower needs no GPU and runs no model. Its requirements are a stable address, an exposed
port, bandwidth, and a **synchronised clock** — and the last one is newly load-bearing.

`roger-tower doctor` now checks it, in two independent ways, because they answer different
questions:

- **Is anything keeping this clock right?** The kernel's own time-discipline state
  (`adjtimex`, Linux only in this build). Offline, instantaneous, and the half that carries
  a repair.
- **How wrong is it now?** One SNTP round trip. Measured against a public NTP server, not
  ours, because the question is whether this clock agrees with the world.

A measured skew at or beyond `protocol.SigMaxSkew` (five minutes) is a **PROBLEM**, not a
note, and it is the only machine-level fault doctor calls one. `protocol.VerifyRequest`
refuses any timestamp more than that far from the verifier's clock, the verifier is the
Tower, so a Tower outside the window refuses every correctly signed request from every
honest node — with a 401 that says nothing about time — and relays nothing at all. The
operator whose earnings stop is the *node's*, and the machine at fault is the Tower's.
Beyond a tenth of the window it is a note instead, because the five minutes is one budget
shared with the node at the other end and `docs/relay-selection-design.md` §5.4b's whole
argument is that an unsynchronised node is ordinary and must not be refused for it.

**A standalone Tower does not make the measurement unless asked.** Its promise is that it
makes no outbound connection — a Phase 1 gate with a source-level test behind it, not a
setting — and doctor does not quietly break that to check a clock. It reports that it did
not measure and why, and `--clock-check` is the operator's explicit consent. A joined Tower
already talks to Roger Core, so it measures by default and `--offline` opts out. The dialer
itself lives in `internal/clockprobe` rather than in `internal/tower`, so the isolation gate
stays a proof rather than a proof with an exception in it.
