#!/usr/bin/env python3
"""Generate sas_vectors.json independently of the Go package.

The matching phrase (internal/joinsas, features/join/approve.feature section 2) is:

    commit = sha256(domain + "/commit\\n" + client_nonce_hex)
    digest = sha256 over, for each field in order
             [domain, client_key, tower_key, client_eph, tower_eph, client_nonce, tower_nonce]:
             4-byte big-endian byte length, then the field's ASCII hex
    v      = first 8 digest bytes, big-endian
    phrase = words[v>>53 & 2047]-words[v>>42 & 2047]-words[v>>31 & 2047]-((v & (2^31-1)) % 100, two digits)

Only the standard library is used. The "realistic" transcript is a fixed set of key, ephemeral and
nonce values (generated once at random and kept here), so the output is reproducible. Regenerate,
from the repository root, with:

    python3 internal/joinsas/testdata/gen_sas_vectors.py > internal/joinsas/testdata/sas_vectors.json

The output must be byte-identical to the committed file.
"""
import hashlib
import json
import os
import struct
import sys

DOMAIN = "join-sas/v1"
HERE = os.path.dirname(os.path.abspath(__file__))
WORDS_PATH = os.path.join(HERE, "..", "words.txt")

FIELDS = ["client_key", "tower_key", "client_eph", "tower_eph", "client_nonce", "tower_nonce"]

ED_A = "64dbdc38ede19b85cac8beccc15d52debb1a30e42c2fa15716ce95ac0913ad09"
ED_B = "293b1186e4eddc2c56cc08582f8236981d633d1023691c7d637bae459f377c8c"
X_A = "32f082ab89c8db3363b293220f78fc0778d5b5a94229579796c827dbf2936f82"
X_B = "01da896f83d25b6b0dbf6e04b2b93b1634368a1e93a0d6bf050946a12139ee41"
N_A = "ec171061adabf7194b3176daf1a7ea4428cf15fc6dd8f092413b5f7b90f26763"
N_B = "34c34fdaae7513ba2d488c8aa78c6e6ddfc5b78422be761513484d1b8cc60eb9"


def transcript(*values):
    return dict(zip(FIELDS, values))


REALISTIC = transcript(ED_A, ED_B, X_A, X_B, N_A, N_B)

CASES = [
    ("short fields (the package test's sample)", transcript("aa", "bb", "cc", "dd", "01", "02")),
    ("realistic: ed25519 keys, x25519 ephemerals, 32-byte nonces", REALISTIC),
    ("all fields empty", transcript("", "", "", "", "", "")),
    ("field boundary: ab|c", transcript("ab", "c", "", "", "", "")),
    ("field boundary: a|bc", transcript("a", "bc", "", "", "", "")),
    ("realistic with client and tower keys swapped", transcript(ED_B, ED_A, X_A, X_B, N_A, N_B)),
]


def phrase(words, domain, t):
    h = hashlib.sha256()
    for f in [domain] + [t[k] for k in FIELDS]:
        b = f.encode("ascii")
        h.update(struct.pack(">I", len(b)))
        h.update(b)
    v = int.from_bytes(h.digest()[:8], "big")
    return "%s-%s-%s-%02d" % (
        words[(v >> 53) & 2047],
        words[(v >> 42) & 2047],
        words[(v >> 31) & 2047],
        (v & (2**31 - 1)) % 100,
    )


def commit(domain, t):
    return hashlib.sha256((domain + "/commit\n" + t["client_nonce"]).encode("ascii")).hexdigest()


def main():
    raw = open(WORDS_PATH, "rb").read()
    words = raw.decode("ascii").split()
    assert len(words) == 2048, len(words)
    out = {
        "domain": DOMAIN,
        "commit_preimage": 'sha256(domain + "/commit\\n" + client_nonce_hex)',
        "phrase_derivation": "sha256 over, for each field in order [domain, client_key, tower_key, client_eph, "
        "tower_eph, client_nonce, tower_nonce]: 4-byte big-endian byte length then the field's ASCII hex; "
        "v = first 8 digest bytes big-endian; words[v>>53&2047]-words[v>>42&2047]-words[v>>31&2047]-"
        "((v & (2^31-1)) % 100 as two digits)",
        "words_sha256": hashlib.sha256(raw).hexdigest(),
        "words_count": len(words),
        "generated_by": "python3 hashlib, independently of the Go package",
        "vectors": [
            {"name": name, "transcript": t, "phrase": phrase(words, DOMAIN, t), "client_commit": commit(DOMAIN, t)}
            for name, t in CASES
        ],
        "wrong_domain_phrase": {
            "domain": "join-sas/v2",
            "vector": CASES[1][0],
            "phrase": phrase(words, "join-sas/v2", REALISTIC),
        },
    }
    sys.stdout.write(json.dumps(out, indent=2))


if __name__ == "__main__":
    main()
