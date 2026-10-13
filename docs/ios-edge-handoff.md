# iOS Edge handoff: validate the phone against the current Edge build

Written 2026-10-11 for the founder's Mac session. The Go side (branch `wt/simple-join`, test build
`6.10.0-test.e41c3b6b`) now asks nine things of a phone on a Roger Edge. This file is the one place
that says what each ask is, the exact bytes the phone must produce or check, what to change in the
app, and what the founder should see on both screens when it works.

Everything here is pinned to the real Go code in `<this checkout>` and to approved
specs. Nothing is pushed anywhere; this is a local test branch.

Read these alongside this file:

- `docs/ios-enrollment-root-trust.md`: the Swift for the root
  check, written file by file, with the golden vectors. This handoff cites it rather than repeating
  it. Call it "the root-trust doc" below.
- `<roger-ios checkout>/EDGE-HANDOFF.md`: the earlier two-repo handoff (presence, claim loop,
  advertiser). Still accurate for the pieces it covers; superseded where this file says so.

## 1. Purpose and status

The phone is a real Edge member today by the dial-out presence path. Everything that moved since the
earlier handoff concerns how the phone JOINS, and how much it checks before it believes an answer.

| Ask | Phone today (branch `band-radio`) | Spec line | Status |
|---|---|---|---|
| (a) First join verifies the root with a code or an invite | Keeps the first answer, no check | `enrollment_root_trust.feature:353,:362,:1003,:1010` | Not built on iOS. The root-trust doc has the full patch. |
| (b) The claim loop holds answers; never keeps the first one from any authority | `claimOnce` calls `adopted(resp)` on the first 200 | `enrollment_root_trust.feature:463,:474` | Not built. Same patch. |
| (c) A certificate is stored only after it chains to the root, names this node, binds this node key | Stores `cert` and `root` straight from JSON | `enrollment_root_trust.feature:1016` | Not built. Same patch (the SecTrust block). |
| (d) Join by `roger://join?...` invite, pasted or scanned | No parser; the join form takes an address only | `features/join/invite.feature:208,:218`, `:65` (old alias) | Not built. New in this file. |
| (e) Presence posting while the Edge tab is open | Built: `EdgePresence`, 30 s loop, foreground only | `features/edge/presence.feature` | Built, verified live 2026-09-23. Re-check against this build. |
| (f) Appears in the hub's status and fleet with its enrolled name | Built: `nm=` advert, presence carries the name | `features/edge/fleet_window.feature:297,:305` | Built. Re-check the name shows, not the node id. |
| (g) The phone consumes a band from a member over mTLS | Not built; the app has no Edge inference client | `features/edge/addressed_inference.feature:113` (a phone cannot SERVE; consuming is unspecified) | DECISION 1 below. |
| (h) Scan-on-request parity: browse only while the Edge tab is open | Browser runs with `startPinging`, stops on background | `features/edge/scan_on_request.feature:133` (enrolled machines keep browsing) | Mostly matches; see ask (h) for the one gap. |
| (i) Honest phone vitals in the fleet window; asleep is not broken | Presence carries no vitals | `features/edge/fleet_window.feature:297,:305` | Not built. DECISION 2 below. |

What the phone can do against this build without any app change: join by typed address (JOIN BY
ADDRESS) or by adopt-and-claim, post presence, and show up on the hub. The hub will print, at
allow or adopt, that "an older joiner does not check codes" (ruling D9). That warning IS the signal
that asks (a) to (c) are missing on the phone.

What it cannot do: refuse an impostor on first join, read an invite, be served to, or report
vitals.

## 2. Setup on the Mac

### 2.1 The Linux box's binaries and build

The current test build is `<the test build folder>/`. Its checksums:

```
89561b9be2963f0746b8ef94c1ff3d30547c3ea742a68bf4ecb2433b269614cc  roger
88aae7be6385c1328fe8813a1aac593d8205b23cd4a4a920841d03a63c758b38  roger-tower
047ce3b716ad2f944499d7aa520aea0f5d27748e9886cae924f6e677ee5b0c1f  roger-tower-local
fd611f635b39ab5506f450a6e21c08d49e1dca206eb2604cd76922b4fa35f210  rogerai-broker
```

Those are linux/amd64. The authority for the phone test RUNS ON THE LINUX BOX, so the Mac does
not need a Go build for the phone to join. If you want `roger` on the Mac too (a second member, or
to drive `roger edge` from there), build it from the same commit. The branch is not pushed, so
copy the worktree over (Syncthing carries `~/ai`; the worktree is outside it, so `rsync` it or
`git bundle` it), then on the Mac:

```sh
cd <copy of <this checkout>>
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build \
  -ldflags "-s -w -X rogerai.fm/roger/v6/internal/buildinfo.Version=6.10.0-test.e41c3b6b" \
  -o roger ./cmd/rogerai
./roger --version      # roger 6.10.0-test.e41c3b6b
```

`make cross` on Linux lists every target goreleaser releases (darwin amd64 and arm64 are in it)
and proves they all compile.

### 2.2 Which authority the phone joins

Two options. Pick one; do not run both on the same port.

**Option A, the simulated hub published on 39019 (matches the runbook step 17).** On the Linux box:

```sh
export EDGESIM_ROGER=<the test build folder>/roger
export EDGESIM_PUBLISH=1
SIM=tools/edgesim/edgesim
$SIM down
$SIM up 2
$SIM status
```

The hub's authority is then reachable from the LAN at `http://192.168.1.69:39019` (wired) or
`http://192.168.1.183:39019` (wifi). `EDGESIM_PUBLISH_PORT` changes 39019. The hub's root
fingerprint is in `$SIM status` ("LOCAL ROOT") and in `$SIM roger hub edge authority`.

Limits of option A: the hub cannot DIAL the phone (rootless podman), so only the push path
(presence) works, which is the phone's normal path anyway. The hub's TUI is `$SIM tui hub`.

**Option B, this Linux box as the authority** (its own Edge, port 39009 persisted). Scratch config so
the founder's real one is untouched:

```sh
export XDG_CONFIG_HOME=/tmp/rbk/phone-auth
export ROGERAI_NO_UPDATE_CHECK=1
R=<the test build folder>/roger
$R edge authority local shed --yes
$R                       # the TUI; press 3 for EDGE. The authority answers while this runs.
```

The authority answers only while a roger runs here (the TUI or `__edge-agent`). Its port is in
`$XDG_CONFIG_HOME/rogerai/edge/authority-port`.

### 2.3 Network

- LAN addresses of this box: `192.168.1.69` (enp174s0, wired), `192.168.1.183` (wlan0).
- Firewall: nftables `inet filter input` policy is `accept`, nothing blocks 39009 or 39019.
- The phone's VPN must be OFF. It blocked all LAN mDNS in the 2026-09-23 session.
- mDNS does not cross the rootless podman boundary, so with option A the phone's Bonjour browse
  will NOT see the hub; use JOIN BY ADDRESS or a pasted invite. With option B it will.

