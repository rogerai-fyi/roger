# The phone half of "the first enrollment verifies the root"

Spec: `features/edge/enrollment_root_trust.feature` (APPROVED 2026-10-06, all fifteen rulings as
recommended). The Go side is built on `wt/edge-enroll-root`. This file is what the roger-ios app
must change to match it. The changes need Xcode and a Mac, so they are written here rather than
made from Linux.

The rule in one line: **a phone that holds no Edge root keeps an authority's answer only when the
one-time enrollment code the owner reads off the authority verifies the root in that answer**.
A phone that already holds this Edge's root checks every answer against that root and needs no
code.

## 1. What changes on the wire

Nothing the phone sends changes. The phone never sends the code, and never sends anything derived
from it.

The authority's answer, to both `POST /edge/enroll` and `POST /edge/claim`, gains two optional JSON
fields:

| key                 | meaning |
|---------------------|---------|
| `root_proof`        | `"v1:" + lowercase hex(HMAC-SHA256(key, message))`. Present when the owner minted a code for this join. |
| `root_proof_status` | `"none"` when the authority proves roots but held no live code for this join (it was already used or has expired). Absent otherwise. It only shapes the message: a missing proof is refused whatever this says. |

The derivation (`internal/edgeauth/code.go`):

```
domain  = "rogerai-edge-root-proof/v1"
code    = canonical code: 10 Crockford symbols, upper case, no separators
key     = PBKDF2-HMAC-SHA256(password = utf8(code),
                             salt     = utf8(domain + "\n" + nonce),
                             iterations = 200000, length = 32)
message = utf8(domain + "\n" + rootFP + "\n" + account + "\n" + nodeID + "\n" + nonce)
proof   = "v1:" + hex(HMAC-SHA256(key, message))
```

- `nonce` is the hex nonce the phone's own request carried: `Request.nonce` for an enrollment,
  `EdgeClaim.Request.nonce` for a claim.
- `rootFP` is lowercase hex SHA-256 of the root certificate's DER bytes (the PEM body, base64
  decoded). It is not a hash of the PEM text.
- `account` and `nodeID` are the answer's `account` and `node_id`.

Reading a typed code (`CanonicalCode`):

- Trim the input and upper-case it.
- Drop `-` and space.
- Read `I` and `L` as `1`, and `O` as `0`.
- Any other character outside `0123456789ABCDEFGHJKMNPQRSTVWXYZ` is refused. So is any length but 10.

## 2. Swift changes, file by file

### `RogerAI/Services/Edge/EdgeEnroll.swift`

1. Extend `Response`. Old authorities omit the new keys, so decoding them as optionals keeps old
   answers readable:

   ```swift
   struct Response: Codable, Equatable {
       var nodeID: String
       var account: String
       var cert: String
       var root: String
       var rootProof: String?        // "v1:<hex>", absent from an authority that predates proofs
       var rootProofStatus: String?  // "none" = no live code for this join

       enum CodingKeys: String, CodingKey {
           case account, cert, root
           case nodeID = "node_id"
           case rootProof = "root_proof"
           case rootProofStatus = "root_proof_status"
       }
   }
   ```

2. `post(_:to:)` must not lose the request's nonce. Its caller already holds the `Request`, so keep
   it there (see `EdgeService` below).

### New file: `RogerAI/Services/Edge/EdgeRootProof.swift`

This mirrors `internal/edgeauth/code.go` and `VerifyAnswer`. CryptoKit provides SHA-256 and HMAC.
PBKDF2 comes from CommonCrypto (`CCKeyDerivationPBKDF` with `kCCPRFHmacAlgSHA256`). No third-party
library is needed.

