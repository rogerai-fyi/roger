#!/usr/bin/env python3
"""Generate join_proof_vectors.json independently of the Go code.

The bearer join proof (internal/edgeauth/bearer.go, internal/joincode) is:

    domain  = "rogerai-edge-join/v1"
    key     = PBKDF2-HMAC-SHA256(utf8(code), utf8(domain + "\n" + nonce), 200000, 32)
    message = utf8("\n".join([domain, root_fp, user_key, node_id, node_key, nonce]))
    proof   = "v1:" + lowercase hex(HMAC-SHA256(key, message))

root_fp is the authority's root fingerprint as bare lowercase hex (an invite's "sha256:" prefix
is stripped before proving). Only the standard library is used, so the recipe is checkable without
roger. Run from this directory:  python3 gen_join_proof_vectors.py > join_proof_vectors.json
"""
import hashlib
import hmac
import json

JOIN = "rogerai-edge-join/v1"
ROOT = "rogerai-edge-root-proof/v1"
ITER = 200000

# Reuse the root-proof vectors' fingerprints and keys, so the phone can line the two files up.
ROOT_A = "5f0920045e1af32fd724bc3f3cc640a803ac9a16d30e268081f2fdc08539be4d"
ROOT_B = "a702f86f60357ec0893f900c3145f152d10a8039e80b1f9b0eec836d4b45b6d9"
USER_1 = "1f2e3d4c5b6a79880918273645546372819aabbccddeeff00112233445566778"
USER_2 = "99887766554433221100ffeeddccbbaa0123456789abcdef0123456789abcdef"
NODE_1 = "3f9a1c2b7d4e5f60a1b2c3d4e5f60718293a4b5c6d7e8f90"  # 24 bytes after n_
NODEKEY_1 = "0a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f9"
NODEKEY_2 = "f9e8d7c6b5a4938271605f4e3d2c1b0af9e8d7c6b5a4938271605f4e3d2c1b0a"


def canonical(typed: str) -> str:
    out = []
    for ch in typed.strip().upper():
        if ch in "- ":
            continue
        if ch in "IL":
            ch = "1"
        if ch == "O":
            ch = "0"
        out.append(ch)
    return "".join(out)


def proof(domain, code, nonce, fields):
    key = hashlib.pbkdf2_hmac("sha256", code.encode(), (domain + "\n" + nonce).encode(), ITER, 32)
    msg = "\n".join([domain, *fields, nonce]).encode()
    return key, "v1:" + hmac.new(key, msg, hashlib.sha256).hexdigest()


rows = [
    # name, typed code, root_fp, user_key, node_id, node_key, nonce, domain
    ("normal", "7KQ2M-9XH4P", ROOT_A, USER_1, "n_" + NODE_1, NODEKEY_1,
     "00112233445566778899aabbccddeeff", JOIN),
    ("boundary code all zeros", "0000000000", ROOT_A, USER_1, "n_" + "0" * 48, NODEKEY_1,
     "ffffffffffffffffffffffffffffffff", JOIN),
    ("boundary code all Z", "ZZZZZ-ZZZZZ", ROOT_A, USER_1, "n_" + NODE_1, NODEKEY_1,
     "00112233445566778899aabbccddeeff", JOIN),
    ("different root", "7KQ2M-9XH4P", ROOT_B, USER_1, "n_" + NODE_1, NODEKEY_1,
     "00112233445566778899aabbccddeeff", JOIN),
    ("different node key", "7KQ2M-9XH4P", ROOT_A, USER_1, "n_" + NODE_1, NODEKEY_2,
     "00112233445566778899aabbccddeeff", JOIN),
    ("different user key", "7KQ2M-9XH4P", ROOT_A, USER_2, "n_" + NODE_1, NODEKEY_1,
     "00112233445566778899aabbccddeeff", JOIN),
    ("replayed on another request (new nonce)", "7KQ2M-9XH4P", ROOT_A, USER_1, "n_" + NODE_1, NODEKEY_1,
     "deadbeefdeadbeefdeadbeefdeadbeef", JOIN),
    ("confusables typed", "ab1cd-ef2gh", ROOT_B, USER_2, "n_" + NODE_1, NODEKEY_2,
     "0f1e2d3c4b5a69788796a5b4c3d2e1f0", JOIN),
    # A proof made in the ROOT-PROOF domain over the join fields: a correct computation that must
    # NOT verify as a join proof.
    ("wrong domain (root-proof domain over the join fields)", "7KQ2M-9XH4P", ROOT_A, USER_1,
     "n_" + NODE_1, NODEKEY_1, "00112233445566778899aabbccddeeff", ROOT),
]

proofs = []
for name, typed, root, user, node_id, node_key, nonce, domain in rows:
    code = canonical(typed)
    key, p = proof(domain, code, nonce, [root, user, node_id, node_key])
    row = {
        "name": name,
        "code_typed": typed,
        "code": code,
        "root_fp": root,
        "user_key": user,
        "node_id": node_id,
        "node_key": node_key,
        "nonce": nonce,
        "key": key.hex(),
        "join_proof": p,
        "verifies": domain == JOIN,
    }
    if domain != JOIN:
        row["domain"] = domain
    proofs.append(row)

doc = {
    "comment": "Golden vectors for the bearer Edge join proof (features/join/invite.feature, roger edge invite). "
               "Computed independently of the Go code (Python hashlib, gen_join_proof_vectors.py). Shared with "
               "roger-ios: both implementations must reproduce every verifying proof, a vector with any one field "
               "changed must fail, and the row marked verifies=false (a root-proof-domain proof over the same "
               "fields) must never verify as a join proof.",
    "domain": JOIN,
    "root_domain": ROOT,
    "iterations": ITER,
    "key_bytes": 32,
    "salt": "utf8(domain + \"\\n\" + nonce)",
    "message": "utf8(domain + \"\\n\" + root_fp + \"\\n\" + user_key + \"\\n\" + node_id + \"\\n\" + node_key + \"\\n\" + nonce)",
    "root_fp": "bare lowercase hex; an invite's \"sha256:\" prefix is stripped before proving",
    "join_proof": "\"v1:\" + lowercase hex(HMAC-SHA256(key, message))",
    "proofs": proofs,
}
print(json.dumps(doc, indent=2))