## 3. The asks

Each ask: the spec it satisfies, the wire format with a worked example, the Swift change, and the
pass criteria.

### (a) First-join root verification with a code or an invite

**Spec:** `features/edge/enrollment_root_trust.feature:353`
(adopted phone keeps its certificate only once the code verifies), `:362` (typed-address join uses
the same code), `:1003` (the Edge tab asks for the code), `:1010` (the join form takes the code),
`:668` (same cap, same stretch, same window as the CLI), `:751` and `:758` (re-claim with and
without a held root), `:1023` (golden vectors).

**Wire.** Unchanged request. The answer to `POST /edge/enroll` and `POST /edge/claim` carries:

| key | meaning |
|---|---|
| `root_proof` | `"v1:" + hex(HMAC-SHA256(key, message))` |
| `root_proof_status` | `"none"` when the authority proves roots but held no live code for this join |

and, NEW since the root-trust doc, a response HEADER `Roger-Edge: <query-escaped name>` naming the
Edge (`internal/edgeauth/enrollhttp/enrollhttp.go:55-57`,
`:147`, `:213`). The body is exactly what an old app knows. Show the name after a successful join
("joined the Edge shed"); it decides nothing. Up to 64 printable characters, else ignore it
(`internal/edgeauth/issue.go:92-98`).

Derivation (`internal/joincode/joincode.go:51-54`, domain
`rogerai-edge-root-proof/v1`, 10 Crockford symbols, PBKDF2-HMAC-SHA256 at 200000 iterations
salted with `domain + "\n" + nonce`, message `domain, rootFP, account, node_id, nonce` joined by
`\n`): the root-trust doc section 1 has it in full and section 2 has the Swift (`EdgeRootProof`).

**Golden vectors:** `internal/edgeauth/testdata/root_proof_vectors.json`
(4 proofs, 13 code transcriptions, 10 pins). Copy it into `RogerAITests/`. The first row:

```
code 7KQ2M-9XH4P  nonce 00112233445566778899aabbccddeeff  account edge-local  node_id n_3f9a1c2b7d4e5f60
root_fp 5f0920045e1af32fd724bc3f3cc640a803ac9a16d30e268081f2fdc08539be4d
proof   v1:a6510a9b012b00d275195f2960cef594e313ce6d250f7ee1bd633c9ffdb9404d
```

**Swift:** the root-trust doc, sections 2 and 3, unchanged. Add: read the `Roger-Edge` header in
`EdgeEnroll.post` and `EdgeClaim.poll`, URL-unescape it, and carry it next to the Response.

**Pass:**

- Authority screen: `roger edge authority allow <phone user key>` (or adopt) prints a code like
  `7KQ2M-9XH4P` and, for an app that has NOT been updated, the line that an older joiner does not
  check codes. For an updated app that line must NOT appear once the app announces itself as
  proof-aware. (There is no version signal in the request today; the warning is printed on the
  authority for every allow. DECISION 3 below.)
- Phone: after the answer, the Edge tab says an Edge answered and asks for the code, shows the
  root's short fingerprint (`XXXX-XXXX-XXXX-XXXX`, the first 16 hex of the root DER's SHA-256,
  upper case), and does NOT draw the phone as a member.
- Wrong code: nothing kept, count shown, 5 wrong codes discards the held answers with the
  impersonation warning.
- Right code: membership saved, presence starts, the authority's `roger edge` shows the phone.

### (b) The claim loop never keeps the first answer from any authority

**Spec:** `enrollment_root_trust.feature:463` (several authorities, keep only the one the code
verifies), `:474` (hold at most 4 answers, earliest received, say how many more).

**Wire:** none. This is app behaviour. `EdgeClaim.poll` must return the nonce it signed with
(`.granted(Response, nonce:)`), because the proof is salted with it.

**Swift:** root-trust doc, section 2, "Claim loop". Today's code at
`<roger-ios checkout>/RogerAI/Services/Edge/EdgeService.swift:192-215` (`claimOnce`, `.granted`
calls `adopted` at line 203) is the exact spot.

**Pass:** run a second authority on the LAN (option B on the Linux box while option A's hub is
published is enough: two authorities, one phone). Adopt the phone on ONE of them. The phone must
hold both answers, ask for the code once, keep the one whose code verifies, and report the other
as "answered as an Edge authority but could not prove its root (offered root XXXX-...)".

### (c) A certificate is stored only after the chain check

**Spec:** `enrollment_root_trust.feature:1016` (same P-256 chain rules as Go).

**Wire:** none. The checks mirror `edge.VerifyPeer`
(`internal/edge/verify.go:129-170`) and `edgeauth.CheckResponse`:

1. the leaf chains to the root in the same answer (SecTrust with the root as the only anchor);
2. the leaf's URI SAN names this phone's `node_id`;
3. the leaf's public key is this phone's node key (a node id IS the hash of its key, so a cert that
   names this node with another key is a misissue).

**Swift:** root-trust doc, section 2, "Chain check". Both `join(authority:name:)` (line 390,
`saveMembership` at 407) and `adopted` (line 249, `saveMembership` at 261) must call it first.

**Pass:** there is no live way to make a real authority hand out a bad chain. Verify with the
XCTest: a Response whose `cert` was issued by a different root, and one whose SAN names another
node id, are both refused with nothing written. Then a real join still succeeds.

### (d) Join by `roger://join` invite, pasted or scanned

**Spec:** `features/join/invite.feature:208` (a device joins an
Edge with `roger join`), `:218` (`roger edge invite` needs no joiner key first: a BEARER invite),
`:65` (the old `rogerai-edge://join?...` alias still accepted), `:80` (Edge codes keep the
10-symbol format), `:39` and `features/join/surfaces.feature:52`
(the QR encodes the same line).

**Wire, the invite** (`internal/joininvite/joininvite.go:3-8`,
`:26-47`, parse at `:79+`):

```
roger://join?kind=edge&at=<host:port>[&at=...]&code=<10 Crockford symbols>&fp=sha256:<64 hex>[&for=<name>]
```

A real one, minted on this build with `roger edge invite --at 192.168.1.69:39009`:

```
roger join 'roger://join?kind=edge&at=192.168.1.69%3A39009&code=0EYJB3368T&fp=sha256%3A7bec85b113edac16b1a1603adab93c3802d3d57197dd73e0b0b13d1578f8f776'
works once, for the next 15 minutes. root 7BEC-85B1-13ED-AC16 - the device shows the same when it has joined.
```

The QR encodes the whole `roger join '...'` line, not just the URL
(`internal/towercli/invite.go:107`, `cmd/rogerai/edgeinvite.go:57`). The app should accept the
URL with or without that `roger join` wrapper and quotes.

