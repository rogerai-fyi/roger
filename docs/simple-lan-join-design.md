# Simple, secure LAN join: one invite for Towers (and Stations), like Edge

Status: DESIGN ONLY (2026-10-10). No code or spec has changed. Founder decisions are in §4.

Asked: "is there a more automated, easier to manage but still secure flow? ... super stupid
simple to use".

Short answer: yes. Today, joining a LAN Tower takes about 5 commands across 2 machines, 3
copy-pastes, and a Tower restart. The code in that flow protects nothing, because the owner
types it back into their own machine. Meanwhile the client never checks which Tower it reached.
The recommended flow is **one invite, one paste**:

1. The owner runs `roger tower invite` (or presses `i` in the TUI or console).
2. The client pastes `roger lan join <invite>`.

The Tower admits the client on the code, and the client pins the Tower's identity. That is
fewer steps than today, and stronger: it closes the "trust the LAN" gap for Towers. Edge already
works this way (approved 2026-10-06), so Edge and Towers end up with one join concept.

---

## 1. Today's flows, counted

Steps are what a person does. Copy-pastes are values moved by hand, between machines or between
two commands.

### 1a. A consumer joins a standalone Tower

| # | Who | What | Notes |
|---|---|---|---|
| 1 | client | `roger whoami` | prints the client id (its key hash) |
| 2 | client → owner | send the client id to the owner | copy-paste 1, across machines |
| 3 | owner | stop `roger tower serve` | `invite`/`admit` take the data dir's exclusive lock, so they refuse while serve runs |
| 4 | owner | `roger tower invite --client <id>` | prints an invitation id and a code |
| 5 | owner | `roger tower admit --client <id> --id <inv> --code <code>` | copy-pastes 2 and 3, on the same machine |
| 6 | owner | restart `roger tower serve` | |
| 7 | client | `roger lan add <addr>` | the client trusts whatever answers there |

That is 7 steps, 3 copy-pastes and a service interruption. The person who already has the
client id produces the code and immediately types it back into their own machine.

**What actually protects today:**
- The owner explicitly admits key K: `IsAdmitted(K)` in `internal/tower/bootstrap.go`.
- Requests are signed by K and carry the nonce (`localplane.authClient`).

**What doesn't:**
- The bootstrap code adds no security in this CLI flow. Its machinery (one-time, a durable
  attempt budget, a global limiter, a verifier never stored in plaintext) was built for a code
  that travels to a *client* and is consumed *remotely*. That remote consume was never built.
  `modes.feature:233` describes it: "a correct ... code bound to client public key K".
- The client pins nothing (`features/lan/trust_and_honesty.feature`: "trust the LAN"). Any LAN
  device can answer at the address, impersonate the Tower, and read prompts.

### 1b. A Station attaches to a standalone Tower

| # | Who | What |
|---|---|---|
| 1 | station | `roger share --broker http://<tower>`: detects the Tower and prints its node key and the hint `roger tower attach --station <name> --key <K> --models <m>` |
| 2 | station → owner | send the key and model list (copy-paste, across machines) |
| 3 | owner | stop serve, `roger tower attach --station <name> --key <K> --models <list>`, restart serve |
| 4 | station | re-run share (the poll now gets work) |

That is about 5 steps, 1 or 2 copy-pastes, and a serve restart. The model list is typed by
hand and goes stale when the station changes models.

### 1c. A device joins an Edge (today, approved 2026-10-06)

| # | Who | What |
|---|---|---|
| 1 | joiner | `roger edge setup` (prints its key) |
| 2 | joiner → owner | the key (copy-paste 1) |
| 3 | owner | `roger edge authority allow <key>`: prints a one-time code, a join command, and `--invite` |
| 4 | owner → joiner | the invite (copy-paste 2) |
| 5 | joiner | `roger edge enroll <name> --invite <invite>` |

That is 5 steps and 2 copy-pastes. The joiner verifies the root through the code's HMAC proof,
so an impostor authority is refused. There is also the claim gesture (`features/edge/claim.feature`):
1. a candidate advertises;
2. the owner presses `a` on [3] (no key copy);
3. the joiner types the code shown.

