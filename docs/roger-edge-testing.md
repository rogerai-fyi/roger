# Testing Roger Edge with real devices and instances

This is the hands-on guide for standing up a small private Edge and checking it behaves as
intended: machines discover each other on the LAN, several rogers run on one machine, some
serve inference to the others, and nothing leaves the network unless you let it.

It matches what is BUILT on `wt/roger-edge` today. Where a step needs something not built
yet, it says so and points at the gap.

## The shape of the test

- **This PC** is one node, running two named instances (two `roger` processes, one node
  identity).
- **The Mac laptop** is a second node, running one instance.
- **The iOS app** is a third node, once it implements enrollment (see
  `docs/roger-edge-ios-handoff.md`; not capable yet).

All three are the same owner's Edge, rooted at a machine you designate (this PC), so it
forms and runs with no internet at all.

## The vocabulary, so the output reads right

- **node** is a machine. It holds the certificate. One per machine.
- **instance** is a running `roger` on it. Several per node, each with a name you choose.
  Addressed `node/instance`.
- **station** is an instance broadcasting one model. That is what "serves inference" means.

Two `roger` processes on ONE machine are two INSTANCES of ONE node. That is the intended
"same node, different names". A second machine is a second node.

## What must be true of your network

- All devices on the **same LAN and subnet**, e.g. everything on `192.168.1.x`.
- **mDNS/multicast** allowed between them. Home routers do this; some corporate and guest
  Wi-Fi networks block multicast or isolate clients ("AP isolation"), and discovery will
  find nothing there. A VPN interface can also swallow multicast.
- Discovery and advertising happen only in a **long-lived** roger (the TUI, `roger webui`,
  or a headless `roger share`). A one-shot `roger edge` scans once but cannot be found by
  others unless they are already advertising. So keep a roger open on each device during the
  test.

Same-host note: two processes on ONE machine will NOT reliably discover each other over
multicast (they share the loopback path), and they do not need to: they are one node, seen
through the household, not through discovery. Discovery is for machine-to-machine.

## Step 1 - make this PC the Edge's authority

An Edge has one root. Designating a machine as the authority means the whole thing works
with no internet and no login.

```sh
roger edge authority local mypc      # generates the root here; the private half never leaves
roger edge enroll mypc               # this machine joins its own Edge
roger edge authority                 # confirm: "rooted at the designated machine mypc"
```

Pin the authority's LAN port so the other machines have a stable address to enroll against:

```sh
export ROGERAI_EDGE_AUTHORITY_BIND=:8791
```

Find this PC's LAN address (you will hand it to the other machines):

```sh
ip -4 addr show scope global | grep -oP 'inet \K[0-9.]+'   # Linux
ipconfig getifaddr en0                                     # macOS
```

Call it `PC_IP` below (e.g. `192.168.1.69`).

## Step 2 - two named instances on this PC

Two `roger` processes, one node, different names. The names come from `ROGER_EDGE_INSTANCE`,
which is per process, so the two do not fight over one saved setting.

Terminal 1 (an interactive TUI agent):

```sh
ROGER_EDGE_INSTANCE=agent-a roger
```

Terminal 2 (a second agent; give it its own ports so the two do not collide):

```sh
ROGER_EDGE_INSTANCE=agent-b ROGERAI_EDGE_BIND=:0 roger
```

In a third terminal, look at the fleet:

```sh
roger edge
```

You should see one node `mypc` with `/agent-a` and `/agent-b` under it, and the header
`EDGE · LOCAL ROOT · mypc · prefers local`. Describe one:

```sh
roger edge describe mypc/agent-a
```

It reports the instance's capabilities, what it is serving (nothing yet), and this machine's
real GPU and RAM. It says plainly it holds no certificate of its own: the node vouches for it.

## Step 3 - make one instance serve a model

For a peer to use inference, an instance must have a model on air, which needs a local
OpenAI-compatible server (Ollama, LM Studio, llama.cpp server, vLLM, etc.).

In one of the roger TUIs, press `[2]` (share), detect your local server, and put a model on
air. Then:

```sh
roger edge bands
```

lists the models your Edge can serve locally and which instance serves each. Anything not
listed there would go to the market.

## Step 4 - add the Mac laptop as a second node

On the Mac, first show its user key so the PC can admit it:

```sh
roger account         # copy the "pubkey:" value - that is this machine's user key
```

Back on the PC, allow that key:

```sh
roger edge authority allow <the Mac's pubkey>
```

On the Mac, enroll against the PC's authority over the LAN, and give this instance a name:

