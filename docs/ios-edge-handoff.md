# iOS Edge handoff: validate the phone against the current Edge build

Written 2026-10-11 for the founder's Mac session. The Go side (branch `wt/simple-join`, test build
`6.10.0-test.e41c3b6b`) now asks nine things of a phone on a Roger Edge. This file is the one place
that says what each ask is, the exact bytes the phone must produce or check, what to change in the
app, and what the founder should see on both screens when it works.

Everything here is pinned to the real Go code in `/home/luis/rogerai-wt/simple-join` and to approved
specs. Nothing is pushed anywhere; this is a local test branch.

Read these alongside this file:

- `/home/luis/rogerai-wt/simple-join/docs/ios-enrollment-root-trust.md`: the Swift for the root
  check, written file by file, with the golden vectors. This handoff cites it rather than repeating
  it. Call it "the root-trust doc" below.
- `/home/luis/ai/roger-ios/EDGE-HANDOFF.md`: the earlier two-repo handoff (presence, claim loop,
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

The current test build is `/home/luis/rogerai-test-builds/20261011-1924-e41c3b6b/`. Its checksums:

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
cd <copy of /home/luis/rogerai-wt/simple-join>
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
export EDGESIM_ROGER=/home/luis/rogerai-test-builds/20261011-1924-e41c3b6b/roger
export EDGESIM_PUBLISH=1
SIM=/home/luis/rogerai-wt/simple-join/tools/edgesim/edgesim
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
R=/home/luis/rogerai-test-builds/20261011-1924-e41c3b6b/roger
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

**Spec:** `/home/luis/rogerai-wt/simple-join/features/edge/enrollment_root_trust.feature:353`
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
Edge (`/home/luis/rogerai-wt/simple-join/internal/edgeauth/enrollhttp/enrollhttp.go:55-57`,
`:147`, `:213`). The body is exactly what an old app knows. Show the name after a successful join
("joined the Edge shed"); it decides nothing. Up to 64 printable characters, else ignore it
(`internal/edgeauth/issue.go:92-98`).

Derivation (`/home/luis/rogerai-wt/simple-join/internal/joincode/joincode.go:51-54`, domain
`rogerai-edge-root-proof/v1`, 10 Crockford symbols, PBKDF2-HMAC-SHA256 at 200000 iterations
salted with `domain + "\n" + nonce`, message `domain, rootFP, account, node_id, nonce` joined by
`\n`): the root-trust doc section 1 has it in full and section 2 has the Swift (`EdgeRootProof`).

**Golden vectors:** `/home/luis/rogerai-wt/simple-join/internal/edgeauth/testdata/root_proof_vectors.json`
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
`/home/luis/ai/roger-ios/RogerAI/Services/Edge/EdgeService.swift:192-215` (`claimOnce`, `.granted`
calls `adopted` at line 203) is the exact spot.

**Pass:** run a second authority on the LAN (option B on the Linux box while option A's hub is
published is enough: two authorities, one phone). Adopt the phone on ONE of them. The phone must
hold both answers, ask for the code once, keep the one whose code verifies, and report the other
as "answered as an Edge authority but could not prove its root (offered root XXXX-...)".

### (c) A certificate is stored only after the chain check

**Spec:** `enrollment_root_trust.feature:1016` (same P-256 chain rules as Go).

**Wire:** none. The checks mirror `edge.VerifyPeer`
(`/home/luis/rogerai-wt/simple-join/internal/edge/verify.go:129-170`) and `edgeauth.CheckResponse`:

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

**Spec:** `/home/luis/rogerai-wt/simple-join/features/join/invite.feature:208` (a device joins an
Edge with `roger join`), `:218` (`roger edge invite` needs no joiner key first: a BEARER invite),
`:65` (the old `rogerai-edge://join?...` alias still accepted), `:80` (Edge codes keep the
10-symbol format), `:39` and `/home/luis/rogerai-wt/simple-join/features/join/surfaces.feature:52`
(the QR encodes the same line).

**Wire, the invite** (`/home/luis/rogerai-wt/simple-join/internal/joininvite/joininvite.go:3-8`,
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

1. The request gains `join_proof` (`/home/luis/rogerai-wt/simple-join/internal/edgeauth/request.go:45`),
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

**Golden vectors:** `/home/luis/rogerai-wt/simple-join/internal/edgeauth/testdata/join_proof_vectors.json`,
beside the root-proof vectors, computed independently of Go by
`/home/luis/rogerai-wt/simple-join/internal/edgeauth/testdata/gen_join_proof_vectors.py` (Python
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

**Spec:** `/home/luis/rogerai-wt/simple-join/features/edge/presence.feature` (dial-out presence),
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

**Spec:** `/home/luis/rogerai-wt/simple-join/features/edge/scan_on_request.feature`. For a
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

## 4. Validation checklist for the Mac session

Each step stands on its own. Host commands run on the Linux box (`192.168.1.69`); phone steps in
the app. Keep every paste block as written; a long line that wraps in your terminal and errors with
"command not found" was split by the terminal, paste it again.

### Step 0. Build the app from `band-radio` with the asks (a) to (d) applied

**Why.** Everything below measures the updated app. The current `band-radio` build will join, but
cannot pass steps 2 to 5.

**Run (Mac):**
```sh
cd ~/ai/roger-ios
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
export EDGESIM_ROGER=/home/luis/rogerai-test-builds/20261011-1924-e41c3b6b/roger
export EDGESIM_PUBLISH=1
SIM=/home/luis/rogerai-wt/simple-join/tools/edgesim/edgesim
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
- Host: `$SIM status` lists the phone by its name, `lan`, seen seconds ago. `$SIM roger hub edge
  invite --list` shows the invite spent.

**If instead you see:**
- "that invite was already used": mint a new one (single use).
- "the Edge that answered is not the one this invite names": the `fp` and the root disagree.
  Either the invite was retyped, or something else answers on that port. Stop and check.
- "that authority does not prove its root": the hub is running an older roger; `EDGESIM_ROGER`
  was not exported before `up`.

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
  `/home/luis/rogerai-wt/simple-join/internal/edgeauth/testdata/join_proof_vectors.json`; port the
  Swift check against them before any live join, the same way as the root-proof vectors.
- Option A's hub cannot dial the phone (podman boundary): only push presence is exercised there.
  Use option B for anything that needs the authority to reach the phone.
- The D9 "older joiner" warning prints for every allow until decision 3 adds a signal.
- The phone cannot serve a model to the Edge (`addressed_inference.feature:113`), by design.
- The pull path (`EdgeResponder`, the phone serving `/edge/describe` over TLS) is still unbuilt
  and still unnecessary.
- The Tower side (`roger join` to a Tower, Tower TLS pins) is computer-only; a Tower invite on the
  phone must be refused with a clear line.

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
9. Any crash log, and the app build's commit hash on `band-radio`.

With those pasted into this session, the Linux side can close the `@ios` scenarios in
`enrollment_root_trust.feature` as verified, mark `fleet_window.feature:297,:305` built or not,
and take decisions 1 to 4 to the founder with evidence.
