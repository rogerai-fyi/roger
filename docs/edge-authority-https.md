# Design note: an HTTPS listener on the Edge authority (PROPOSED, pending the founder's ruling)

Status: PROPOSED 2026-10-13. Not built. Written after the audit of `features/edge/phone_consume.feature`.

## Why

A phone cannot do mutual TLS (its node key is an Ed25519 key in CryptoKit, and iOS has no SecKey type
for Ed25519), so it talks to its Edge's authority over the authority's plain-HTTP listener, proving
itself with a signature in each request. That signature makes a request unforgeable, but it does
nothing for the other direction:

- confidentiality: a prompt and its answer are readable by anyone on the LAN;
- reply integrity: a device in the path can rewrite an answer, a band list or a receipt header;
- authority authenticity: an impostor answering at the authority's address (an mDNS spoof, a taken
  address) can read a turn and answer it.

The fix is a server-authenticated TLS listener the phone can verify against the Edge root it already
holds. The phone still proves ITSELF with the request signature; TLS only proves the authority.

## The certificate

- `cert.Authority.IssueServer(nodeID string, hosts []net.IP, ttl)`, issued by the Edge root:
  - key: a fresh **P-256** key held only in memory by the authority process (iOS's TLS stack
    handshakes with P-256 server keys; it is not known to with Ed25519);
  - Extended Key Usage: **ServerAuth only** (never ClientAuth), KeyUsage DigitalSignature;
  - SANs: the URI SAN of the authority's own node id (the same form member certificates carry) and an
    IP SAN for each LAN address the authority answers on; no DNS names;
  - lifetime: **short** (24 hours), reissued at half-life and on any change of LAN address, so a leaked
    key is worth a day at most. Nothing is persisted; a restart issues a new one.
- It never appears in a member's trust as a member: its EKU is ServerAuth only and member
  authentication requires the member certificate shape, so it cannot be presented as a client.

## The listener

- The same `authMux` the plain listener serves, on a second listener wrapped in TLS with that
  certificate (`tls.Config{MinVersion: TLS1.2, Certificates: [server cert]}`, no client certificates).
- Its port is stable across restarts like the plain one (`authority-tls-port` beside `authority-port`).
- Where the phone learns it:
  - the authority's mDNS advert gains a TXT key **`tls=<port>`** (an older phone ignores it);
  - the enroll and claim answers gain a response header **`Roger-Edge-TLS: <port>`** beside
    `Roger-Edge`, so the body stays exactly what an older joiner knows.
- Plain HTTP stays for what must work before a phone holds the root: `/edge/enroll`, `/edge/claim`,
  `/edge/root`, `/edge/revocations*`, the pairing routes. Once the app speaks HTTPS, the phone paths
  (`/edge/present`, `/edge/trouble*`, `/edge/phone/*`) are REFUSED on the plain listener with a line
  naming the TLS port, by an owner-visible switch whose default flips one release later.

## What the phone pins

- **The Edge root it already holds** (the `root` PEM it kept at enrolment, after the root proof):
  SecTrust with that root as the ONLY anchor (`SecTrustSetAnchorCertificates` +
  `SecTrustSetAnchorCertificatesOnly(true)`), no system roots.
- The leaf must: chain to that root; carry ServerAuth EKU; be in date; carry the URI SAN of **the
  authority node id the phone recorded** (from the advert's `id` at join, or the enroll answer); and
  carry an IP SAN equal to the address dialed.
- The phone does NOT pin the leaf or its key (it rotates daily). It pins the root and the node id.
- A handshake that fails any of these is refused and said ("the Edge at this address could not prove
  it is shed"), never retried over plain HTTP.

## What stays the same

The request signature (`rogerai-edge-infer/v1`, presence, trouble feed) stays exactly as it is: it is
the phone's identity, and it binds each request to its body and moment. The relay attestation between
the authority and the faces is unchanged.

## Also proposed, not built (review of 2026-10-13)

- **A dedicated relay key.** Today the authority signs each relay attestation (`Roger-Relay`) with the
  Edge root itself. Defence in depth would be a separate relay key, certified by the root with a
  narrow EKU and a short life, sent beside the attestation; faces would check the chain and the
  attestation. It fits the same issuing code as the server leaf above (`IssueServer`'s sibling).
- **The invite's id in the bearer proof.** A wrong join proof costs a guess against every live invite
  it could have been for, because the proof does not say which invite it was made with. Putting the
  invite's join id in the proof (a new `rogerai-edge-join/v2` domain, the id carried in the invite URL)
  would charge a miss only to the invite it names. It changes the invite and the proof on every
  joiner, so it waits for its own ruling.
