# Roger Edge enrollment: the wire contract

A language-neutral description of how a machine joins an Edge, for implementers not writing
Go (the iOS/Swift app first). The authority is the Go side; this is exactly what it speaks
(`internal/edgeauth`), written so a Swift client can enroll without reading Go.

Everything here is ed25519 and hex (lowercase, unpadded) and JSON. There are no other
encodings.

## The two keys

- **User key**: the owner's account identity on this machine (ed25519). Its public half is
  what the owner adds to the authority's allow-list (`roger edge authority allow <user key>`).
  On desktop this is the value `roger account` prints as `pubkey:`.
- **Node key**: a fresh ed25519 keypair for THIS machine as a node. Its public half is bound
  into the certificate the authority issues. Generate it once and keep the private half in
  the Keychain; it never leaves the device.

A machine can enroll with the user key alone allowed; the node key is what it is enrolling.

## Node id

```
node_id = "n_" + hex( sha256(node_public_key)[0:24] )
```

That is the first 24 bytes of the SHA-256 of the 32-byte ed25519 public key, hex-encoded, so
`node_id` is `"n_"` followed by 48 hex characters. Derive it exactly this way; the authority
recomputes and checks it.

## The request

`POST http://<authority-lan-address>/edge/enroll`, body `application/json`:

```json
{
  "account":  "<the Edge's account name, or empty to take the authority's>",
  "node_id":  "n_<48 hex>",
  "node_key": "<hex ed25519 node PUBLIC key, 64 hex>",
  "name":     "<the node name the owner chose, e.g. macbook>",
  "kind":     "mobile",
  "nonce":    "<hex of 16 random bytes>",
  "ts":       <unix seconds>,
  "user_key": "<hex ed25519 user PUBLIC key, 64 hex>",
  "sig":      "<hex ed25519 signature, 128 hex>"
}
```

- `kind` is one of `host`, `board`, `mobile`. Use `mobile` for a phone or tablet.
- `account` may be empty. A locally-rooted authority fills in its own account; send empty
  unless the owner told you a specific account.
- Enrollment is plain TLS to the authority (the client has no certificate yet). Do NOT send
  a client certificate on this request. Pin the authority's certificate if you fetched its
  root out of band; otherwise this first exchange trusts the LAN address the owner typed.

### The signature

`sig` is the ed25519 signature, by the **user private key**, over this exact byte string
(fields joined by a single `\n`, no trailing newline):

```
rogerai-edge-enroll/v1
<account>
<node_id>
<node_key>
<name>
<kind>
<nonce>
<ts>
```

`<ts>` is the decimal unix seconds as text. `user_key` in the JSON is the hex public half of
the same key that signed. Any field changed after signing breaks the signature - that is the
tamper check, so build the JSON from the exact strings you signed.

## The response

`200`, `application/json`:

```json
{
  "node_id": "n_<48 hex>",
  "account": "<the account you were enrolled into>",
  "cert":    "<PEM: the certificate issued to your node key>",
  "root":    "<PEM: the Edge's PUBLIC root certificate>"
}
```

Store all three plus your node private key. The `cert` is your node certificate (present it
as the client cert for peer inference and to serve `describe`). The `root` is the ONLY trust
anchor for this Edge: verify every peer against it, not the system trust store. Keep the
account name; it scopes everything.

## Refusals

Every refusal is `403` with a short text reason and mints nothing. The status is uniform on
purpose: a caller that is not allowed learns "no", never which "no", because the differences
are facts about the account, not the caller. The reasons you may hit while integrating:

- the signature does not verify (wrong key, or the JSON was not built from the signed bytes),
- the user key is not on the authority's allow-list (owner must run `authority allow`),
- the node id does not match the node key,
- a stale or reused timestamp/nonce.

`405` means you did not POST. `400` means the body was not a request at all.

## Two more endpoints on the same authority

- `GET /edge/root` returns the public root as PEM (`application/x-pem-file`). Useful to pin
  the authority before enrolling, if you can get the owner to compare a fingerprint.
- `GET /edge/revocations` returns a JSON array of revoked certificate serials. Refresh it
  periodically and refuse any peer whose certificate serial is in it.

## After enrollment: talking to peers

Not part of enrollment, but the reason to enroll. To use a peer's inference:

`POST https://<peer-lan-address>/edge/infer`, mutual TLS:
- present your node certificate as the client certificate,
- verify the peer's served certificate against the fingerprint the fleet recorded (SHA-256
  of its DER), under the Edge root,
- body is an OpenAI chat-completions JSON; header `X-Roger-Request: <a fresh id>`,
- the reply is JSON or SSE; it carries a receipt in the `X-RogerAI-Receipt` header (plain) or
  a `: rogerai-receipt=<json>` comment at the end of the stream. Decode it, and verify its
  `node_sig` against the peer's certificate public key before trusting it as a record.

That is the whole private path: no broker, no account lookup, no money.

## Ground truth (Go)

`internal/edgeauth/request.go` (Request, canonical, Sign), `internal/edgeauth/nodeid.go`
(NodeID), `internal/edgeauth/issue.go` (Response, Issue), `internal/edgeauth/enrollhttp/`
(the HTTP handler, paths), `internal/edge/infer.go` (the peer path),
`internal/protocol/protocol.go` (UsageReceipt, VerifyNode). If any of these change, this file
changes with them; it is the contract the app is built against.
