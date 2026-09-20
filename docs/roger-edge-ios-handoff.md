# Handoff: bringing the iOS/macOS app onto Roger Edge

For the roger-ios team (private repo `rogerai-fyi/roger-ios`). This describes what the app
must implement to become a member of a Roger Edge - so a phone or iPad shows up on the
owner's private fleet beside their PC and Mac, can use another machine's inference over the
LAN, and (later) can offer its own. It also states honestly what iOS can and cannot do, so
we scope the first version to what the platform allows.

The Edge fabric this targets is on `wt/roger-edge`; the wire contract below is what the Go
`roger` already speaks (`internal/edge`, `internal/edgeauth`). Nothing here needs a change on
the Go side except where noted under "Gaps on the Go side".

## The one-paragraph version

An Edge node is a machine with a certificate issued by the Edge's authority (a machine the
owner designated, or Core). To join, the app generates a keypair, gets the owner to allow its
user key on the authority, and enrolls over HTTP against the authority's LAN address,
receiving a certificate under the Edge root. From then on it is a member: it can dial peers'
LAN faces for inference (client role), and, if the platform allows a background listener, it
can answer as a peer (server role). The app already has most of the identity and grant
plumbing for the market; this reuses it.

## What the app already has (per roger-ios PLAN.md and the apple-agent-handoff notes)

- An ed25519-style account identity and `roger account`-equivalent user key.
- Grant-key handling (`Authorization: Bearer rog-grant_...`, `/grants` CRUD) and a
  RogerAI-as-backend flow that mints a grant + `/v1` base URL with Copy/QR.
- An OpenAI-compatible client against the broker.

That is the consumer half of the market. Edge adds a private, broker-free path on the LAN.

## Role 1: be a visible, enrollable member (required, do this first)

This is the minimum to satisfy the founder's test ("I should be able to see them"). It makes
the phone a node that appears on `roger edge` from the PC and Mac.

1. **Identity.** Generate and store (Keychain) a node keypair. The node id is derived from
   the public key the same way the Go client does (`internal/edgeauth.NodeID` = a stable,
   privacy-preserving hash of the ed25519 public key; match that derivation exactly).

2. **Enroll against a local authority.** The owner runs, on the authority machine:
   `roger edge authority allow <the app's user key>`. The app then POSTs an enrollment
   request to the authority's LAN address (the one the owner enrolls other machines against,
   e.g. `http://192.168.1.69:8791`). The request/response shapes are
   `internal/edgeauth.Request` / `Response` (`request.go`, `issue.go`), served by
   `internal/edgeauth/enrollhttp`. The request is signed by the user key and names the
   account, a chosen node name, the kind (`mobile`), and the public key to bind; the response
   carries the issued certificate and the public root. Store both in the Keychain.

   - Kind is `mobile` (already a node kind in `internal/edge/node.go`).
   - The account, root fingerprint and descriptor are what `internal/edgeauth.Store` persists
     on desktop; the app keeps the equivalent (account name, root cert, node cert+key).

3. **Advertise + answer describe (if backgrounding allows; see Gaps).** A member is seen by
   advertising over mDNS (`_rogerai._tcp` equivalent; the Go side uses a minimal mDNS on
   `224.0.0.251:5353`, service in `internal/edge/mdns.go`/`advert.go`) and serving a TLS
   `GET /edge/describe` over its node certificate (`internal/edge/server.go`, the `Describe`
   JSON: `node_id`, `account`, `kind`, `caps`, `instances`). iOS backgrounding makes a
   persistent listener hard; see Gaps for the fallback.

Once enrolled and advertising, the phone appears in `roger edge` on the other machines as a
`mobile` node.

## Role 2: use another machine's inference over the LAN (high value, client-only)

This needs no listener, so it works on iOS without background-execution tricks. It is the
"use edge inference from any of them" half.

1. Discover peers (or let the owner pick one from the fleet the app learned at enroll time /
   by a foreground mDNS browse).
2. Dial the peer's LAN face at `POST https://<peer>:<port>/edge/infer` over **mutual TLS**:
   present the app's node certificate as the client cert; pin the peer's served certificate
   against the fingerprint the fleet recorded (do not trust the system root store here - the
   Edge root is the only trust anchor). The request body is an OpenAI chat-completions JSON;
   set `X-Roger-Request: <id>`. Streaming replies are SSE.
