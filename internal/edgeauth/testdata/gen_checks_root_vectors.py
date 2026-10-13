#!/usr/bin/env python3
"""Generate checks_root_vectors.json independently of the Go code.

A joiner that verifies the root proof says so with "checks_root": true and proves it with
"checks_root_sig" (internal/edgeauth/checksroot.go, ruling D9 fixed 2026-10-13):

    checks_root_sig = lowercase hex( Ed25519( key, canonical + "\n" + "rogerai-checks-root/v1" ) )

where canonical is the request's own signed form, unchanged:

    enrolment (and bearer join), key = the USER key that signs the request:
        "\n".join(["rogerai-edge-enroll/v1", account, node_id, node_key, name, kind, nonce, str(ts)])
    claim, key = the NODE key:
        "\n".join(["rogerai-edge-claim/v1", node_id, node_key, name, kind, nonce, str(ts)])

node_key is the lowercase hex of the node's Ed25519 public key; node_id is "n_" + the first 24 bytes of
SHA-256(node public key), hex. Ed25519 is the RFC 8032 reference in pure Python (hashlib only).
Run from this directory:  python3 gen_checks_root_vectors.py > checks_root_vectors.json
"""
import hashlib
import json

# --- RFC 8032 section 6 reference Ed25519 ---------------------------------------------------------
p = 2**255 - 19
d = -121665 * pow(121666, p - 2, p) % p
q = 2**252 + 27742317777372353535851937790883648493


def sha512(s):
    return hashlib.sha512(s).digest()


def point_add(P, Q):
    A, B = (P[1] - P[0]) * (Q[1] - Q[0]) % p, (P[1] + P[0]) * (Q[1] + Q[0]) % p
    C, D = 2 * P[3] * Q[3] * d % p, 2 * P[2] * Q[2] % p
    E, F, G, H = B - A, D - C, D + C, B + A
    return (E * F, G * H, F * G, E * H)


def point_mul(s, P):
    Q = (0, 1, 1, 0)
    while s > 0:
        if s & 1:
            Q = point_add(Q, P)
        P = point_add(P, P)
        s >>= 1
    return Q


def modp_inv(x):
    return pow(x, p - 2, p)


g_y = 4 * modp_inv(5) % p


def recover_x(y, sign):
    x2 = (y * y - 1) * modp_inv(d * y * y + 1)
    x = pow(x2, (p + 3) // 8, p)
    if (x * x - x2) % p != 0:
        x = x * pow(2, (p - 1) // 4, p) % p
    if (x & 1) != sign:
        x = p - x
    return x


g_x = recover_x(g_y, 0)
G = (g_x, g_y, 1, g_x * g_y % p)


def point_compress(P):
    zinv = modp_inv(P[2])
    x, y = P[0] * zinv % p, P[1] * zinv % p
    return int.to_bytes(y | ((x & 1) << 255), 32, "little")


def secret_expand(secret):
    h = sha512(secret)
    a = int.from_bytes(h[:32], "little")
    a &= (1 << 254) - 8
    a |= 1 << 254
    return a, h[32:]


def public_key(secret):
    a, _ = secret_expand(secret)
    return point_compress(point_mul(a, G))


def sign(secret, msg):
    a, prefix = secret_expand(secret)
    A = point_compress(point_mul(a, G))
    r = int.from_bytes(sha512(prefix + msg), "little") % q
    R = point_compress(point_mul(r, G))
    h = int.from_bytes(sha512(R + A + msg), "little") % q
    s = (r + h * a) % q
    return R + int.to_bytes(s, 32, "little")


# --- the vectors ----------------------------------------------------------------------------------
DOMAIN = "rogerai-checks-root/v1"
SEED_USER = "9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60"  # RFC 8032 test 1
SEED_NODE = "4ccd089b28ff96da9db6c346ec114e0f5b8a319f35aba624da8cf6ed4fb8a6fb"  # RFC 8032 test 2
SEED_NODE2 = "c5aa8df43f9f837bedb7442f31dcb7b166d38535076f094b85ce3a2e0b4458f7"  # RFC 8032 test 3
NONCE = "00112233445566778899aabbccddeeff"
TS = 1760400000


def node_id(pub):
    return "n_" + hashlib.sha256(pub).hexdigest()[:48]


def enroll(name, account, node_seed, jname, kind, nonce, ts):
    user_pub = public_key(bytes.fromhex(SEED_USER))
    npub = public_key(bytes.fromhex(node_seed))
    canonical = "\n".join(["rogerai-edge-enroll/v1", account, node_id(npub), npub.hex(), jname, kind, nonce, str(ts)])
    msg = (canonical + "\n" + DOMAIN).encode("utf-8")
    return {"name": name, "request": "enroll", "signer": "user key", "seed": SEED_USER, "public_key": user_pub.hex(),
            "account": account, "node_id": node_id(npub), "node_key": npub.hex(), "join_name": jname, "kind": kind,
            "nonce": nonce, "ts": ts, "canonical": canonical, "signed": msg.decode("utf-8"), "signed_hex": msg.hex(),
            "checks_root_sig": sign(bytes.fromhex(SEED_USER), msg).hex()}


def claim(name, node_seed, jname, kind, nonce, ts):
    seed = bytes.fromhex(node_seed)
    npub = public_key(seed)
    canonical = "\n".join(["rogerai-edge-claim/v1", node_id(npub), npub.hex(), jname, kind, nonce, str(ts)])
    msg = (canonical + "\n" + DOMAIN).encode("utf-8")
    return {"name": name, "request": "claim", "signer": "node key", "seed": node_seed, "public_key": npub.hex(),
            "node_id": node_id(npub), "node_key": npub.hex(), "join_name": jname, "kind": kind,
            "nonce": nonce, "ts": ts, "canonical": canonical, "signed": msg.decode("utf-8"), "signed_hex": msg.hex(),
            "checks_root_sig": sign(seed, msg).hex()}


rows = [
    enroll("enrol against a local authority (account empty)", "", SEED_NODE, "pocket", "mobile", NONCE, TS),
    enroll("enrol naming the local account", "edge-local", SEED_NODE, "pocket", "mobile", NONCE, TS),
    enroll("a bearer join is the same enrolment", "", SEED_NODE2, "gentle-ibex-14", "mobile",
           "ffeeddccbbaa99887766554433221100", TS + 61),
    claim("a claim after an adopt", SEED_NODE, "pocket", "mobile", NONCE, TS),
    claim("a claim with a non-ASCII name", SEED_NODE2, "atelier-é", "mobile", NONCE, TS + 1),
]
print(json.dumps({"domain": DOMAIN, "joined_by": "\n", "vectors": rows}, indent=2, ensure_ascii=False))