Parse rules, each a refusal with a reason (`joininvite.go` lines noted):

- scheme must be `roger` (or legacy `rogerai-edge`, read as kind edge with `root=` for the pin);
- `kind` must be `edge` for the phone (a Tower invite is refused on the phone: "that invite is
  for a Tower, open it on a computer");
- at least one `at`, each a `host:port`;
- `code` canonicalised with the Crockford rule (I/L to 1, O to 0, case folded, dashes dropped,
  length 10);
- `fp` must be `sha256:` plus exactly 64 hex; the pin is the FULL fingerprint, never the short one.

**Wire, the join itself.** Two things differ from a plain enroll:

1. The request gains `join_proof` (`internal/edgeauth/request.go:45`),
   the device's proof that it holds the invite's code, in the `rogerai-edge-join/v1` domain
   (`joincode.go:60-63`, same 10 symbols, same 200000 iterations, salted with the request nonce):

   ```
   domain  = "rogerai-edge-join/v1"
   key     = PBKDF2-HMAC-SHA256(utf8(code), utf8(domain + "\n" + nonce), 200000, 32)
   message = utf8(domain + "\n" + rootFP + "\n" + user_key + "\n" + node_id + "\n" + node_key)
             (rootFP is the invite's fp WITHOUT the "sha256:" prefix, lowercase hex)
   join_proof = "v1:" + hex(HMAC-SHA256(key, message))
   ```

   (`internal/edgeauth/bearer.go:27-33`). The MAC message is the domain line, the fields, and the
   nonce appended by `joincode.mac`; read `joincode.go:152-160` for the exact byte layout, which
   is the same shape the root proof uses. The authority admits the request's `user_key` to its
   allow-list the moment the proof matches, and spends the code (`local.go:196-215`). Wrong
   proofs count against every live bearer code; 10 wrong voids them (`bearer.go:17-19`).

2. The answer's `root_proof` is in the ROOT-PROOF domain, proven with the same code
   (`local.go:288-292`). So the phone verifies the answer exactly as in ask (a), using the code
   from the invite. The phone also checks the answer's root hashes to the invite's `fp`; both must
   hold.

So a pasted invite is: parse, POST `/edge/enroll` to `at` with `join_proof`, verify `root_proof`
with the code AND `fp` against the root, chain-check, keep. No typing beyond the paste.

**Swift, new:** `RogerAI/Services/Edge/EdgeInvite.swift` (parse per the rules above, plus the
legacy scheme), a `joinProof(code:fp:request:)` next to `EdgeRootProof` reusing its PBKDF2 and
HMAC, and a "Paste an invite" field on the Edge tab (above JOIN BY ADDRESS) that reads the
clipboard. QR scanning: `AVCaptureMetadataOutput` with `.qr`, feeding the same parser. Ruling D12
made QR a later UX slice for the CLI; on the phone a scan is the natural input, so build it if it
fits the session. Mark the Linux-side `edge invite` output as the source.

**Golden vectors:** `internal/edgeauth/testdata/join_proof_vectors.json`,
beside the root-proof vectors, computed independently of Go by
`internal/edgeauth/testdata/gen_join_proof_vectors.py` (Python
`hashlib` only; re-run it to regenerate). Nine rows: a normal join, the two boundary codes, a
different root, node key and user key, the same proof on a new nonce (a replay), a code typed with
confusables, and one row marked `verifies: false`: a correct root-proof-domain computation over
the join fields, which must NEVER verify as a join proof. Each row carries the stretched `key` as
well as the `join_proof`, so a Swift port can tell a PBKDF2 slip from an HMAC slip. The Go side
pins every row byte for byte, both directions of the cross-domain refusal, and the
`sha256:`-prefix strip (`go test ./internal/edgeauth -run Join`); the Swift port must do the same
against the same file.

**Pass:**

- Linux: `roger edge invite` (option B) or `$SIM roger hub edge invite --at 192.168.1.69:39019`
  (option A) prints the line and a QR.
- Phone: paste or scan; it shows "joining the Edge <name> at <host:port> (root XXXX-...)", then
  "joined". Authority's `roger edge` shows the phone under its enrolled name.
- Second use of the same invite on another phone or config: refused ("already used, ask for a
  new one"). A wrong `fp` (edit one hex character): refused before anything is kept, "the Edge
  that answered is not the one this invite names".
- The old `rogerai-edge://join?...` form, if any older invite is around, still works.

### (e) Presence posting while the Edge tab is open

**Spec:** `features/edge/presence.feature` (dial-out presence),
built on both sides.

**Wire:** `POST /edge/present` on the authority (`internal/edge/presence.go:29`), canonical
`rogerai-edge-present/v1` (`:32`), node-key signed, freshness plus or minus 5 minutes, nonce
replay-guarded until TS plus window (`:217-219`). There is also a `v2` prefix at `:70` that
carries `PhoneVitals` (`:61`); see ask (i).

**Swift:** built (`EdgePresence.swift`; the 30 s loop at `EdgeService.swift:92-127`, foreground
only, stops on background at `:283-286`).

**Pass:** on the authority's `roger edge` (or `$SIM status`) the phone's row says `last seen Ns
ago` and refreshes about every 30 s while the Edge tab is in front. Background the app: within
about a minute the hub marks it dark. Foreground: it returns. The runbook's cut-and-heal timings for
Pis (20 to 40 s) are the comparison.

### (f) The enrolled name in status and fleet

**Spec:** `features/edge/discovery.feature` naming scenarios (added 2026-10-10: members list
machines under their enrolled names, hardware name only as a fallback), and
`features/edge/fleet_window.feature:103` (an Edge with a PC, a Pi and a phone).

**Wire:** the enroll `name` field, the advert's `nm=` TXT key, and presence's `name`. All built.

**Pass:** `$SIM status` and `$SIM fleet` show the name typed on the phone (or its callsign), not
`n_...`, with kind `phone` or `mobile`. In the TUI [3] EDGE the row reads the same.

### (g) The phone consuming a band from a member over mTLS

**Spec:** `features/edge/addressed_inference.feature:113-114` says a phone cannot yet SERVE a
model to the Edge, and must be told so honestly. Nothing approved says a phone CONSUMES one.

**Wire, if built:** `POST /edge/infer` on the member's face (`internal/edge/infer.go`), mTLS
where the phone presents its member certificate; the face checks it chains to the Edge root and
names a member (`caller()` at `:106-131`), answers OpenAI-shaped chat with a local receipt
(`:194`), and `/edge/speech` the same way with a 64 KiB text cap (`speech.go:25,:36,:43`). The
phone would need to serve its Ed25519 member cert as a TLS client identity; whether
`Network.framework` accepts an Ed25519 `SecIdentity` is the unverified platform risk noted in
memory for the pull path, and it applies to the client side too.

**DECISION 1 (founder):** should the phone consume Edge bands in this slice? Options: (i) not
now, the phone is a presence-and-adopt member only (recommended for this validation: nothing
approved requires it, and the TLS identity question is open); (ii) spec it first as
`features/edge/phone_consume.feature` and build after; (iii) build it now against `/edge/infer`.
Until ruled, the Mac session should only CONFIRM the platform question: can the app open a TLS
connection presenting the member cert's P-256 key as its client identity, and does the member's
face accept it (`caller()` returns the node id). That one probe settles option (iii)'s feasibility.

### (h) Scan-on-request parity

**Spec:** `features/edge/scan_on_request.feature`. For a
machine NOT in an Edge: browse only while the Edge screen is open, nothing in the background. For
an enrolled machine (`:133`): it keeps browsing in the background. The Go rule is
`edgeMayBrowse` (`cmd/rogerai/edgescan.go:108-121`): enrolled, or an on-request scope is open.

**Phone today:** the browser starts with `startPinging()` (the user's tap) and stops on background
(`EdgeService.swift:130-140`, `:283-299`). That matches the unenrolled rule. The gap: once the
phone IS a member, Go would keep browsing in the background, and iOS cannot (the app is
suspended). That is a platform difference, not a bug; presence already covers membership without
a browse.

**Pass:** fresh install, Edge tab NOT open: the phone makes no mDNS traffic (watch with
`$SIM roger hub edge scan` seeing nothing from the phone, or `tcpdump -i wlan0 udp port 5353` on
the Linux box filtering the phone's address). Open the tab: its advert appears within a scan
pass. Leave the tab: the advert stops.

**DECISION 4 (founder):** record in `scan_on_request.feature` that a phone member browses only
while its Edge tab is in front, as the honest iOS form of "enrolled machines keep browsing".
Recommended: yes, one scenario, `@ios`.

### (i) What the fleet window asks of a phone

**Spec:** `features/edge/fleet_window.feature:297` (a phone reports battery, charging, thermal
state, whether the app is in front; no CPU load or temperature, because iOS gives none; absent
drawn as absent), `:305` (a suspended phone is "asleep, last heard at", not broken, and raises no
offline alert until the owner's rule says so).

**Wire:** presence `v2`, canonical prefix `rogerai-edge-present/v2`
(`internal/edge/presence.go:70`), which signs the vitals too. `PhoneVitals` (`:61-67`) is the only
addition to the body, every field optional:

```
"battery_pct": 0..100, "charging": bool, "thermal": "" | "nominal" | "fair" | "serious" | "critical", "foreground": bool
```

Any other `thermal` word is refused (`:72`).

**Swift:** fill `PhoneVitals` from `UIDevice.current.batteryLevel` and `batteryState`,
`ProcessInfo.processInfo.thermalState`, and the scene phase, and sign the `v2` canonical form.
Until built, keep sending `v1`; the authority accepts both.

**DECISION 2 (founder):** build vitals in this Mac session, or after asks (a) to (d)? Recommended:
after, it is additive and the fleet window already draws absence honestly.

**Pass:** `$SIM fleet` shows the phone's battery and "in front" or "asleep", and no temperature
column for it.

### Summary of founder decisions in this file

1. Phone consuming Edge bands: not now (recommended), spec first, or build now.
2. Phone vitals in presence v2: this session or after.
3. Proof-aware signal: should an updated app tell the authority it checks codes, so the D9
   warning stops printing for it? Today there is no such field. Recommended: add an optional
   request field `checks_root: true` on the Go side later; for the Mac session, expect the warning
   and ignore it once the phone demonstrably verifies.
4. Record the iOS form of background browsing in `scan_on_request.feature`.
5. Sighting flows (section 3b): ship the invite flow plus the camera scanner in the first Mac
   session and request-and-approve in the second (recommended), or both at once.

## 3b. Joining from a sighting (ruling 2026-10-12)

The founder started the app, it pinged, and the authority's screen said `Nearby gentle-ibex-14` with
the old `roger edge adopt 8eda6e1a92c9` hint. Detection works; the step from a sighting to a join did
not. Ruling 2026-10-12: from a sighting on [3] EDGE the authority offers BOTH

- `i`: mint an invite for that device and show the QR plus the `roger join` line, and
- `a`: request-and-approve, the matching phrase on both screens and `y` to approve.

Nobody types an address. The Go side of `i` and `a` on a sighting is BUILT on `wt/simple-join`
(TUI, CLI and console; `features/edge/empty_edge_simple.feature` section 7); this section says what
the phone must do for each and what is already there on the Linux side.

### 3b.1 The two flows from the phone's side

**Invite from a sighting (`i`):**

| Step | Phone | Authority's screen ([3] EDGE) |
|---|---|---|
| 1 | Edge tab in front, "Start pinging" (the advert, `EdgeAdvertiser.swift:44`) | `NEARBY  gentle-ibex-14  phone  i invite  a approve` |
| 2 | nothing | owner presses `i`: the QR and the `roger join '...'` line, "works once, for the next 15 minutes" |
| 3 | "Scan invite" (3b.5) or paste the line; the app shows "join the Edge <name>, root XXXX-XXXX-XXXX-XXXX?" and one Join button | unchanged |
| 4 | POST `/edge/enroll` with `join_proof` (ask d); verify `root_proof` and the invite's `fp`; chain check; keep | `gentle-ibex-14 joined` on the status line; the row leaves NEARBY and is drawn as a member |
| 5 | Edge tab shows member state; presence starts | the phone's row under its enrolled name |

**Approve from a sighting (`a`) on a PHONE.** A phone advertises no certificate, so on its row `a`
grants its claim and shows the one-time code, exactly the adopt-and-claim path of asks (a) and (b)
(spec scenario "a on a claim-only device grants its claim"). The phone's claim poll is answered;
nothing new is needed on the phone for this.

**Approve by asking (request-and-approve, the phrase).** For a roger device on the row, `a` waits
for it to ask. A phone that ASKS on its own (the app's "Ask to join", 3b.3) appears on the owner's
screen as a join request with the phrase, answered with `y` or `n`, whether or not anyone pressed `a`:

| Step | Phone | Authority's screen |
|---|---|---|
| 1 | Edge tab in front, pinging | `NEARBY  gentle-ibex-14  mobile · seen 3s ago · key 8eda6e1a92c9` |
| 2 | "Ask to join": `pair/start` (commit), then `pair/reveal` (3b.3) | `gentle-ibex-14 wants to join · amber-river-71 · y approves, n refuses` |
| 3 | shows `amber-river-71` and "the owner should see the same phrase; waiting for the owner (1m59s)" | the owner compares and presses `y` |
| 4 | `pair/wait` answers `approved`; the phone then enrolls (POST `/edge/enroll`, signed with the SAME key it paired with), checking the chain against the root the phrase covered; keeps | `gentle-ibex-14 joined` |

Where the authority learns the phone's address: from the advert the phone already sends (its `port`
is the phone's TLS face, `EdgeAdvertiser.swift:107`). The pairing itself is phone-initiated, so the
authority never needs to dial the phone; option A (the published simulated hub) can therefore run the
approve flow too, as long as the phone can reach the hub's address.

### 3b.2 Invite flow: what changes against ask (d)

Everything in ask (d) stands: the parser, the bearer `join_proof` in `rogerai-edge-join/v1`, the
`root_proof` check, the `fp` check, the chain check, and the golden vectors. Two things are new.

**The invite is bound to the device (BUILT).** Option 1 of the earlier draft is what was built:
bind by node id. The advert stays key-free (`internal/edge/wire/advert.go`: `id`, `acct`, `kind`,
`caps`, `port`, `fp`, `auth`, `nm`); `id` derives from the node key, and the join proof already
covers `node_id`, so the binding needs nothing new from the phone.

- `i` on a sighting, `roger edge invite --for <node id>` and the console's Invite all mint through
  one function (`edgeMintInvite`, `cmd/rogerai/edgeinvite.go`), which calls
  `MintBearerCodeFor(<node id>)` (`internal/edgeauth/bearer.go`) and writes
  `roger://join?kind=edge&at=<host:port>&code=<10 symbols>&for=<node id>&fp=sha256:<64 hex>`.
  For a phone (no certificate) it also grants the phone's claim, so the claim poll the app is
  already running is answered too.
- The authority refuses a correct proof from any other node id: HTTP 403 with the body
  `that invite is for another device` (`edgeauth.ErrInviteForOther`, `enrollhttp.go` `writeAuthError`).
  The invite is NOT spent by that refusal: the device it was minted for can still use it. The app
  should show "that invite is for another device: ask for one for this one" (the CLI's words,
  `look.JoinSaysForOther`).
- The phone's side: when an invite carries `for=`, compare it to the phone's own node id before
  sending anything, and say the same line if they differ (no request needed). Then the flow is
  ask (d) unchanged: parse, prove, verify, keep.
- Risk accepted (founder-visible): a node id is public, so a device that forges the same id AND
  holds the code could join; the code is the secret, the id only narrows it.

### 3b.3 Approve flow: the SAS exchange the phone must implement

This is the request-and-approve pairing of `features/join/approve.feature` (built on the Go side
for Towers and for an Edge authority, `approve.feature:49-54`; the Edge's own `j` prompt reuses it,
`cmd/rogerai/edgejoinprompt.go:42-70`). The math is `internal/joinsas/joinsas.go`, which imports
nothing from the rest of roger, so it ports cleanly.

**Routes (CONFIRMED).** The Edge authority serves the same three routes as a Tower, on its
authority listener (plain HTTP on the LAN, the same `host:port` as `POST /edge/enroll`,
`POST /edge/claim` and `GET /edge/root`; the advert's authority address, `http://<lan ip>:<port>`).
`cmd/rogerai/edgehost.go` mounts them at `/local/pair/`, and the wire is written down, for citing,
at `internal/localplane/pair.go:40-58`. Pinned by `cmd/rogerai/edgepair_routes_test.go`.

```
POST /local/pair/start   {"kind":"client","name":"<display name>","client_eph":"<64 hex>","commit":"<64 hex>"}
   -> 200 {"id":"<16 hex>","tower_eph":"<64 hex>","tower_nonce":"<64 hex>","tower":"<authority name>","seconds":120}
POST /local/pair/reveal  {"id":"<id>","client_nonce":"<64 hex>"}
   -> 200 {"waiting":<seconds left>}            (a nonce that breaks the commitment voids the pairing: 401)
POST /local/pair/wait    {"id":"<id>"}           (long poll, held open up to 25 s; call again on "pending")
   -> 200 {"status":"pending"|"approved"|"refused"|"expired","answer":{"joined":"edge","tower":"<name>"}}
```

`wait` is a POST with a JSON body, not a GET with a query (the earlier draft was wrong). `models` is
optional and only for a station. A client whose key is already admitted gets
`{"already_member":true,"tower":"<name>"}` from `start` and no request is made.

**Every call is signed**, with the same ed25519 key each time (`internal/protocol/auth.go`):

```
X-Roger-Pubkey: <hex ed25519 public key>      (this key is the phrase's ClientKey)
X-Roger-TS:     <unix seconds, within 5 minutes of the authority's clock>
X-Roger-Nonce:  <32 lowercase hex, fresh per request; REQUIRED on these routes>
X-Roger-Sig:    hex( ed25519.sign( "POST\n" + path + "\n" + ts + "\n" + hex(sha256(body)) + "\n" + nonce ) )
```

A bad, stale or replayed signature, an unknown id or a broken commitment is one uniform
`401 {"error":"unauthorized"}`; a sixth `start` from one address within a minute is `429` with
`Retry-After: 60`; a ninth pending request is `503`. On an Edge authority, `TowerKey` in the phrase is
the root fingerprint: lowercase hex SHA-256 of the root certificate's DER, read first from
`GET /edge/root` (that is what `roger join <address>` does, `cmd/rogerai/edgejoinprompt.go`).
Approving puts the asking key on the authority's allow-list as a USER key, so after `approved` the
phone enrolls (`POST /edge/enroll`) with `user_key` = that same signing key, pinning that root.

**The exchange, in order, from the phone:**

1. Make a fresh X25519 keypair (CryptoKit `Curve25519.KeyAgreement.PrivateKey`) and a 32-byte nonce
   `n_c` (hex). Compute `commit = hex(SHA256(utf8("join-sas/v1" + "/commit\n" + n_c)))`
   (`joinsas.go:60-63`).
2. `pair/start` with `client_eph` and `commit`. The answer carries `tower_eph`, `tower_nonce` (hex)
   and `id`. The phone has now committed BEFORE it saw the other nonce; that order is the whole
   defence (`approve.feature:65-68`, `:83-86`).
3. `pair/reveal` with `n_c`. The authority checks `commit`; a mismatch voids the pairing.
4. Compute the phrase on the phone, exactly as the authority does (`joinsas.go:74-90`):

   ```
   fields  = [ "join-sas/v1", clientKey, towerKey, clientEph, towerEph, clientNonce, towerNonce ]
   h       = SHA256( for each field: uint32 big-endian length(field) || utf8(field) )
   v       = uint64 big-endian of h[0..8]
   w1 = v>>53 & 2047,  w2 = v>>42 & 2047,  w3 = v>>31 & 2047,  digits = (v & (1<<31 - 1)) % 100
   phrase  = words[w1] + "-" + words[w2] + "-" + words[w3] + "-" + two-digit(digits)
   ```

   `clientKey` is the hex public key the phone signs the pairing requests with (`X-Roger-Pubkey`),
   which is the `user_key` it then enrolls with; `towerKey` is the authority's root fingerprint from
   `GET /edge/root` (hex, no `sha256:`), the ephemerals are the raw
   32-byte X25519 public keys in hex, nonces as sent. Every field is length-prefixed, so no field
   can bleed into the next. `words` is the BIP-39 English list shipped as
   `internal/joinsas/words.txt` (2048 words, SHA-256
   `2f5eed53a4727b4bf8880d8f3f199efc90e58503646d9ff8eff3a2ed3b24dbda`, `joinsas.go:27`); bundle the
   same file in the app and check its hash on load.
5. Show the phrase. Poll `pair/wait`. On `approved`, enroll (`POST /edge/enroll`, signed by the
   same key) and run the root-trust checks of asks (a) and (c) against the ROOT THE PHRASE COVERED
   (`towerKey`), not a root read later from the wire, then keep. On `refused`: "the owner refused". On `expired` (2
   minutes, `approve.feature:96-100`): "did not answer in 2 minutes; ask the owner to open roger, or
   use an invite".

**Limits the phone will meet** (`approve.feature:110-126`): one pending request per client (asking
again replaces the earlier request and makes a new phrase, so do not retry in a loop); at most 8
pending per authority ("busy with other join requests, try again in a minute"); 5 requests per
address per minute, the sixth gets a retry-after. A request grants nothing until approved
(`:128-131`): the phone keeps nothing before `approved`.

**Worked example (vectors DONE).** `internal/joinsas/testdata/sas_vectors.json`: six transcripts
(transcript fields in, phrase out, each with its `client_commit`), the word list's SHA-256, the exact
derivation in words, and one wrong-domain phrase that must NOT match. Generated with python3
hashlib independently of the Go package, and checked against it by
`internal/joinsas/vectors_test.go`. Port the Swift check against this file before any live pairing;
in the Mac session the phone's phrase must still equal the authority's for the same pairing.

**UI on the phone:** one card: the phrase large, "the owner should see the same phrase; if it
differs, someone is in the middle: do not continue" (`approve.feature:137-140`), a countdown from
`seconds`, and the three end states (joined, the owner refused, did not answer in 2 minutes) each
with one next step. No typing.

**DECISION 5 (recommended: invite first).** Ship the invite flow (3b.2 plus the scanner in 3b.5)
in the first Mac session and leave approve for the second. Invite needs the parser and a proof the
root-trust doc already specifies in Swift; approve needs the whole SAS stack (X25519 agreement,
commit/reveal, the word list, the three routes, the long poll) and has no offline vectors yet.
For a phone the authority's `a` is the claim path (no asking needed), and its `i` is the bound
invite, so a phone that never implements asking still joins from a sighting both ways.

### 3b.4 What the ping-and-adopt code becomes

Keep, as is:

- `EdgeAdvertiser.swift` (the advert) and `EdgeDiscovery.swift` (the browse): they ARE the sighting.
  Keep `acct` empty while unenrolled (`EdgeAdvertiser.swift:9`), that is what makes the phone a
  candidate on the authority's screen.
- `startPinging` / `stopPinging` and the foreground/background handling
  (`EdgeService.swift:130-137`, `:283-301`): unchanged, and they are what ask (h) measures.
- `EdgeClaim.swift` and `claimOnce` (`EdgeService.swift:192-216`): keep for adopt-and-claim with a
  code (asks a and b; the root-trust doc's hold-then-ask patch applies here). On the Go side `a` on a
  phone's row IS this path; `roger edge approve <id>` is its command, and `roger edge adopt` stays
  one release as an alias that prints "adopt is now: roger edge invite --for <id>, or roger edge
  approve <id>".

Change:

- `EdgeView.swift:203-209`, the "ON YOUR ROGER MACHINE  roger edge adopt <id>" card: replace the
  copy with "On your Roger machine, open Edge and press i to invite this phone, or a to approve it"
  (the terminal equivalents: `roger edge invite --for <id>`, `roger edge approve <id>`),
  keep the node id visible (short form) since the owner's screen names the device by it. The Copy
  button copies the id only.
- `EdgeView.swift:306-339`, "JOIN BY ADDRESS": keep under Advanced as the fallback for a hub the
  phone cannot see (option A), reworded "Only needed if this phone can't see your Edge on this
  network".

Delete nothing yet. After the second session (approve built), `join(authority:name:)`'s
address-only enrol path (`EdgeService.swift:390`) becomes the fallback only and can lose its
first-answer trust, which the root-trust doc already removes.

### 3b.5 Camera QR scanner for invites (founder addition 2026-10-12)

The app has no scanner today: no `AVCaptureMetadataOutput`, `DataScannerViewController` or VisionKit
use anywhere under `RogerAI/`, and the shipping `RogerAI/Resources/Info.plist` has NO
`NSCameraUsageDescription` (it has microphone, speech, calendar, reminders and local-network strings
only). A side worktree (`.claude/worktrees/ios-operator-status`) carries a camera string for a
different feature ("Scan a QR code to move your connection details to another device"); do not
reuse that wording.

**Build:**

1. A "Scan invite" button on the Edge tab, next to "Start pinging" when the phone is not a member,
   and the same button inside Advanced beside "Paste an invite". Native path: VisionKit
   `DataScannerViewController` (iOS 16+) with `recognizedDataTypes: [.barcode(symbologies: [.qr])]`,
   `isHighlightingEnabled: true`, falling back to `AVCaptureSession` + `AVCaptureMetadataOutput`
   (`.qr`) where `DataScannerViewController.isSupported` is false (older devices, the Simulator).
2. `Info.plist` (and `project.yml`, since xcodegen generates it): `NSCameraUsageDescription` =
   "Scan the QR code a Roger machine shows to join its Edge. Nothing is recorded." Permission
   denied: the scanner view is replaced by one line, "Camera is off for RogerAI. Paste the invite
   line instead, or allow the camera in Settings", with a Paste field and an Open Settings button.
3. Accept only `roger://join?...` (and the legacy `rogerai-edge://join?...`) payloads. The scanner
   hands the raw string to the SAME parser as pasted invites (`EdgeInvite.swift`, ask d), after
   stripping a `roger join '...'` wrapper if the QR carries the whole line (it does,
   `cmd/rogerai/edgeinvite.go:57`). Anything else: one line, "that code is not a Roger invite",
   keep scanning. Specifically an `http://` or `https://` link is refused the same way, never
   opened.
4. Before sending anything: show what it is about to join, "join the Edge <name>, root
   XXXX-XXXX-XXXX-XXXX? works once, for the next N minutes" (the short fingerprint from `fp`, the
   `at` host, the kind), with one Join button. A Tower invite (`kind=tower-client` or
   `tower-station`) on the Edge path is refused here with "that invite is for a Tower, open it on a
   computer" (ask d's rule), with no Join button.
5. On Join: the invite flow of 3b.2 (proof, root_proof, fp, chain check). On success the camera
   closes and the Edge tab shows the member state. On the authority's refusal the sheet shows the
   authority's wording verbatim: a used code, "that invite was already used. Ask the owner for a new
   one: roger edge invite"; an expired one, the expired line (the authority answers "no live code"
   for both and the host maps it to one line, `cmd/rogerai/edgejoinprompt.go`; show that line).
6. Adversarial, each a test: a `tower-client` QR on the Edge path is refused with the Tower
   message; a QR whose code was already used gets the authority's used/expired wording and keeps
   nothing; an `https://rogerai.fm/...` QR is refused as not an invite and never opened; a QR with
   `fp` edited by one hex character is refused before anything is kept ("the Edge that answered is
   not the one this invite names").

**Pass:** the owner presses `i` on the phone's row (or runs `roger edge invite`); the phone scans
the QR from the terminal; the confirm sheet names the Edge and its short root; Join completes with
no typing; the authority's screen says the phone joined. Then: the owner mints a Tower invite
(`roger tower invite` on a Tower, or any `kind=tower-client` QR) and the phone refuses it with the
Tower message.

### 3b.6 Pass criteria for the Mac session (both flows)

Host commands run on the Linux box. Use option B (this box as the authority, `/tmp/rbk/phone-auth`,
section 2.2) so the phone's Bonjour sees it and the sighting appears; option A works for the join
itself but the sighting does not cross the podman boundary.

**Step S1. The sighting.**
Run (host): `XDG_CONFIG_HOME=/tmp/rbk/phone-auth ROGERAI_NO_UPDATE_CHECK=1 <the test build folder>/roger`,
press 3. Run (phone): Edge tab, Start pinging.
You should see (host): the phone under NEARBY by its callsign (`gentle-ibex-14`), kind phone,
with `i invite · a approve` above the rows, and `mobile · seen Ns ago · key <12 hex>` on the phone's.
The same list: `roger edge nearby` (it prints the whole node id and the two commands). If instead the
row says `roger edge adopt ...`: the build predates the sighting work; use the newer test build.

**Step S2. Invite from the sighting (3b.2, 3b.5).**
Run (host): select the phone's row, press `i`. Run (phone): Scan invite, point at the terminal.
You should see (phone): the confirm sheet with the Edge's name and root; Join; "joined". (Host): the
row moves from NEARBY to members, named, and the status line says it joined. Then scan the same QR
on a second phone or after Leave: "already used".

**Step S3. Binding (`for=` is built).**
Run (host): `i` on phone A's row; scan that QR on phone B. You should see (phone B): refused, "that
invite is for another device: ask for one for this one" (the app's own check of `for=`, or the
authority's 403). Then scan the same QR on phone A: it still joins (the refusal did not spend it).

**Step S4. Approve: the claim, then asking (3b.3; asking in a second session if DECISION 5 is taken).**
Run (host): `a` on the phone's row. You should see (host): the claim granted and its code; (phone)
the claim poll answered, the code prompt, "joined". Then, with asking built, on a fresh phone config:
Run (phone): "Ask to join" on the Edge tab.
You should see: the same phrase on both screens; (host) `y`; (phone) "joined"; (host) the row named.
Then: `a` again on a fresh phone config and press `n`: (phone) "the owner refused", nothing kept.
Then: `a`, and let 2 minutes pass: (phone) the did-not-answer line; (host) the request gone.

**Step S5. Tower invite on the phone.**
Run (host): any `roger tower invite` QR. Run (phone): Scan invite.
You should see: "that invite is for a Tower, open it on a computer", no Join button.

**Step S6. Camera denied.**
Run (phone): Settings, RogerAI, Camera off; Scan invite.
You should see: the one-line fallback with Paste and Open Settings; pasting the line still joins.

Paste back, per step: S1 the host's NEARBY row text; S2 the phone's confirm sheet text and the
host's joined line; S3 phone B's refusal or the note that it joined; S4 both phrases verbatim for
one pairing, and the refused and expired lines; S5 the refusal; S6 the fallback line.

## 4. Validation checklist for the Mac session

Each step stands on its own. Host commands run on the Linux box (`192.168.1.69`); phone steps in
the app. Keep every paste block as written; a long line that wraps in your terminal and errors with
"command not found" was split by the terminal, paste it again.

### Step 0. Build the app from `band-radio` with the asks (a) to (d) applied

**Why.** Everything below measures the updated app. The current `band-radio` build will join, but
cannot pass steps 2 to 5.

**Run (Mac):**
```sh
cd <roger-ios checkout>
xcodegen generate
# apply the root-trust doc's section 2, and EdgeInvite.swift from ask (d) above
xcodebuild -scheme RogerAI -destination 'platform=iOS Simulator,name=iPhone 16' test
```

**You should see:** `EdgeRootProofTests` green on all 4 proof rows and 13 code rows, the new
`EdgeInviteTests` green, and the existing 38 Edge tests still green. Then install on the device.

**If instead you see:** a vector mismatch: the PBKDF2 salt is `domain + "\n" + nonce` and the
message joins with `\n`; the root fingerprint hashes the DER, not the PEM text.

### Step 1. The authority is reachable

**Why.** Rules out network before anything else.

**Run (host, option A):**
```sh
export EDGESIM_ROGER=<the test build folder>/roger
export EDGESIM_PUBLISH=1
SIM=tools/edgesim/edgesim
$SIM down && $SIM up 2 && $SIM status
curl -s -o /dev/null -w '%{http_code}\n' http://192.168.1.69:39019/edge/root
```

**You should see:** `hub`, `pi-1`, `pi-2` in status, and `200` from curl (the public root, a PEM).
From the phone's browser, `http://192.168.1.69:39019/edge/root` shows the same PEM.

**If instead you see:** connection refused from the phone but 200 locally: the phone is on a VPN
or a different network. `000` locally: `EDGESIM_PUBLISH` was not exported before `up`.

### Step 2. Join by invite (ask d, with a and c)

**Why.** The one-paste path, and it exercises the root proof, the bearer proof and the chain
check in one go.

**Run (host):**
```sh
$SIM roger hub edge invite --at 192.168.1.69:39019
```

**Run (phone):** Edge tab, "Paste an invite" (or scan the QR from the host terminal), Join.

**You should see:**
- Phone: "joining the Edge hub at 192.168.1.69:39019 (root <the 16 hex the host printed>)", then
  "joined". Status: member, presence live within 30 s.
- Host: `$SIM status` lists the phone by its name, `lan`, seen seconds ago.

**If instead you see:**
- "that invite was already used": mint a new one (single use).
- "the Edge that answered is not the one this invite names": the `fp` and the root disagree.
  Either the invite was retyped, or something else answers on that port. Stop and check.
- "that authority does not prove its root": the hub is running an older roger; `EDGESIM_ROGER`
  was not exported before `up`.

### Step 2b. Scan the invite with the camera, and join from a sighting (section 3b)

Run the six steps S1 to S6 of section 3b.6 here, between the invite join and the adopt flow. S2
(scan the QR the authority prints for `i`, join with no typing) and S5 (a Tower invite QR refused
on the Edge path) are the pass marks for the scanner; S4 only if request-and-approve is in this
session (DECISION 5).

### Step 3. Adopt and claim with the code (asks a, b)

**Why.** The no-typing path: the owner adopts, the phone claims, the code verifies the root.

**Run (phone):** reset the Edge membership (Leave), then "Start pinging". Needs option B (same
LAN, Bonjour visible), or option A plus the authority address typed under JOIN BY ADDRESS to
give the claim loop a target.

**Run (host, option B TUI):** press 3, select the phone under DISCOVERED, press `a` to adopt. The
screen prints a 10-symbol code.

**You should see (phone):** "An Edge answered. Enter the code shown where this phone was adopted",
with the root's short fingerprint matching the host's `roger edge authority` line; NOT a member
yet. Type the code: member, presence live.

**Then, the impostor (ask b):** keep option A's hub published AND run option B. Adopt the phone on
option B only. Both answer the claim. The phone must ask for the code once, keep option B's
answer, and name option A's hub as "could not prove its root (offered root 160B-EA72-...)" or
whatever its fingerprint is.

**If instead you see:** the phone drawn as a member before any code is typed: ask (b) is not
applied; the old `adopted(resp)` call is still in `claimOnce`.

### Step 4. Wrong codes and the cap (ask a)

**Run (phone):** at the code prompt, type a wrong code 5 times.

**You should see:** each wrong code counts ("4 attempts left"), a malformed one (a `U`, or 9
symbols) is refused as "not a code" and does NOT count, and the fifth wrong code discards the
held answers with: "the code did not match 5 times: a device on your network may be impersonating
the authority. Nothing was kept; adopt this phone again for a fresh code."

### Step 5. Presence and the fleet (asks e, f, i)

**Run (host):**
```sh
$SIM status
$SIM fleet          # q quits
```

**You should see:** the phone's row under its name, `last seen` refreshing about every 30 s while
the Edge tab is in front. Background the app for 90 s: the row goes dark or asleep. Foreground:
back within one presence period. With ask (i) built: battery and "in front/asleep" on its row, no
temperature.

### Step 6. Scan only while the tab is open (ask h)

**Run (host):**
```sh
sudo tcpdump -ni wlan0 'udp port 5353 and host <phone address>' -c 20
```
(the phone address is in Settings, Wi-Fi.) Phone: Edge tab closed, wait 60 s; then open it; then
close it.

**You should see:** no packets while closed, packets after opening, none again after closing.

### Step 7. Platform probe for being served (decision 1)

**Run (Mac, a throwaway test):** open a TLS connection to a member's face (pi-1's address and
port from `$SIM roger pi-1 edge`, or option B's face port in the TUI header) presenting the
phone's member cert and node key as the client identity, `GET /edge/describe`.

**You should see:** either a 200 with the member's describe (the identity works; option (iii) is
feasible), or a handshake refusal (record the exact `NWError`; this is the answer to the open
platform question). Report either way.

### Step 8. Cleanup

```sh
$SIM down
rm -rf /tmp/rbk/phone-auth
```

## 5. Known gaps and what to report back

**Gaps the Mac session should expect:**

- The `rogerai-edge-join/v1` golden vectors (ask d) exist now at
  `internal/edgeauth/testdata/join_proof_vectors.json`; port the
  Swift check against them before any live join, the same way as the root-proof vectors.
- Option A's hub cannot dial the phone (podman boundary): only push presence is exercised there.
  Use option B for anything that needs the authority to reach the phone.
- The D9 "older joiner" warning prints for every allow until decision 3 adds a signal.
- The phone cannot serve a model to the Edge (`addressed_inference.feature:113`), by design.
- The pull path (`EdgeResponder`, the phone serving `/edge/describe` over TLS) is still unbuilt
  and still unnecessary.
- The Tower side (`roger join` to a Tower, Tower TLS pins) is computer-only; a Tower invite on the
  phone must be refused with a clear line.
- DONE: `for=` by node id on Edge invites. `i` on a sighting, `roger edge invite --for <node id>`
  and the console's Invite mint through `edgeMintInvite` (`cmd/rogerai/edgeinvite.go`) into
  `edgeauth.MintBearerCodeFor` (`internal/edgeauth/bearer.go`); another node id gets HTTP 403
  `that invite is for another device` and the invite is not spent (section 3b.2).
- DONE: SAS vectors at `internal/joinsas/testdata/sas_vectors.json` (six transcripts with their
  commits, the word list hash, one wrong-domain negative), checked by
  `internal/joinsas/vectors_test.go`.
- DONE: the Edge authority's pairing routes, confirmed: `POST /local/pair/start`,
  `POST /local/pair/reveal`, `POST /local/pair/wait` on the authority listener
  (`http://<lan ip>:<authority port>`, beside `/edge/enroll`, `/edge/claim`, `/edge/root`), all
  signed with `X-Roger-Pubkey`/`X-Roger-TS`/`X-Roger-Nonce`/`X-Roger-Sig`. Wire comment:
  `internal/localplane/pair.go:40-58`; route test: `cmd/rogerai/edgepair_routes_test.go`; full
  bodies in section 3b.3.
- `a` on a PHONE's row is the claim path (asks a, b), not asking: a phone never has a certificate to
  advertise. A phone that asks on its own is answered from the join banner with `y`/`n`.

**Paste back, per step:**

1. Step 0: the XCTest summary line (counts), and the four proof rows' computed `key` and `proof`
   if any differ.
2. Step 1: the curl status from the Mac and from the phone's browser.
3. Step 2: the phone's two status lines, and `$SIM status` after the join.
4. Step 3: the phone's prompt text with the fingerprint, the host's code line, and for the
   impostor run the phone's refusal line naming the other authority.
5. Step 4: the five messages.
6. Step 5: three `$SIM status` snapshots (in front, after 90 s backgrounded, after foregrounding).
7. Step 6: the `tcpdump` counts for the three phases.
8. Step 7: the describe body, or the exact handshake error.
8b. Step 2b: the section 3b.6 paste-backs (S1 to S6).
9. Any crash log, and the app build's commit hash on `band-radio`.

With those pasted into this session, the Linux side can close the `@ios` scenarios in
`enrollment_root_trust.feature` as verified, mark `fleet_window.feature:297,:305` built or not,
and take decisions 1 to 4 to the founder with evidence.
