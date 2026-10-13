# STATUS: APPROVED in principle 2026-10-13 by founder ruling ("we should support that": a phone should
# be able to use the Edge's models). The details below are PROPOSED unless a scenario says RULED.
# BUILD STATUS: BUILT on wt/ios-asks (Go side). Not godog-bound: pinned by
# internal/edge/phoneconsume_test.go (every @security scenario here has a test of the same name) and
# the golden vectors in internal/edge/testdata/infer_sig_vectors.json. The app side is not built.
#
# ROGER EDGE - A PHONE CONSUMES THE EDGE'S BANDS.
#
# WHY IT CANNOT BE THE FACE'S /edge/infer. A member asks another member's face for a turn over mutual
# TLS, presenting its member certificate (local_inference.feature). A phone cannot present one: its
# node identity is an Ed25519 key kept by CryptoKit, and Apple's Security framework has no SecKey type
# for Ed25519, so no SecIdentity can be built on iOS to offer as a client certificate. Nor can the app
# count on completing a TLS handshake with a face whose own certificate is an Ed25519 leaf: the app
# can verify that CHAIN in its own code (the root and the issuing signature are P-256), but whether
# iOS's TLS stack offers and accepts Ed25519 for the handshake signature is unverified, and nothing
# here depends on it.
#
# THE DESIGN (PROPOSED; the transport choice is for founder review). The phone asks the Edge's
# AUTHORITY, on the listener it already reaches for presence, and proves itself the way presence does:
# its member certificate travels with the request, and the request is signed by that certificate's key
# over a canonical form that covers a hash of the body. The authority checks it as a face checks a peer,
# then relays the very bytes the phone signed to the member that serves the band, over its own pinned
# mutual-TLS connection, forwarding the phone's proof and a RELAY ATTESTATION: the Edge root's ECDSA
# signature over "rogerai-edge-relay/v1" \n <face node id> \n <the phone's Roger-Sig>. A face credits a
# turn to a phone only with that attestation naming ITSELF, and checks the phone's proof again; a face
# from before this receipts it to the authority. The other candidate, the member's face accepting a
# signed request with no client certificate, was not chosen: it puts the phone on a TLS handshake with
# an Ed25519 server key (above), and it opens every member's face to requests that are not mutually
# authenticated.
#
# THE WIRE. Paths on the authority listener (the address and port the phone posts presence to), each
# POST, each answered with JSON errors shaped {"error":{"message":...,"type":"edge_error"}}:
#   /edge/phone/bands    empty body; answers {"bands":[{"band","voice","served_by":[{"node","node_id","instance","target"}]}]}
#   /edge/phone/infer    the OpenAI-shaped chat body /edge/infer takes (4 MiB cap), streamed when it asks to be
#   /edge/phone/speech   the speech body /edge/speech takes (64 KiB cap); audio back
# Headers, ALL REQUIRED except Roger-Target: Roger-Node (node id), Roger-Cert (base64 of the member
# certificate DER), Roger-Target (optional: "jetson" or "jetson/serve", as `roger use` names a member),
# Roger-Nonce (exactly 32 lowercase hex: 16 random bytes), Roger-TS (unix seconds), Roger-Body-SHA256
# (exactly 64 lowercase hex: SHA-256 of the exact body bytes, of the empty string for the band list;
# REQUIRED since 2026-10-13, no request is served without it), Roger-Sig (hex Ed25519 by the node key):
#   "rogerai-edge-infer/v1" \n node_id \n action \n target \n Roger-Body-SHA256 \n nonce \n ts
# where action is bands, infer or speech by path, and target is "" when the header is absent.
# Order and refusals (audit 2026-10-13): 405 not POST; 403 "that request is stale: check this device's
# clock" (it depends only on the time the request carries); then, from the HEADERS ALONE and before a
# slot is taken or a byte of the body is read, the signature over the declared body hash, the
# certificate under this root and not revoked, and membership of this fleet - every failure of these,
# and a body that does not match its declared hash, is the one 403 "that request could not be verified
# as a member of this Edge", so a certificate captured without its key learns nothing; 403 "that
# request was already seen"; 503 with Retry-After: 2 when this member already has two requests in
# flight or eight are in flight in all; then the body, which must arrive within 30 seconds: 413 over
# the cap, 400 a band list with a body or a turn naming no band; 404/409/502/503 for the band and target
# refusals below. The reply carries the serving member's local receipt exactly as /edge/infer does
# (X-RogerAI-Receipt on a plain reply, ": rogerai-receipt=" at a stream's end), X-RogerAI-Provider and
# X-RogerAI-Cost: 0.
#
# KNOWN LIMITATIONS, FOR FOUNDER REVIEW (restated after the 2026-10-13 audit). The authority listener
# is plain HTTP, so until the fix below:
#   - Confidentiality: a prompt and its answer cross the LAN between the phone and the authority in the
#     clear; anyone on that network can read them.
#   - Reply integrity: nothing authenticates the REPLY. A device in the path can rewrite an answer, a
#     band list or a receipt header, and the phone cannot tell. (A receipt is signed by the serving
#     member's node key, but the phone holds no copy of that member's certificate to check it with.)
#   - Authority authenticity: the phone reaches the authority by an address it holds or saw advertised;
#     an impostor answering there (an mDNS spoof, a taken address) can read a turn and answer it.
#   - What does hold: a request cannot be forged, altered or retargeted (the signature covers its
#     action, target and body); presented twice to the authority it is refused; relayed to a face it
#     was not attested for, it is refused. A captured request can still be presented ONCE, within its
#     five minutes, to an authority that restarted and so forgot the nonce.
#   - X-Roger-Request is not signed, so the authority does not forward it: the face names the turn and
#     that name is what the receipt carries.
# The fix, pending the founder's ruling: docs/edge-authority-https.md (an HTTPS authority listener
# whose server certificate is a P-256 leaf under the Edge root, pinned by the phone to that root).
# Until it is built, the app should say on its ask screen that a turn on this Edge is readable, and its
# answer changeable, by others on the local network.