```swift
import Foundation
import CryptoKit
import CommonCrypto

enum EdgeRootProof {
    static let domain = "rogerai-edge-root-proof/v1"
    static let iterations: UInt32 = 200_000
    static let codeLength = 10
    static let lifetime: TimeInterval = 15 * 60   // ruling D4: a held answer is trusted this long
    static let attempts = 5                       // ruling D5: wrong codes before the answers are discarded
    static let alphabet = Array("0123456789ABCDEFGHJKMNPQRSTVWXYZ")

    enum Failure: Error, Equatable {
        case badCode            // not a code at all: not counted as a guess
        case noProof(spentOrExpired: Bool)
        case unknownVersion
        case mismatch           // the device that answered is not the Edge that showed this code
        case notUnderRoot       // the certificate does not chain to the root it came with
        case differentRoot      // the phone already holds another root
    }

    /// CanonicalCode in code.go.
    static func canonical(_ typed: String) -> String? {
        var out = ""
        for ch in typed.trimmingCharacters(in: .whitespacesAndNewlines).uppercased() {
            switch ch {
            case "-", " ": continue
            case "I", "L": out.append("1")
            case "O": out.append("0")
            default:
                guard alphabet.contains(ch) else { return nil }
                out.append(ch)
            }
        }
        return out.count == codeLength ? out : nil
    }

    /// Lowercase hex SHA-256 of the root certificate's DER.
    static func fingerprint(rootPEM: String) -> String? {
        guard let der = derFromPEM(rootPEM) else { return nil }
        return SHA256.hash(data: der).map { String(format: "%02x", $0) }.joined()
    }

    static func key(code: String, nonce: String) -> Data {
        var out = Data(count: 32)
        let salt = Data((domain + "\n" + nonce).utf8)
        let pw = Array(code.utf8)
        _ = out.withUnsafeMutableBytes { o in
            salt.withUnsafeBytes { s in
                CCKeyDerivationPBKDF(CCPBKDFAlgorithm(kCCPBKDF2),
                                     pw.map { CChar(bitPattern: $0) }, pw.count,
                                     s.bindMemory(to: UInt8.self).baseAddress, salt.count,
                                     CCPseudoRandomAlgorithm(kCCPRFHmacAlgSHA256), iterations,
                                     o.bindMemory(to: UInt8.self).baseAddress, 32)
            }
        }
        return out
    }

    static func proof(code: String, rootFP: String, account: String, nodeID: String, nonce: String) -> String {
        let msg = Data([domain, rootFP, account, nodeID, nonce].joined(separator: "\n").utf8)
        let mac = HMAC<SHA256>.authenticationCode(for: msg, using: SymmetricKey(data: key(code: code, nonce: nonce)))
        return "v1:" + Data(mac).map { String(format: "%02x", $0) }.joined()
    }

    /// VerifyRootProof in code.go. The comparison must be constant-time.
    static func verify(typedCode: String, resp: EdgeEnroll.Response, nonce: String) -> Failure? {
        guard let code = canonical(typedCode) else { return .badCode }
        guard let p = resp.rootProof, !p.isEmpty else {
            return .noProof(spentOrExpired: resp.rootProofStatus == "none")
        }
        guard p.hasPrefix("v1:") else { return .unknownVersion }
        guard let fp = fingerprint(rootPEM: resp.root) else { return .mismatch }
        let want = proof(code: code, rootFP: fp, account: resp.account, nodeID: resp.nodeID, nonce: nonce)
        return constantTimeEqual(want, p) ? nil : .mismatch
    }
}
```

`derFromPEM` strips the armour lines and base64-decodes the body. `constantTimeEqual` compares the
two strings' UTF-8 bytes without returning early.

### `RogerAI/Services/Edge/EdgeService.swift`

**Claim loop (`claimOnce` and `adopted`).** Today `claimOnce` calls `adopted(resp, ...)` on the
first 200 OK from any discovered authority, which stores the membership at once. Replace that with
hold, then verify:

1. Add the held-answer state:

   ```swift
   struct HeldAnswer { let resp: EdgeEnroll.Response; let nonce: String; let from: String; let at: Date }
   private(set) var held: [HeldAnswer] = []   // at most 4, the earliest received
   private(set) var wrongCodes = 0
   ```

2. `EdgeClaim.poll` must return the nonce it signed, as `.granted(Response, nonce: String)`. On
   `.granted`, append to `held` (keep the first 4, and count the extras for the warning). Then set
   `state = .awaitingCode` (a new `State` case). Do **not** call `adopted`, and do **not** stop
   claiming against other authorities yet.