### Summary

| Flow | Steps | Copy-pastes | Restart | Who verifies whom |
|---|---|---|---|---|
| Consumer joins Tower | 7 | 3 | yes | Tower checks client; client checks nothing |
| Station attaches to Tower | ~5 | 1-2 | yes | Tower checks station key |
| Device joins Edge | 5 (or 3 with claim) | 2 (or 1 code) | no | both directions |

---

## 2. Options

Threats considered for every option:
- **(I) Impostor Tower:** a LAN device answers as the Tower.
- **(C) Impostor client:** someone joins who shouldn't.
- **(R) Replay:** a captured request or code is replayed.
- **(B) Brute force:** someone guesses the code remotely.
- **(L) LAN attacker:** someone sniffs or MITMs plain http.
- **(O) Offline / airgap:** the flow must work with no internet.

### (a) One invite string (and QR) for everything (RECOMMENDED)

**Owner:**
- `roger tower invite` takes no arguments and needs no client id. Pressing `i` on the Tower row
  in the TUI or console does the same.
- It mints a one-time code (Edge's format: 10 Crockford base32 symbols, 15 minutes, single use)
  and prints:

```
invite (valid 15 min, one use):
  roger lan join rogertower:192.168.1.69:8787#fp=sha256:9f3c...e1&code=7KD2M-QX4PA
or scan:  [QR]
```

**Client:**
- One command: `roger lan join <invite>`.
- It connects to the address and checks the Tower's identity against `fp`, either over TLS with
  the pinned key or with signed answers (decision §4.2). It presents its key K plus a proof
  derived from the code, the same construction as Edge's proof with roles mirrored: PBKDF2 of
  the code, then an HMAC over (domain, K, the Tower's fp, nonce).
- The Tower runs the **existing** `ConsumeInvitation` logic (durable attempt budget, global
  limiter, verifier) and on success admits K.
- The client adds the Tower to `lan_towers`, **with its pinned fingerprint**. Every later request
  checks it.

**Steps:** 1 for the owner, 1 for the client, 1 paste, no restart (see (d)). Today it is 7 steps
and 3 copy-pastes.

**Security:**
- **I:** closed. The client refuses any Tower whose identity doesn't match `fp`. Today the client
  checks nothing, so this is strictly stronger.
- **C:** only whoever holds the invite within 15 minutes, and only once. The owner sees
  "laptop-x joined" right away and can revoke it. The invite is a bearer secret, exactly like
  Edge's approved `--invite`. Optional hardening: `roger tower invite --for laptop-x`, so the name
  must match.
- **R:** single use, and the proof is bound to a fresh nonce and to K.
- **B:** the existing budget (5 wrong tries, then the code is dead) plus the global limiter. A
  50-bit code makes guessing hopeless inside that.
- **L:** with TLS pinned, a sniffer learns nothing. With signed answers only, the prompts are
  still plain http (today's exposure, unchanged), but impersonation is closed.
- **O:** works fully offline. Nothing contacts Core.

### (b) Request and approve, with a matching phrase

**Flow:**
- **Client:** `roger lan add <addr>` (or picks a candidate on the dial). It sends a join request
  with K and a name.
- **Owner:** sees "laptop-x wants to join · `amber-river-71`" in the TUI or console. The client
  shows the same phrase. The owner checks they match and presses `y`.

**Steps:** 1 for the client, 1 keypress plus a glance for the owner. No copy-paste at all.

**Is the phrase real protection?** Only if it is a SAS over an authenticated key exchange: the
phrase must be derived from a transcript covering both public keys and both parties' fresh nonces
(commit-then-reveal, as in Bluetooth numeric comparison or Signal safety numbers). Then a MITM who
swaps keys produces different phrases on the two screens.
- **The catch:** it is only as strong as the human comparison, and people press `y` without
  looking. A short phrase (~20 bits) is fine against a MITM, since they get one try per pairing.
  It is no help when the owner doesn't compare.

**Security:**
- **I/C/L:** good if the owner compares, weak if they don't.
- **R:** fine.
- **B:** not applicable.
- **O:** works. But the Tower must listen for join requests at all times, so pending requests
  need a cap and a rate limit (flood).

**Verdict:** lovely UX. Worth adding later as TUI/console sugar on top of (a)'s machinery, not
as the only path.

### (c) Reuse the Edge enrollment machinery: one join concept

Edge already has the hard parts, approved and tested:
- `internal/edgeauth/code.go`: the code format, the PBKDF2/HMAC proof and golden vectors.
- An invite string, the code-entry UI in the TUI wizard, and console adopt/join.

Towers should use the **same primitives**: the same code format and proof package, renamed with
a Tower domain string, plus the same invite grammar and UI components. The verb also
generalizes. `roger lan join <invite>` (or a top-level `roger join <invite>`) accepts either an
Edge invite or a Tower invite and does the right thing.

Merging the *concepts* (making a Tower an Edge member) is not recommended:
- **Different trust:** an Edge member gets a certificate under the owner's root and can be
  invoked. A Tower client only gets to *use* a Tower, which may belong to someone else, such as a
  lab Tower.
- **Different audiences:** keeping them distinct keeps the honesty labels ("your hardware" vs "a
  LAN Tower") accurate.

So: shared parts, one verb, two kinds of membership.

### (d) Invite and attach work while the Tower is serving

Today `invite`/`admit`/`attach` refuse while `serve` runs, because they take the data dir's
exclusive lock. Options:
1. **Admin socket (recommended).** `roger tower serve` opens a Unix socket in the data dir with
   mode 0600, owned by the operator. `roger tower invite`/`attach`/`stations`/`revoke` talk to the
   running process through it, and fall back to the direct lock when serve isn't running.
   - Security is the filesystem permission, the same trust as editing the data dir directly.
   - No new network surface, and it works offline. Windows uses a named pipe with an owner ACL.
2. **Concurrent store.** Let these commands take a shared or short lock while serve holds a
   reader lock. This is riskier, because it means two writers on one snapshot.
3. **The running process does admin itself, only through its own TUI.** This leaves headless
   Towers without a CLI path, so it's not enough on its own.

### (e) Stations join with the same invite

`roger tower invite --station` mints a station-kind invite. On the station machine,
`roger share --tower <invite>` (or `roger lan join <invite>`) proves the code, gets attached
under its own name, and reports its **own** models from its live offers. That ends the
hand-typed `--models` list that goes stale. The owner can restrict them with
`roger tower invite --station --models qwen3-8b`. Security is the same as (a).

### (f) Other simplifications found

- **The client id is hard to find.** `roger whoami` prints it with a hint naming the wrong
  command. With (a) the client never needs its id.
- **The not-admitted hint is wrong** (`internal/client/lan.go:288`). It tells the client the
  owner "gives the code to this machine", when the owner actually consumes it. With (a) the hint
  becomes "ask the Tower's owner for an invite (`roger tower invite`), then run `roger lan join
  <invite>`".
- **`roger lan add` becomes optional.** (a) adds the Tower as part of join. `lan add` stays for
  Towers you already belong to, or for candidates.
- **QR.** The invite fits in a QR. The TUI can draw it with half-blocks (a dependency choice, so
  decision §4.6). The phone side is a later UX slice, as with Edge's QR (D12).

---

## 3. Recommendation

Build (a) with (c)'s shared primitives, (d1) the admin socket, and (e) station invites. Add (b)
later as TUI/console sugar.

### What the user sees

**Owner** (Tower machine, serve running, no restart):

```
$ roger tower invite
invite for a client (valid 15 min, one use):
  roger lan join rogertower:192.168.1.69:8787#fp=sha256:9f3c...e1&code=7KD2M-QX4PA
waiting... laptop-x joined (key 4be1...a9). Revoke with: roger tower revoke-client laptop-x
```

In the TUI, the Tower row's `i` key (or the console's "Invite a client" button) shows the same
invite plus a QR, then the "joined" line when it's used.

**Client** (one paste):

```
$ roger lan join rogertower:192.168.1.69:8787#fp=sha256:9f3c...e1&code=7KD2M-QX4PA
checked the Tower's identity: sha256:9f3c...e1  ✓
joined the Tower at 192.168.1.69:8787 as laptop-x
it serves: qwen3-8b, gpt-oss-20b    try: roger --lan use qwen3-8b
```

**Station** (one paste):

```
owner:   $ roger tower invite --station
station: $ roger lan join <invite>      # or: roger share --tower <invite>
         joined as a station; serving: smollm2-135m-instruct (from this machine's offers)
```

**Failure messages:**

| Situation | Message |
|---|---|
| Wrong or expired code | "that invite has expired or was already used - ask the owner for a new one" (the budget stays uniform server-side) |
| Fingerprint mismatch | "the device at 192.168.1.69:8787 is not the Tower this invite names - nothing was sent; check you're on the right network" |
| Tower unreachable | "can't reach 192.168.1.69:8787 - is `roger tower serve` running?" |

### Spec changes (proposed edits for approval; not made here)

- **New `features/lan/join.feature`**, exhaustive:
  - invite format and parse;
  - Tower and station kinds;
  - remote consume using the existing budget and limiter;
  - proof binding to K, the Tower fp and a nonce;
  - fingerprint pin and mismatch refusal;
  - TLS (or signed answers);
  - `--for <name>`;
  - a revoked client;
  - invite while serving via the admin socket;
  - the TUI/console invite and QR;
  - offline;
  - brute force, replay, a stolen invite race, and invite reuse;
  - the old CLI path still working for one release.
- **Superseded:**
  - `features/packaging/one_binary.feature:605`: the not-admitted hint names `roger tower invite`
    and `roger lan join`.
  - `features/lan/trust_and_honesty.feature`: Towers joined by invite are pinned and shown
    "verified". Towers added with `lan add` and no pin stay "unverified" under the 2026-10-06
    "trust the LAN" ruling, narrowed to that case.
  - `features/tower/station_attachment.feature:49` "The operator starts an explicit Station
    invitation": consistent. Its text gains the invite string and self-reported models.
  - `features/tower/modes.feature:233` (client invitation consume): consistent, since it already
    describes a remote, client-bound consume. Note that K is bound at consume time, not at mint.
- **Unchanged:**
  - `IsAdmitted`, signing, the nonce and the uniform 401.
  - Edge enrollment specs. They only gain the shared verb, if decision §4.5 says yes.

### Effort

Roughly 3 to 5 focused agent-days with tests:

| Part | Size |
|---|---|
| Shared code/proof package (moved out of edgeauth with a domain parameter, keeping Edge's golden vectors) | small |
| Localplane `/local/join` handler, using the existing ConsumeInvitation with an unbound-at-mint variant | medium |
| Tower identity pin: TLS with a self-signed cert from `tls.key`, pinned by fp, or signed answers with `identity.key` | medium (TLS touches every client call to a Tower) |
| `roger lan join`, the invite parser and `lan_towers` gaining a pin | small to medium |
| Admin socket for serve (Unix socket and Windows pipe) | medium |
| TUI/console invite with the joined notification | small to medium |
| Station invites with self-reported models | medium |

### Migration

- **Existing admitted clients keep working.** `IsAdmitted(K)` is unchanged. The first time a
  client talks to a Tower after upgrading, it records the Tower's fingerprint on first use, but
  only from an address the user already added. It shows "pinned now (first use)" once, and refuses
  on any later change. This is TOFU for upgrades only. New joins always pin from the invite.
- **Old commands stay one release as "advanced":** `roger tower invite --client` plus `admit`, and
  `attach --station --key --models`. Their help points at the invite flow.
- **Plain-http Towers:** if TLS is chosen, serve answers on TLS and keeps plain http for one
  release (logged "a client used plain http; upgrade it"), then plain http is dropped.

---

## 4. Open founder decisions

1. **The main flow.** (a) one invite and one paste [recommended]; (b) request and approve with a
   matching phrase; or both now.
2. **How the client checks the Tower.**
   - TLS with a pinned self-signed key [recommended: it also hides prompts from LAN sniffers]
   - signed answers over plain http (simpler; prompts stay readable on the LAN)
   - no pin (today's "trust the LAN")

   Recommended means narrowing the 2026-10-06 "trust the LAN" ruling to Towers added without an
   invite.
3. **Bearer invite or bound invite.** A bearer one-time invite valid for 15 minutes, with a
   "joined" notification and revoke [recommended, same as Edge]; or always require `--for <name>`;
   or owner confirmation on first use.
4. **Admin while serving.** A Unix socket or named pipe owned by the operator [recommended]; a
   concurrent store; or TUI only.
5. **One verb.** `roger lan join <invite>` for Towers only [recommended now]; a top-level
   `roger join <invite>` that also accepts Edge invites [recommended next, once both share the
   primitives]; or keep the verbs separate.
6. **QR in the terminal.** Add a tiny QR encoder (vendored or a dependency) [recommended:
   phone-friendly, and Edge's D12 wants it later]; or text invite only.
7. **Old commands.** Keep `invite --client`/`admit`/`attach --key` for one release as advanced
   [recommended]; or remove them now.

---

## 5. Proposed spec (2026-10-10, awaiting founder approval)

Founder rulings 2026-10-10:
- **Both flows now.** The one-paste invite and request-and-approve are both built in this slice.
- **TLS with the pinned key.**
- **Bearer invites.** An invite is single use and valid for 15 minutes, and works for whoever holds it.
- **One `roger join`** for Edge devices and Towers alike.

The spec lives in `features/join/`:

| File | Scenarios | Covers |
|---|---|---|
| `invite.feature` | 27 | minting while serving, the invite scheme, code entropy, client/station/Edge joins, aliases, offline |
| `approve.feature` | 18 | request and approve, the SAS construction, timeouts, one pending per client, caps, headless approve |
| `tower_identity.feature` | 14 | Tower TLS from `tls.key`, pinning, refusing a changed key, rotation, the plain-http transition |
| `admin_socket.feature` | 9 | the owner-only socket, admin while serving, Windows named pipe, stale socket |
| `migration.feature` | 8 | pin on first use after upgrade (once), old commands with deprecation notes |
| `adversarial.feature` | 23 | impostor Tower and client, replay, cross-domain proofs, brute force, stolen and expired invites, approve MITM, flood, downgrade, addresses |
| `surfaces.feature` | 8 | one-line copy per situation, the not-admitted hint, TUI/console panels, QR |

### Decisions marked in the files (recommendations)

1. **Invite scheme:** one `roger://join?kind=...&at=...&fp=...&code=...`. The existing `rogerai-edge://join?...` invites stay accepted.
2. **Code length:** Tower invite codes are 128-bit, keeping the approved `modes.feature:194`, since invites are pasted or scanned. Edge codes stay 50-bit, since people type them. Both use one proof package with distinct domain strings.
3. **TUI key:** `i` on the Tower row and on [3], or `+` if `i` is already bound there.
4. **Older unused invites:** they stay independent. `invite --list` and `--void <id>` manage them.
5. **Bearer Edge invite:** the Edge code admits the joiner's key on use, which supersedes enrollment D15. `edge authority allow <key>` stays as an option.
6. **SAS phrase:** three words from a fixed 2048-word list plus two digits, about 40 bits.
7. **Request lifetime:** 2 minutes.
8. **Pending cap:** 8 per Tower, and 5 requests per source address per minute.
9. **Plain http:** served for one release with a warning and marked "unverified". The release after, it's refused unless the owner passes `--allow-plain-http`.
10. **Windows admin:** a named pipe whose ACL grants only the serving user.
11. **`roger lan add` after the transition:** it stays for Towers you're already admitted to. For a TLS Tower it asks "pin this Tower? [y/N]".
12. **QR encoder:** vendor a small pure-Go encoder under `internal/` with a golden test, adding no new module dependency.

### Approved scenarios this supersedes (proposed text; not edited here)

- **`features/packaging/one_binary.feature:600`** ("The not-admitted hint names the command the owner actually runs").
  - **Then:** "the hint says this Tower hasn't let you in yet: ask its owner for an invite (roger tower invite), then run roger join <invite>".
- **`features/lan/trust_and_honesty.feature`:**
  - **:29 and :38, the trust note:** it applies only to plain-http Towers during the transition release.
  - **:44, TUI add confirm:** the confirm shows the Tower's short fingerprint and asks to pin it.
  - **:49, row marking:** a pinned Tower row carries "verified", and only an unpinned plain-http Tower carries "unverified".
- **`features/lan/discovery_and_dial.feature`:**
  - **:112, :118, :125, :131, :138, :153 (`roger lan add`):** unchanged as behavior, but `lan add` becomes the path for Towers you're already admitted to. For a TLS Tower it asks to pin.
  - **:182, :191 (candidates):** adding a candidate becomes "join it". The dial's add key runs request and approve against the candidate.
- **`features/tower/modes.feature`:**
  - **:232, consumption:** consistent. The invitation is consumed remotely by the client, binding K at consume time instead of at mint. Its Given drops "bound to ... client public key K" at mint and gains "bound to the K that presents the proof".
  - **:245, binding mismatch:** the "another client public key" row moves to `--for <name>` invites only.
  - **:260, root pin:** consistent. The client pins the Tower identity: the TLS key here, and the offline root where that exists.
- **`features/tower/station_attachment.feature:49`** (explicit Station invitation, marked unbuilt as worded).
  - **Text:** becomes "the operator runs roger tower invite --station; the station joins with roger join <invite> and reports its models".
- **`features/edge/enrollment_root_trust.feature`:**
  - **D15:** a bearer `roger edge invite` admits the joiner's key on use.
  - **:328, invite carries address, pin and code:** consistent. It gains the unified scheme, with the old scheme accepted.
- **`features/tower/standalone_consumer_plane.feature`:** its routes gain TLS from the Tower key and the join routes.

### Implementation map

| Area | Files | Change |
|---|---|---|
| Shared code and proof | `internal/edgeauth/code.go` → a domain-parameterized package (for example `internal/joincode`) | Keep the Edge golden vectors byte-identical. Add the `tower-join` domain and 128-bit codes. |
| Invite scheme and `roger join` | `cmd/rogerai/edgeenrolltrust.go` (`edgeInvite`, `edgeParseInvite`), new `cmd/rogerai/join.go` | One parser for `roger://join` plus the old Edge scheme. `roger join` dispatches on kind. `lan join` and `edge enroll --invite` become aliases. |
| Tower admission on the code | `internal/tower/bootstrap.go` (`CreateInvitation`, `ConsumeInvitation`, `IsAdmitted`), `internal/localplane` | Bearer invitations bind K at consume. A `/local/join` route behind the uniform 401, with budgets and the global limiter reused. Station kind attaches with self-reported models. |
| Tower TLS identity | `internal/localplane/serve`, `internal/tower` (`State.TLSKey`) | Serve TLS with a cert self-signed by `tls.key`. Plain http alongside for one release. |
| Client pin | `cmd/rogerai/main.go` (`LanTowers` gains a pin), `internal/lanmode` (`Check`/`IsTower`), `internal/client` LAN transport | Pinned TLS dial on every Tower call (dial, relay, chat, stream, poll). No http fallback once pinned. TOFU once on upgrade. |
| Request and approve | new join-request handler in localplane plus a SAS package; `internal/towercli` (`requests`, `approve`); `internal/tui`, `internal/webui` | Commit-reveal X25519 exchange and the transcript-hash phrase. Pending cap and rate limits. |
| Admin socket | `internal/towercli` (`serve.go`, `main.go`) | A 0600 Unix socket in the data dir, with a peer-uid check. Admin commands go through the running serve, or the direct lock when serve isn't running. |
| Surfaces | `internal/client/lan.go:288` hint, `internal/tui` (Tower row and [3] invite and request panels), `internal/webui` (invite panel and request card), QR encoder under `internal/` | One-line copy per `surfaces.feature`. |
| Edge bearer invite | `internal/edgeauth/local.go`, `cmd/rogerai/edgeenroll.go`, `cmd/rogerai/edge.go` | `roger edge invite` mints a key-less code that admits on use. |
