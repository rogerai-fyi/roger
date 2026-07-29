# Licensing

RogerAI carries **two licenses side by side**. Which one applies depends on the file.

| Part | License | Text |
|---|---|---|
| The **node-agent protocol** and the **usage-receipt SDK** | Apache License 2.0 | [`LICENSE-APACHE-2.0`](LICENSE-APACHE-2.0) |
| Everything else (the broker, the marketplace, the CLI/TUI, the web surfaces) | PolyForm Perimeter 1.0.0 | [`LICENSE`](LICENSE) |

## Why a carve-out

Two different jobs. The protocol and the receipt format are **interoperability surfaces**:
somebody writing their own node agent, or auditing what they were charged, has to be able to
read, implement and re-implement them without asking anyone's permission. A non-OSI license
there would make an open protocol a closed one in practice.

The platform is the product, and it stays under PolyForm Perimeter, which permits use and
modification but not building a competing service out of it.

The two licenses **sit alongside each other** - the Apache grant does not extend to the rest
of the repository, and the PolyForm terms do not restrict the Apache-licensed files.

## Exactly which files

Apache-2.0 applies to the files carrying an `SPDX-License-Identifier: Apache-2.0` header,
and no others. Today that is:

- `internal/protocol/protocol.go` - the wire types a node registers and serves with
  (`ModelOffer`, `NodeRegistration` and its Ed25519 signing/verification), plus the
  `UsageReceipt`: its canonical signing bytes, hash, and cost arithmetic.
- `internal/protocol/auth.go` - the request-signing scheme a node or consumer uses to prove
  who is spending. Without it the protocol above is not implementable against a real broker,
  so it belongs to the same surface.

Deliberately **not** carved out, though they live in the same package: `band.go` (private
frequency codes) and `rc.go` (the remote-control wire) are platform features, not things a
third-party node has to speak.

`internal/protocol/license_carveout_test.go` pins this list, so a new file cannot drift into
or out of the carve-out unnoticed.

## Known limitation, being fixed

The Apache files currently live under `internal/`, which Go forbids other modules from
importing. The license is real, but to *use* it today you would have to copy the files rather
than import them. Moving this surface to an importable path is a mechanical change that
rewrites the import in ~230 files, so it is being done as its own focused change rather than
folded in here. Until then, treat these files as the reference implementation of the format.

## Third parties

Vendored or referenced third-party code keeps its own license; nothing here changes it.
