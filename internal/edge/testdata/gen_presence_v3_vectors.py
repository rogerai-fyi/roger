#!/usr/bin/env python3
"""Generate presence_v3_vectors.json independently of the Go code.

A presence v3 (internal/edge/presence.go, ruled 2026-10-13) is signed by the node key over these
lines, joined by "\n", every absent reading an EMPTY line (never a zero):

    "rogerai-edge-present/v3"
    node_id          "n_" + hex(sha256(node public key)[:24])
    name
    kind             host | board | mobile
    caps             comma-joined, in the order sent
    addr             "" for a node that only dials out
    nonce            32 lowercase hex
    ts               unix seconds
    thermal          phone only: nominal | fair | serious | critical | ""
    foreground       phone only: "1" | "0" | ""
    battery_pct      0..100 | ""
    charging         "1" | "0" | ""
    power_source     ac | battery | unknown | ""
    low_power        "1" | "0" | ""
    minutes_left     decimal >= 0 | ""

JSON: {"v":3, ..., "vitals":{"thermal","foreground"} (mobile only), "power":{"battery_pct",
"charging","source","low_power","minutes_left"} (any kind)}. Ed25519 is the RFC 8032 reference in
pure Python. Run from this directory:  python3 gen_presence_v3_vectors.py > presence_v3_vectors.json
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
DOMAIN = "rogerai-edge-present/v3"
NONCE = "00112233445566778899aabbccddeeff"
TS = 1760400000
SEED_PHONE = "9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60"
SEED_HOST = "4ccd089b28ff96da9db6c346ec114e0f5b8a319f35aba624da8cf6ed4fb8a6fb"
SEED_PI = "c5aa8df43f9f837bedb7442f31dcb7b166d38535076f094b85ce3a2e0b4458f7"


def b(v):
    return "" if v is None else ("1" if v else "0")


def i(v):
    return "" if v is None else str(v)


def row(name, seed_hex, jname, kind, caps, addr, nonce, ts, vitals, power):
    seed = bytes.fromhex(seed_hex)
    pub = public_key(seed)
    nid = "n_" + hashlib.sha256(pub).hexdigest()[:48]
    v = vitals or {}
    w = power or {}
    lines = [DOMAIN, nid, jname, kind, ",".join(caps), addr, nonce, str(ts),
             v.get("thermal", ""), b(v.get("foreground")),
             i(w.get("battery_pct")), b(w.get("charging")), w.get("source", ""), b(w.get("low_power")),
             i(w.get("minutes_left"))]
    canonical = "\n".join(lines).encode("utf-8")
    body = {"v": 3, "node_id": nid, "name": jname, "kind": kind, "caps": caps, "addr": addr,
            "nonce": nonce, "ts": ts}
    if vitals is not None:
        body["vitals"] = vitals
    if power is not None:
        body["power"] = power
    return {"name": name, "seed": seed_hex, "public_key": pub.hex(), "node_id": nid, "request": body,
            "canonical": canonical.decode("utf-8"), "canonical_hex": canonical.hex(),
            "sig": sign(seed, canonical).hex()}


rows = [
    row("a phone, every reading", SEED_PHONE, "pocket", "mobile", ["classify"], "", NONCE, TS,
        {"thermal": "fair", "foreground": True},
        {"battery_pct": 64, "charging": False, "source": "battery", "low_power": True, "minutes_left": 312}),
    row("a phone asleep, battery unknown", SEED_PHONE, "pocket", "mobile", [], "", NONCE, TS + 30,
        {"foreground": False}, {"low_power": False}),
    row("a laptop host with a battery, on AC and charging", SEED_HOST, "bench", "host", ["serve", "operate"],
        "192.168.1.20:8443", NONCE, TS, None,
        {"battery_pct": 0, "charging": True, "source": "ac", "low_power": False}),
    row("a host with no power block at all", SEED_HOST, "nas", "host", ["serve"], "192.168.1.21:8443",
        "ffeeddccbbaa99887766554433221100", TS, None, None),
    row("a Pi on a UPS HAT, external power lost", SEED_PI, "shed-pi", "host", [], "", NONCE, TS + 61, None,
        {"battery_pct": 100, "source": "battery", "minutes_left": 0}),
    row("a board whose source is unknown", SEED_PI, "sensor-1", "board", [], "", NONCE, TS, None,
        {"source": "unknown"}),
]
print(json.dumps({"domain": DOMAIN, "joined_by": "\n", "vectors": rows}, indent=2, ensure_ascii=True))