Feature: a phone uses the Edge's models through its authority

  Background:
    Given an Edge "shed" whose authority is also a member
    And a member "jetson" that serves "qwen3-8b" and the voice "kokoro", verified by the serving probe
    And the phone "pocket" is a member of "shed" and posts its presence to the authority

  @ios
  Scenario: the phone lists what it can use
    When the phone asks the authority for the Edge's bands
    Then it is told "qwen3-8b" is served by "jetson" and "kokoro" is a voice on "jetson"
    And nothing a phone cannot reach is listed: a dark member, an unproven serve, or one with no address

  @ios @security
  Scenario: a member reaches a band on another member
    When the phone asks for a turn on "qwen3-8b"
    Then the authority relays exactly the bytes the phone signed to "jetson" over mutual TLS
    And the answer comes back with jetson's local receipt, made out to the phone and signed by jetson
    And nothing is billed

  @ios
  Scenario: a streamed turn streams, with the receipt at its end
    When the phone asks for a streamed turn on "qwen3-8b" addressed to "jetson"
    Then the events arrive as the member sends them, and the receipt is the last comment line

  @ios
  Scenario: a spoken line keeps the 64 KiB text cap
    When the phone asks "kokoro" to speak a line
    Then it gets jetson's audio and a receipt made out to the phone
    And a request over 64 KiB is refused before anything is relayed

  @ios @security
  Scenario: an unknown band is refused with a clear line
    When the phone asks for a turn on "llama-9000"
    Then it is refused with "no member of this Edge serves llama-9000; it offers: kokoro, qwen3-8b"
    And a turn addressed to "nas", which is no member, is refused with "no member of this Edge is called nas"

  # Audit 2026-10-13: the certificate travels in the clear, so it proves nothing on its own.
  @ios @security
  Scenario: a sniffed certificate without its key holds no slot and is read nothing
    Given a device that captured "pocket"'s certificate but not its key
    When it sends turns with that certificate and trickles their bodies
    Then each is refused from its headers before a slot is taken or a byte of a body is read
    And "pocket"'s own turns are served

  @ios @security
  Scenario: one member cannot hold every slot
    Given "pocket" has two turns streaming
    When it asks for a third
    Then it is told to try again shortly, and every other member is still served

  @ios @security
  Scenario: a request without its body hash, or with a body that does not match it, is refused
    When the phone leaves Roger-Body-SHA256 off, or sends a body other than the one it declared
    Then it is refused as any request that cannot be verified is

  @ios @security
  Scenario: a non-member is refused
    Given a device whose certificate is under another Edge's root, or under this root but no longer in its fleet
    When it asks for a turn
    Then it is refused, and the words do not say which
    And not one byte of its body is read, and nothing reaches a model

  @ios @security
  Scenario: a revoked phone is refused
    Given the owner forgot "pocket"
    When it asks for a turn with its old certificate
    Then it is refused at once, without a restart, in the same words as any stranger
    And a device that captured that certificate, without its key, gets the same answer whether or not it was revoked

  @ios @security
  Scenario: a replay is refused
    Given a turn the phone sent and the authority served
    When the same bytes and headers are sent again
    Then it is refused as already seen

  @ios @security
  Scenario: a stale request is refused
    When the phone signs a turn with a time more than five minutes from the authority's clock
    Then it is refused as stale, and told to check the device's clock

  @ios @security
  Scenario: a tampered body is refused
    When a device in between changes one byte of the body after the phone signed it
    Then it is refused because the signature does not check out
    And nothing reaches a model

  @ios @security
  Scenario: a signature is bound to its action and its target
    When a signed band list is presented as a turn, or a turn signed for "jetson" is retargeted
    Then it is refused

  @ios @security
  Scenario: the member checks the phone itself
    When a relay changes the body after the authority let the turn through
    Then jetson refuses it, because the phone's proof does not cover those bytes

  # Audit 2026-10-13: before the attestation, a member that captured a relayed turn could replay it to
  # every other face serving the band within five minutes, receipted to the phone.
  @ios @security
  Scenario: only the authority can relay a phone's turn, and only to the face it named
    When a member forwards a phone's proof to jetson with no attestation from the Edge's root, or a turn the root attested for another face
    Then jetson refuses it, and nothing reaches its model

  @ios @security
  Scenario: the receipt names the turn the face named
    When the phone's request carries an X-Roger-Request of its own choosing
    Then the receipt's request id is the face's, not that one

  @ios
  Scenario: a member recorded at a wildcard address is not dialed on the authority's own machine
    Given a member whose recorded face address is 0.0.0.0 or [::]
    When the phone asks it for a turn
    Then the authority refuses with "has no address this Edge can reach", and dials nothing

  @ios
  Scenario: a band list is asked with an empty body
    When the phone asks for the band list with a body
    Then it is refused, saying a band list is asked with an empty body

  @ios @security
  Scenario: a member from before this change still serves the relayed turn
    Given "jetson" runs a roger that does not read the phone's proof
    When the phone asks for a turn
    Then it is served, and the receipt is made out to the authority that relayed it

  @ios @security
  Scenario: what a phone's turn on this Edge exposes is said, not hidden
    When the phone shows the ask screen for an Edge band
    Then it says the turn is readable, and its answer changeable, by others on the local network until the authority serves HTTPS
