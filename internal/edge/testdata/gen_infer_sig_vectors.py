#!/usr/bin/env python3
"""Generate infer_sig_vectors.json independently of the Go code.

A phone's request to the Edge authority for a band (internal/edge/phoneconsume.go,
features/edge/phone_consume.feature) is signed with its node key over:

    canonical = utf8("\n".join([
        "rogerai-edge-infer/v1",
        node_id,        # "n_" + hex(sha256(node public key)[:24])
        action,         # "bands" | "infer" | "speech"
        target,         # "" for any member, else the Roger-Target header exactly
        body_sha256,    # lowercase hex SHA-256 of the exact HTTP body bytes ("" body included)
        nonce,          # the Roger-Nonce header (hex of 16 random bytes)
        str(ts),        # the Roger-TS header, unix seconds, decimal
    ]))
    sig = Ed25519(node private key, canonical)   # the Roger-Sig header, lowercase hex

Ed25519 here is the RFC 8032 reference algorithm in pure Python (hashlib only), so the vectors do
not lean on the Go library they check. Run from this directory:
    python3 gen_infer_sig_vectors.py > infer_sig_vectors.json
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
DOMAIN = "rogerai-edge-infer/v1"


def node_id(pub):
    return "n_" + hashlib.sha256(pub).hexdigest()[:48]


def row(name, seed_hex, action, target, body, nonce, ts):
    seed = bytes.fromhex(seed_hex)
    pub = public_key(seed)
    nid = node_id(pub)
    body_bytes = body.encode("utf-8")
    body_sha = hashlib.sha256(body_bytes).hexdigest()
    canonical = "\n".join([DOMAIN, nid, action, target, body_sha, nonce, str(ts)]).encode("utf-8")
    return {
        "name": name,
        "seed": seed_hex,
        "public_key": pub.hex(),
        "node_id": nid,
        "action": action,
        "target": target,
        "body": body,
        "body_sha256": body_sha,
        "nonce": nonce,
        "ts": ts,
        "canonical": canonical.decode("utf-8"),
        "canonical_hex": canonical.hex(),
        "sig": sign(seed, canonical).hex(),
    }


SEED_A = "9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60"  # RFC 8032 test 1 secret
SEED_B = "4ccd089b28ff96da9db6c346ec114e0f5b8a319f35aba624da8cf6ed4fb8a6fb"  # RFC 8032 test 2 secret
NONCE = "00112233445566778899aabbccddeeff"
TS = 1760400000
CHAT = '{"model":"qwen3-8b","messages":[{"role":"user","content":"hello"}]}'
STREAM = '{"model":"qwen3-8b","stream":true,"messages":[{"role":"user","content":"hello"}]}'
SPEAK = '{"model":"kokoro","input":"roger that","voice":"af_heart"}'

rows = [
    row("list the bands (empty body)", SEED_A, "bands", "", "", NONCE, TS),
    row("a turn, any member", SEED_A, "infer", "", CHAT, NONCE, TS),
    row("a turn addressed to a member", SEED_A, "infer", "jetson", CHAT, NONCE, TS),
    row("a turn addressed to node/instance", SEED_A, "infer", "jetson/serve", CHAT, NONCE, TS),
    row("a streamed turn", SEED_A, "infer", "", STREAM, NONCE, TS),
    row("a spoken line", SEED_A, "speech", "", SPEAK, NONCE, TS),
    row("another phone, another nonce and time", SEED_B, "infer", "",
        CHAT, "ffeeddccbbaa99887766554433221100", TS + 61),
    row("non-ASCII in the body and the target", SEED_B, "infer", "atelier-é",
        '{"model":"qwen3-8b","messages":[{"role":"user","content":"¿qué tal? 日本"}]}', NONCE, TS),
]

print(json.dumps({"domain": DOMAIN, "joined_by": "\n", "vectors": rows}, indent=2, ensure_ascii=False))