3. Add `func enterCode(_ typed: String)`:
   - Drop every held answer older than `EdgeRootProof.lifetime`. If none remain, show "that answer
     arrived more than 15 minutes ago - adopt this phone again for a fresh code".
   - For each held answer, in the order received, run in order:
     1. the chain check (below);
     2. if a root is already held (re-admit), its root must equal the held root, else
        `.differentRoot`;
     3. otherwise `EdgeRootProof.verify`.
   - Keep the first answer that passes: call `adopted(resp, authority: from)`, clear `held`, and
     reset `wrongCodes`.
   - Every other held answer is reported once: "<address> answered as an Edge authority but could
     not prove its root (offered root XXXX-XXXX-XXXX-XXXX)". Nothing from it is stored.
   - A `.badCode` is not counted. A `.mismatch` from every answer counts one wrong code.
   - When `wrongCodes == EdgeRootProof.attempts`, discard `held` and say: "the code did not match 5
     times: a device on your network may be impersonating the authority. Nothing was kept; adopt
     this phone again for a fresh code."

**Typed-address join (`join(authority:name:)`).** Today it calls `saveMembership` straight after
`EdgeEnroll.post`. Instead:
- Keep `req.nonce`.
- Hold the answer in the same `held` list.
- Go to `.awaitingCode`. The join form already collects the address; add a code field so the owner
  can type both at once, and verify immediately when a code is present.

**Re-admit.** `leave()` deletes the whole membership, root included. That is fine. A phone that kept
its root (one that never left) passes check 2 with no code. A reset phone needs the fresh code that
`roger edge readmit` prints.

**Chain check (fixes existing bug 2).** Before anything is stored, the answer's `cert` must chain to
its `root`. Certificates are P-256 ECDSA (`towercore/cert`), so `Security.framework` can verify:

```swift
let leaf = SecCertificateCreateWithData(nil, derFromPEM(resp.cert)! as CFData)!
let root = SecCertificateCreateWithData(nil, derFromPEM(resp.root)! as CFData)!
var trust: SecTrust?
SecTrustCreateWithCertificates(leaf, SecPolicyCreateBasicX509(), &trust)
SecTrustSetAnchorCertificates(trust!, [root] as CFArray)
SecTrustSetAnchorCertificatesOnly(trust!, true)
var err: CFError?
guard SecTrustEvaluateWithError(trust!, &err) else { return .notUnderRoot }
```

Also require the leaf's URI SAN to name the phone's own `node_id`, the same as Go's
`Authenticate`. Also require the leaf's public key to be the phone's node key, the same as
`ProveMatches`. Without these checks, an answer can carry a certificate for somebody else.

### `RogerAI/Features/Edge/EdgeView.swift`

- **`.awaitingCode` state.** Say: "An Edge answered. Enter the code shown where this phone was
  adopted." Show the short fingerprint of each root offered, as `XXXX-XXXX-XXXX-XXXX` (the first 16
  hex characters, upper case, in groups of four). Add a code field, accepting either case, with or
  without the dash, and a Check button that calls `edge.enterCode`. While held, never draw the phone
  as a member.
- **Join form.** Add a "Code (from the authority)" field beside the address field, which is already
  at line ~322.
- **Failure messages.** Use the authority's own words where it gave them:

  | case | message |
  |---|---|
  | `.noProof(spentOrExpired: true)` | "this Edge holds no live code for this phone: it was already used or has expired" |
  | `.noProof(false)` | "that authority does not prove its root: update roger on it" |
  | `.unknownVersion` | "the authority is newer than this app: update the app" |

There is no "trust anyway" button (ruling D7). QR scanning is the next UX slice (D12); a pasted
invite string is enough until then.

## 3. Golden vectors

Both implementations must reproduce these, and a vector with any one field changed must fail in
both. The file is `internal/edgeauth/testdata/root_proof_vectors.json`; copy it into
`RogerAITests/`. The vectors were computed with Python's `hashlib`, independently of the Go code.

