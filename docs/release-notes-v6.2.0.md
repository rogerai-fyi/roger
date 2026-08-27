# v6.2.0 - a Tower you run entirely on your own network

A minor, not a patch: this ships a whole new way to run a Tower, and nothing breaks. No API
change, no migration, no configuration change, and the module path is unchanged at
`rogerai.fm/roger/v6`. A joined Tower keeps behaving exactly as it did.

## The standalone consumer plane

Until now a Tower only ever earned by relaying the Open Market's paid traffic: it enrolled
with Roger Core, was approved, and carried consumer requests the broker sealed to it. That
is still the headline product. But it meant a Tower had no answer for the operator who wants
to serve *their own* network - a lab, a clinic, a ship, an airgapped site - where the point
is local inference, not the paid market, and where reaching Roger Core is either unwanted or
impossible.

`roger-tower-local` is that answer. It is a Tower's consumer plane with the market removed:
clients on your LAN call an OpenAI-shaped `/v1/chat/completions`, a node you run answers
them, and no request ever leaves the network.

```sh
roger-tower-local --dir ~/roger-tower           # loopback by default
roger share --broker http://192.168.1.10:8787   # a node connects in and serves it
roger config set broker http://192.168.1.10:8787 # a client points at it and asks
```

Every answer carries `X-Roger-Cost: 0` and `X-Roger-Local: 1`. There is no login, no
on-air lock, no relay share - the traffic is free because it never touches the market.

## It cannot reach the market - by construction, not by policy

The guarantee that makes this safe to run on a private network is structural, not a promise
in a config file. The `roger-tower-local` binary links **none** of the join, Core, or hub
packages - its entire dependency graph is Core-free, and a test fails the build if that ever
stops being true. The consumer handler itself opens no socket and makes no outbound call: a
source-scan gate forbids `net.Dial`/`Listen`/`Lookup`, an HTTP client, or `exec` anywhere in
the plane. A standalone Tower is not *configured* to stay off the market; it is *unable* to
touch it.

## What a standalone Tower does

- **Multi-client admission.** The first client to present a key becomes the operator; the
  rest are admitted as local clients. Keys can be revoked, and a revoked operator cannot be
  silently re-appointed.
- **A poll-based completion loop.** A serving node polls the Tower (`/local/poll`), runs the
  job against its local model, and hands the answer back (`/local/complete`). The Tower
  itself dials nobody - it holds the queue and matches work to whichever node claimed it.
- **Receipts.** Every served request appends to a local receipt log, so an operator can
  account for what their Tower did without any of it leaving the site.
- **`roger share` auto-detects it.** Point `roger share` at a broker and it fingerprints the
  standalone `/local/poll` response; if it is a Tower, it serves it directly instead of
  trying to register on the public network. `http://localhost:PORT` is recognized too.

## Resource safety and a signed anti-replay nonce

Because the plane faces untrusted clients on a shared network, it ships with a per-client
token bucket and concurrency cap, a whole-Tower inflight ceiling, and a station rate limit -
one noisy client cannot starve the rest or exhaust the node.

Request signing gained a **per-request nonce** (`X-Roger-Nonce`), folded into the canonical
signed material, so a captured request cannot be replayed inside the signature's validity
window. Audit caught three real defects in this before it shipped, each named here because
they are the kind that matter: a replay window narrower than a future-skewed signature's
validity (a drifted-clock replay gap), an unvalidated nonce that a caller could grow without
bound (a memory-exhaustion vector), and a non-atomic check-then-record in the replay guard (a
TOCTOU two clients could race through). All three are closed and pinned by regression tests,
including a 64-goroutine race that proves exactly one request wins.

The nonce rides only on the LAN path where a standalone Tower lives; loopback and public
broker traffic are unchanged, so the Open Market's V1 signing contract is untouched.

## Packaging

`roger-tower-local` builds for Linux amd64/arm64 and ships in the release archives and
through `install.sh` (`ROGERAI_COMPONENT=tower-local`), alongside the existing `roger` and
`roger-tower` binaries.

## Also in this release: a browser save can no longer destroy a spend setting

The browser console and the terminal edit the same spend limits. A save from the console
rebuilt each limit from only the fields its price form can see, so saving an output-price cap
in the browser silently wiped the *other* settings the terminal owned on that band - first a
standing quant routing rule, then the input-price money cap (`roger config set-limit
--max-in`). Nothing failed and nothing warned; a routing rule set in the terminal simply
stopped applying, or a money cap vanished. The console's save now merges - it carries every
field its form does not edit - and the shared limit store is mutex-guarded, closing a
concurrent-write crash between the two surfaces. Regression tests pin each case, including a
race test that hammers both surfaces at once. The console's quant field also now accepts the
terminal's own space-separated format, so a rule pasted from the terminal (`Q4_K_M IQ4_XS`)
is read as its labels rather than one that matches nothing.