3. The reply carries a **local receipt** (`X-RogerAI-Receipt` header, or a
   `: rogerai-receipt=` SSE comment at stream end): decode it, verify its node signature
   against the peer's certificate (`protocol.UsageReceipt.VerifyNode` /
   `edge.VerifyLocalReceipt`), and show the turn as served locally, cost zero.

This is the same shape the desktop proxy's rung 2 uses (`internal/client` `relayEdgeFirst`,
`internal/edge/infer.go`). Reuse the market client's OpenAI plumbing; only the transport
(mutual TLS, pinned) and the receipt verification are new.

## Role 3: serve or act as a peer (later, platform-limited)

- **Serve inference** would mean answering `/edge/infer` from an on-device model
  (Apple's on-device LanguageModel or an MLX server). It requires a background LAN listener,
  which iOS restricts; realistic only while the app is foregrounded, or on macOS. Treat as a
  later, opt-in "share while open" mode, not the first version.
- **Sense / actuate** (a phone offering its camera or a HomeKit bridge as an Edge device) is
  interesting but is `control.feature` territory and unbuilt on the Go side too. Out of scope
  for now.

## Gaps to note and document

### On iOS (platform)

- **No reliable background listener.** iOS will not keep a TCP/mDNS server alive in the
  background. So Role 1's "advertise + answer describe" is only dependable while the app is
  open. **Fallback for visibility:** the app can register itself in the fleet at enroll time
  and refresh via a foreground heartbeat, so it shows on other machines as a `mobile` node
  that is present when the app is open and goes dark otherwise - which is honest and matches
  how the Edge already draws a node that stops answering. Document this as expected.
- **mDNS in background** is likewise limited; foreground browse is fine for Role 2.

### On the Go side (small, do before iOS integration)

1. **Enrollment content-type / CORS for a mobile client.** `internal/edgeauth/enrollhttp`
   was written for the desktop client. Verify it accepts the app's request as-is (it should:
   it is a signed JSON POST) and returns the root + certificate the app needs. If the app
   cannot do client-cert TLS for enrollment (enrollment happens BEFORE it has a cert, so it
   is plain TLS to the authority), confirm the authority endpoint does not require a client
   cert. It does not today (enrollment is the one unauthenticated-by-cert path, gated by the
   allow-list + the signed request), so this should be fine - but pin it with a test.

2. **A documented enrollment wire spec.** The request/response Go structs are the contract,
   but there is no language-neutral description of them for a Swift implementer. Write one
   (fields, signing bytes, the node-id derivation) as `docs/roger-edge-enroll-wire.md`. This
   is the single most useful thing to unblock the app; it is a doc, not code.

3. **Node kind `mobile` on every surface.** It exists as a kind; confirm the TUI/console
   draw it with a sensible glyph (a phone) rather than defaulting to host. Minor.

4. **A "present while the app is open" presence** is already expressible: the node simply
   ages to dark when the app stops heartbeating. No Go change needed; just confirm the dark
   threshold is comfortable for a phone (default liveness window).

None of these block Role 2 (client inference), which is the highest-value, most-testable
first step and needs only the mutual-TLS dial + receipt verification against a peer that is
already a member.

## Suggested first milestone for the app

1. Generate identity, enroll against the owner's PC authority over the LAN, store the cert.
2. Show "you are on <owner>'s Edge" with the mode (LOCAL root).
3. Let the user pick a peer from the fleet and run a chat turn against it over `/edge/infer`,
   pinned, verifying the local receipt, shown as a local (free) turn.

That alone proves the phone is a real member using private, broker-free inference, and it is
all client-side. Roles 1-advertise and 3-serve follow when we decide how much background
presence to invest in.

## For the article

The iOS piece is the vivid one: a phone using the workstation's GPU over the home Wi-Fi with
no cloud in the path, and the receipt to prove it stayed home. Keep the claim to what the
first milestone actually does (consume peer inference), not to serving from the phone, until
Role 3 ships. This file and `docs/roger-edge-claims.md` are the source of truth for what may
be said.
