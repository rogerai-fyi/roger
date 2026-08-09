# v5.7.0 — your band is yours to move, your models can stay home

Three threads: a private band becomes something you can actually manage, the agent can run
on a model that never leaves your machine, and Roger Core learns what a joined Tower is
allowed to claim about the Stations behind it.

**One security fix in here is worth reading before anything else.**

---

## Before you upgrade

### Band mutations now require a signed request

`PATCH /bands/{id}` (move) and `DELETE /bands/{id}` (revoke) verify the Ed25519 signature
over method, path and body. They used to resolve the owner from the `X-Roger-Pubkey` header
alone and check no signature at all — which treats a PUBLIC key as a bearer credential.
Anyone who learned an owner's public key could burn their band's code, irreversibly, cutting
off everyone tuned in; or repoint the band at a model they controlled.

**No client changes are needed.** `roger` and the TUI have always signed these requests, so
the signature was already on the wire and the broker was ignoring it. Anything speaking to
the API directly with only a pubkey header will now receive 403 — correctly.

Reads are unchanged: `GET /bands` still accepts a browser session cookie, because a browser
holds no signing key.

### Self-hosters running a joined Tower: put the CA root in your secret store

Set `ROGERAI_TOWER_CA_KEY_PEM` and `ROGERAI_TOWER_CA_CERT_PEM` (both, or neither — a
half-configured root is refused rather than silently regenerated). With neither set, the
first broker start generates a root, stores it in `rogerai.tower_ca_root`, and logs a loud
warning. That works, but it puts the network's most sensitive key in the application
database. Rotating the root later invalidates every Tower certificate, so treat it as a
network event rather than a config change.

---

## Private bands you can manage

The incident that prompted this: pressing `h` on a model to put it on a private band failed
with `private band limit reached (free plan allows 1) - revoke an existing band first`. Every
word true, none of it usable. There was no `roger bands`, no revoke in the TUI, and the
website's list rendered "No private bands yet" to everyone. The one band in the way was on a
different model on a different machine, and nothing in any interface could have said so.

- **`roger bands`** — list, move and revoke. `roger bands move <band> <model>` repoints a
  band at another model **keeping the frequency code**, so everyone already tuned in stays
  connected. A move is not a mint: the quota is untouched.
- **`PATCH /bands/{id}` now persists `label`**, either alone or atomically with `node_id`.
  An occupied destination leaves both the old binding and old label untouched.
- **BASE STATION `[p]`** — bands are selectable, with move and revoke on the keymap.
- **The quota refusal now names the band in the way**, the machine it is on, and offers to
  move it. It never suggests buying more bands, because no purchase path exists.
- **The website's band list works.** It authenticated with a cookie against an endpoint that
  read only the signing key, so every owner got a 403 that the page rendered as an
  empty state. Reads now accept the session cookie; mutations still do not.

One node carries at most one live band, and both store backends now decide that the same
way — the in-memory store could previously be talked into putting two live bands on one node
when that node had also carried a revoked one. PostgreSQL now enforces that invariant with a
partial unique index too, so two simultaneous moves cannot both claim an empty destination.

## Models that never leave the box

`/model` lists models detected on this machine alongside your tuned-in bands. Picking one
routes turns straight to the local server: nothing registers, nothing is metered, the
weights stay home. The picker reads memory only, so opening it never blocks on a port scan.

Switching away releases the local endpoint. That sounds obvious; it was true on one of the
two code paths, and a turn could keep going to `127.0.0.1` while everything on screen named
a broker band.

## The playbox deck

The console page gains input positions, a tape shelf, transport controls, lamps, a peak
meter fed by arriving tokens rather than a timer, and an operator plate. It remembers the
tape you left in it — and a tape that has since gone off air is not silently swapped for
another.

## Tower: what a Tower may claim

Foundation for the relay link. A joined Tower can now describe the Stations behind it, and
Roger Core can check that description without trusting the Tower.

- **`internal/towerobj`** — the canonical encoding and signature suite every network object
  shares. Two independent implementations must produce the same bytes, so the format refuses
  every ambiguity it can: JCS member ordering on UTF-16 code units, no duplicate members, no
  explicit null, no JSON numbers at all (every integer is a bounded base-10 string), NFC
  strings only.
- **`internal/towerinv`** — signed, revisioned inventories with hash-chained deltas. The
  Station signs its offer, the Tower signs the collection, and neither signature substitutes
  for the other: a leaf verifies against the key Core recorded at attachment, never one the
  leaf carries. A revision is accepted whole or not at all. Expiry rides on the object, so
  nothing polls and no other Tower can refresh it.

**Not shipped:** a registered Tower still carries no traffic. Dispatch and settlement are
the next slice.

## Also fixed

- A tool result is clipped on **every** path, not only the successful one. The unknown-tool
  message interpolates the tool name the model chose, so an oversized name could push an
  unbounded string into the conversation.
- A resumed session sizes its tool budget. It was running with no per-result cap at all.
- A refused visibility change no longer takes a working public share off air.
- The band panel no longer shows a stale load error where "No private bands yet." belongs.

## Upgrading

```
roger update
```

Nothing in this release changes the database schema beyond additive, idempotent migrations
the broker applies at boot.