| code typed | canonical | root_fp | account | node_id | nonce | key (PBKDF2 output) | proof |
|---|---|---|---|---|---|---|---|
| `7KQ2M-9XH4P` | `7KQ2M9XH4P` | `5f0920045e1af32fd724bc3f3cc640a803ac9a16d30e268081f2fdc08539be4d` | `edge-local` | `n_3f9a1c2b7d4e5f60` | `00112233445566778899aabbccddeeff` | `b6f3e4472e65660b01bbc93eeb85a1b606fe90ce21c3a054d76e2d944a1dfafd` | `v1:a6510a9b012b00d275195f2960cef594e313ce6d250f7ee1bd633c9ffdb9404d` |
| `0000000000` | `0000000000` | `5f0920045e1af32fd724bc3f3cc640a803ac9a16d30e268081f2fdc08539be4d` | `edge-local` | `n_0000000000000000` | `ffffffffffffffffffffffffffffffff` | `53b58aa2e060c61a0fec8de497259a79ddc4202a3fc8d34649c0c2a9fb7fd723` | `v1:27a0ce0bae6c13c3ac3f2a37a6f591e83421dc782ad4d4eab6147f432530854b` |
| `ZZZZZ-ZZZZZ` | `ZZZZZZZZZZ` | `a702f86f60357ec0893f900c3145f152d10a8039e80b1f9b0eec836d4b45b6d9` | `acct-1` | `n_aaaaaaaaaaaaaaaa` | `0f1e2d3c4b5a69788796a5b4c3d2e1f0` | `50817702c74e01aad884a60683f7be2fec11005ef338e76013ab69a802609f61` | `v1:9ad7111502278ba8aa65135d8aae8300f606b458c11331b113e3666f1d7cdc74` |
| `ab1cd-ef2gh` | `AB1CDEF2GH` | `a702f86f60357ec0893f900c3145f152d10a8039e80b1f9b0eec836d4b45b6d9` | `owner@example` | `n_1234567890abcdef` | `deadbeefdeadbeefdeadbeefdeadbeef` | `061877fecb0509cbe4fc3dcbeb9cc1ea592dc41462543b5a7681a498574bc53b` | `v1:2411441f2b6db6354fd225dc5e66c585515f5ac327fe4adc1e9e961a05c2c543` |

The same file also carries:

- `codes`: transcriptions and the canonical form, or the error kind, for each. These include
  `1LQ2M-9XH4P` → `11Q2M9XH4P`, `O0Q2M-9XH4P` → `00Q2M9XH4P`, `7KQ2U-9XH4P` → refused (symbol), and
  `7KQ2MO9XH4P` → refused (length).
- `pins`: the accepted and refused full-fingerprint forms, for the pasted-invite path.

Suggested XCTest cases (`RogerAITests/EdgeRootProofTests.swift`):

1. Every `proofs` row reproduces `key` and `proof`.
2. Changing any one of code, root_fp, account, node_id, nonce or proof makes `verify` fail.
3. Every `codes` row canonicalizes or refuses as listed.
4. A `Response` JSON with no `root_proof` decodes. Verifying it gives `.noProof(false)`, or
   `.noProof(true)` when `root_proof_status` is `"none"`.
5. A `"v9:..."` proof gives `.unknownVersion`.

## 4. The two existing phone bugs this fixes

These are current behavior on `main` of roger-ios. They are the phone side of the gap the spec
closes.

1. **The claim loop keeps the first answer from any authority.** `EdgeService.claimOnce` polls
   every discovered peer whose advert carries `auth=`, plus the typed hint. It calls
   `adopted(resp, ...)` on the first 200 OK, which saves the membership, root included. Nothing
   checks that the answering device is the authority the owner adopted the phone on. Fix: section 2,
   hold then verify with the code.
2. **Certificates are stored without a chain check.** Both `join(authority:name:)` and `adopted`
   save `cert` and `root` straight from the JSON. Nothing checks that the certificate chains to the
   root, names this phone's node id, or binds this phone's node key. Fix: the chain check in
   section 2. Go has always done this (`edgeauth.CheckResponse`).

## 5. Mixed versions

- **An older app joining a new authority still works.** The new keys are ignored by the old
  `Codable`. That phone is unprotected until it updates, which is why `allow` and `adopt` print the
  warning (ruling D9).
- **A new app joining an older authority.** The answer carries no `root_proof`, so a code-only join
  is refused with "update roger on it" (ruling D8). The pasted-invite path carries the full
  fingerprint and works with an older authority, because only the phone checks it.
