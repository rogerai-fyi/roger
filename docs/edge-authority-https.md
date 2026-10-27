# The Edge authority's HTTPS listener

Status: RULED 2026-10-13 (founder: build it) and BUILT on wt/ios-asks: `cert.Authority.IssueServer`
(internal/towercore/cert/server.go), the listener (cmd/rogerai/edgeauthtls.go). What is below is the
wire as built; the "Also proposed" section at the end is still only proposed.

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
  - Subject CN: the authority node id. SANs: exactly one URI SAN,
    `spiffe://rogerai.fm/tower/<authority node id>` (the same form member certificates carry), and an
    IP SAN for each of the machine's LAN addresses (IPv4, up, not loopback); no DNS names;
  - chain: leaf -> root only (the root's pathLen is 0; the server sends the leaf alone);
  - lifetime: **short** (24 hours), reissued at half-life and on any change of LAN address, so a leaked
    key is worth a day at most. Nothing is persisted; a restart issues a new one.
- It never appears in a member's trust as a member: its EKU is ServerAuth only and member
  authentication requires the member certificate shape, so it cannot be presented as a client; and no
  member certificate (ClientAuth only) can serve as the authority.
- Golden fixture for the phone's trust evaluation: `internal/towercore/cert/testdata/authority_server_fixture.json`,
  a REAL IssueServer leaf and its root (PEM), the node id, the IP SANs, a `verify_at` time inside both
  lives, and a `wrong_ip` that must fail. Its leaf's life is stretched so the file stays valid;
  production leaves live 24 hours.

## The listener

- The same `authMux` the plain listener serves, on a second listener wrapped in TLS with that
  certificate (`tls.Config{MinVersion: TLS1.2, Certificates: [server cert]}`, no client certificates).
- Its port is stable across restarts like the plain one (`authority-tls-port` beside `authority-port`).
- Where the phone learns it (BOTH):
  - the authority's mDNS advert carries the TXT key **`tls=<port>`** beside `auth=<port>` (an older
    reader ignores it; a node that is not an authority emits no `tls` key);
  - EVERY answer on the plain listener (so `/edge/enroll`, `/edge/claim`, `/edge/root`, presence)
    carries two response headers once the TLS listener is up: **`Roger-Edge-TLS: <port>`** (decimal)
    and **`Roger-Edge-Authority: <authority node id>`** (the one URI SAN the server certificate
    names). No JSON field: the enroll and claim bodies stay byte-identical to what an older joiner knows.
- The port is stable across restarts (`authority-tls-port` beside `authority-port`;
  `ROGERAI_EDGE_AUTHORITY_TLS_BIND` pins it).
- Served on TLS: the whole authority mux, so `/edge/phone/*`, `/edge/present` and (when that branch
  lands) the authority-side trouble feed, and enrollment too.
- On the plain listener, once TLS is up, `/edge/phone/*` is REFUSED: 421 Misdirected Request,
  `{"error":{"message":"a phone's requests are served over TLS on port <port>","type":"edge_error"}}`.
  Enrollment, claim, root, revocations, pairing and presence stay on plain HTTP: a fresh phone has no
  root to check TLS with yet, and presence keeps working for apps from before.
- A machine with no node identity of its own serves no TLS listener and says so in its log.

## What the phone pins

- **The Edge root it already holds** (the `root` PEM it kept at enrolment, after the root proof):
  SecTrust with that root as the ONLY anchor (`SecTrustSetAnchorCertificates` +
  `SecTrustSetAnchorCertificatesOnly(true)`), no system roots.
- The leaf must: chain to that root; carry ServerAuth EKU; be in date; carry the URI SAN of **the
  authority node id the phone recorded** (the `Roger-Edge-Authority` header of its enroll or claim
  answer, or the authority advert's `id`); and
  carry an IP SAN equal to the address dialed.
- The phone does NOT pin the leaf or its key (it rotates daily). It pins the root and the node id.
- A handshake that fails any of these is refused and said ("the Edge at this address could not prove
  it is shed"), never retried over plain HTTP.

## What stays the same

The request signature (`rogerai-edge-infer/v1`, presence, trouble feed) stays exactly as it is: it is
the phone's identity, and it binds each request to its body and moment. The relay attestation between
the authority and the faces is unchanged.

## Also proposed, not built (review of 2026-10-13)

- **A dedicated relay key.** Not built with the listener (it did not fall out of it: the relay
  attestation goes to a member's face, which would need a new chain check and a key usage of its
  own). Today the authority signs each relay attestation (`Roger-Relay`) with the
  Edge root itself. Defence in depth would be a separate relay key, certified by the root with a
  narrow EKU and a short life, sent beside the attestation; faces would check the chain and the
  attestation. It fits the same issuing code as the server leaf above (`IssueServer`'s sibling).
- **The invite's id in the bearer proof.** A wrong join proof costs a guess against every live invite
  it could have been for, because the proof does not say which invite it was made with. Putting the
  invite's join id in the proof (a new `rogerai-edge-join/v2` domain, the id carried in the invite URL)
  would charge a miss only to the invite it names. It changes the invite and the proof on every
  joiner, so it waits for its own ruling.