```sh
ROGER_EDGE_INSTANCE=mac roger edge enroll macbook --authority http://PC_IP:8791
```

Then keep a long-lived roger open on the Mac so it advertises and scans:

```sh
ROGER_EDGE_INSTANCE=mac roger
```

Within a discovery pass (about every 30s by default; set
`ROGERAI_EDGE_DISCOVERY_INTERVAL=5s` on both while testing to speed it up), each machine
should draw the other:

```sh
roger edge            # on either machine
```

The PC shows `mypc` (with its two instances) and `macbook`, joined by a solid wire if they
reached each other LAN-direct. A node that stops answering is drawn dark and kept, never
dropped.

## Step 5 - use another machine's inference

With the Mac serving a model the PC does not have (or vice versa), on the machine WITHOUT it:

```sh
roger use <that-model>
```

The dispatch ladder tries your own model first, then a peer on your Edge over the LAN, then
the market. A turn served by the peer never touches the broker and never spends money; it
appears in `roger edge sessions` with route `local`, naming the peer that served it.

To prove nothing leaves the Edge, set:

```sh
roger edge prefer local-only
```

Now a model nothing on your Edge serves is refused with a clear message rather than bought
from the market, and no request reaches the broker at all.

## What to watch for (this is a private network; be sure)

- **Route on every session.** `roger edge sessions` marks each turn `local` or `market`.
  A turn you expected to stay home that reads `market` means no local instance served the
  band and the preference let it fall out. Set `local-only` if that must never happen.
- **The mode badge.** Every surface shows `LOCAL ROOT` (or `CORE`) so you always know
  whether the Edge needs the internet, separately from where a turn went.
- **Membership is by certificate.** A machine that is not enrolled under your root is never
  dialled for inference and never served, even if it is on the same Wi-Fi. A stranger's
  laptop cannot join by being nearby.
- **Actuate is never automatic.** A device capability that changes the world stays
  PENDING CONFIRMATION until you confirm it; nothing acts on your machines without that.
  (The invoke/act path itself is `control.feature`, specified, not built yet.)

## What is NOT built yet (so you do not test for it and think it is broken)

- **The colourful `[3]` patch-bay screen.** The Edge screen today is the list/graph from
  before this work. The game-like coloured screen is specified (`patchbay.feature`) and is
  next. Instances DO show under their node in the current screen.
- **Agent-to-agent (`roger edge invoke`, delegation, acting on a device).** Specified in
  `control.feature`, not built. So step "some may be able to run something on that machine"
  is not yet testable; it is the next build after the patch bay.
- **The end-to-end `roger use` rung labelling and faster-peer choice.** The ladder works;
  the finer scenarios (which rung, fastest peer first) are tagged `@later`.
- **iOS.** The app cannot enroll or be seen yet. See `docs/roger-edge-ios-handoff.md`.

## Handy knobs while testing

| env | what it does |
|---|---|
| `ROGER_EDGE_INSTANCE=<name>` | this process's instance name (per process) |
| `ROGERAI_EDGE_AUTHORITY_BIND=:8791` | pin the authority's LAN port (on the authority machine) |
| `ROGERAI_EDGE_BIND=:0` | pin (or free-pick) this instance's LAN face port |
| `ROGERAI_EDGE_DISCOVERY_INTERVAL=5s` | scan more often while testing |
| `ROGERAI_EDGE_DISCOVERY=0` | turn discovery off entirely for a run |
| `roger edge prefer local-only` | never leave the Edge |

## A quick single-machine sanity check (no second device)

You can prove enrollment, instances and the mode without a second machine, on one box, with
two config directories standing in for two machines:

```sh
export A=/tmp/edge-a B=/tmp/edge-b BIP=$(ipconfig getifaddr en0 2>/dev/null || hostname -I | awk '{print $1}')
HOME=$A XDG_CONFIG_HOME=$A ROGERAI_EDGE_AUTHORITY_BIND=:8791 roger edge authority local shed
HOME=$A XDG_CONFIG_HOME=$A roger edge enroll shed
BKEY=$(HOME=$B XDG_CONFIG_HOME=$B roger account | grep -oE 'pubkey:  [a-f0-9]+' | grep -oE '[a-f0-9]{32,}')
HOME=$A XDG_CONFIG_HOME=$A roger edge authority allow "$BKEY"
# keep shed's authority up (a long-lived roger), then:
HOME=$B XDG_CONFIG_HOME=$B roger edge enroll bench --authority http://$BIP:8791
```

Both `roger edge` listings should show their own node rooted LOCAL. Cross-discovery between
two config dirs on one host is not reliable (loopback multicast) - that part needs two real
machines.
