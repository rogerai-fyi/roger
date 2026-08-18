package towerhub

// nodeauth.go is how a serving node proves to a tower's hub that it is the node a Station
// belongs to. It replaces a reusable bearer token with a SIGNATURE over each request.
//
// # WHY THE TOKEN HAD TO GO
//
// The hub link is structurally plaintext. Both places a relay endpoint enters the system
// validate it with net.SplitHostPort (internal/towercore/link/towerlink.go on the tower's
// Hello, cmd/roger-tower/serve.go on its own configuration), and net.SplitHostPort refuses
// anything carrying a scheme - so internal/agent's hubBaseURL has only ever been able to
// produce "http://host:port", and a TLS-fronted hub is unreachable by construction. That was
// survivable for CONTENT: the job and its answer are sealed to keys the relay does not hold,
// and an on-path observer sees the same ciphertext the relay does.
//
// It was not survivable for the CREDENTIAL. The old scheme put a per-Station bearer token -
// minted once at attach, never rotated, never expiring - in an Authorization header on every
// long poll, forever. Anyone on the path could lift it and poll the victim's queue: not to
// read the work (they cannot open it) but to SWALLOW it. The honest node stops being handed
// jobs, stops earning, and the consumer sees failures. That is a targeted denial-of-earnings
// primitive, and it was live for every signed-in `roger share` on a hostile network from the
// moment joining the relay fabric became automatic.
//
// Signing removes the stealable thing rather than hiding it. It needs no certificates and
// imposes nothing on tower operators, which is why it ships before TLS rather than after.
//
// # THE SCHEME IS THE HOUSE SCHEME
//
// protocol.SignRequest / protocol.VerifyRequest, unchanged: method + target + unix timestamp
// + sha256(body), signed Ed25519, carried in X-Roger-Pubkey / X-Roger-TS / X-Roger-Sig. It is
// exactly how the node already authenticates to Core (see internal/agent's AttachTower), and
// the key is one the Station already holds and Core already records on the attachment - the
// ASSERTION key it signs its receipts with. Nothing new is minted, distributed or rotated.
//
// The one thing the house scheme does not carry is a NONCE, and the hub needs one. Rather
// than fork the canonical string, the nonce rides in the request TARGET - `?nonce=<hex>` -
// which protocol.CanonicalRequest already binds, because the target is the field the scheme
// hands to the caller to fill. A header would have needed a second canonical form, and two
// canonical forms is how a signing scheme grows a hole.
//
// # WHAT A REPLAY ACHIEVES, ROUTE BY ROUTE
//
// The house scheme is timestamp-window based (protocol.SigMaxSkew, five minutes), so a
// captured signature is reusable inside that window unless something else refuses it. The
// question is what a reuse actually BUYS, and the answer differs per route:
//
//   - POST /complete is idempotent by construction. hub.Complete consumes the waiter once
//     and ConsumeDispatched consumes the dispatch record once, so a second identical
//     completion delivers nothing and couriers nothing. A replay is a no-op.
//   - POST /audit/transcript clears the want on the first success, so a replay answers an
//     attempt that is no longer listed and is refused a courier ride.
//   - GET /audit/wanted is a read whose response the on-path attacker is already watching in
//     the clear. A replay tells them what they just saw.
//   - GET /poll DEQUEUES. A replay takes a job the attacker cannot open and the honest node
//     therefore never serves. This is the whole attack, and it survives the timestamp window
//     for a reason worth stating plainly: a node long-polls continuously, so an on-path
//     attacker holds a FRESH signature every twenty-five seconds and never runs out of
//     unexpired ones. Timestamp skew alone would narrow "steal the token once, deny forever
//     from anywhere" to "stay on the path and deny continuously" - a real narrowing, and not
//     the fix this was supposed to be.
//
// So the hole is closed rather than documented, with a nonce cache. It is applied to EVERY
// route and not only to /poll, because a per-route exemption is a trap for whoever adds the
// next route: the safe default has to be the one you get by not thinking about it.
//
// # BOUNDING THE CACHE
//
// A nonce cache is an attacker-growable map, so the growth path is the design:
//
//  1. The signature is verified BEFORE the nonce is recorded. An unauthenticated request -
//     which is every request an attacker can compose that is not a verbatim replay - is
//     refused without touching the cache at all. A verbatim replay is refused by the cache
//     without adding to it. So only the holder of the Station's assertion private key can
//     make it grow, and that holder is the honest node.
//  2. Entries are per Station, so one station cannot evict another's.
//  3. Each Station's set is TWO GENERATIONS, rotated when the live one is older than
//     protocol.SigMaxSkew or holds maxNoncesPerStation entries. Checking both generations
//     means a nonce is remembered for at least one full rotation interval after it is
//     recorded, which is exactly the window in which its timestamp is still acceptable.
//     Memory per Station is bounded at 2 x maxNoncesPerStation regardless of traffic.
//
// THE RESIDUAL, stated precisely: if a Station's own key holder signs more than
// maxNoncesPerStation requests inside protocol.SigMaxSkew, rotation runs early and the oldest
// nonces are forgotten while their timestamps are still valid, so those specific requests
// become replayable for the remainder of their window. At the real poll cadence (one poll per
// worker per 25s long-poll, one audit sweep per 45s) a node needs hours to reach the cap; a
// node that reaches it in five minutes is hammering its own tower with its own key, which is
// a fleet-management problem and not an outsider's lever.

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"rogerai.fm/roger/v5/internal/protocol"
)

// nonceParam is the query parameter carrying each hub request's anti-replay nonce. It is in
// the query rather than a header so protocol.CanonicalRequest binds it unmodified - see the
// scheme note above.
const nonceParam = "nonce"

// nonceBytes is how much randomness a nonce carries. 16 bytes makes an accidental collision
// (which would refuse an honest request) impossible in practice at any traffic a hub sees.
const nonceBytes = 16

// maxNoncesPerStation caps one Station's live nonce generation. See "BOUNDING THE CACHE".
const maxNoncesPerStation = 2048

// NodeAuth is what a tower knows about the node serving one Station: the key it must have
// signed with, and - for one transition release only - the bearer token an older node still
// presents. Core hands both to the tower over /tower/hub/nodes.
type NodeAuth struct {
	// AssertionKey is the Station's Ed25519 assertion key, recorded on its attachment at
	// Core. It is the SAME key the Station's receipts are verified against, which is the
	// point: a node that can be paid can authenticate, with nothing extra to distribute.
	AssertionKey ed25519.PublicKey
	// LegacyToken is the pre-signature bearer credential. It exists so a node built before
	// signatures keeps earning across one release; see AllowLegacyBearer. Empty for a
	// Station with no token on its attachment, and destined for deletion.
	LegacyToken string
}

// Signer produces the three header values that authenticate one hub request: the hex public
// key, the timestamp, and the hex signature over protocol.CanonicalRequest. It is a function
// rather than a key so the private half never has to leave the package that owns it -
// station.Station.SignRequest satisfies it directly.
type Signer func(method, target string, body []byte) (pubHex string, ts int64, sigHex string)

// SignWith adapts a raw Ed25519 private key to Signer. It is for callers that hold the key
// itself - test harnesses, and any future in-process node - rather than a Station.
func SignWith(priv ed25519.PrivateKey) Signer {
	return func(method, target string, body []byte) (string, int64, string) {
		return protocol.SignRequest(priv, method, target, body)
	}
}

// newNonce mints one request nonce. crypto/rand cannot fail on any platform this runs on, and
// a signing path that silently degraded to a predictable nonce would be worse than a stop.
func newNonce() string {
	raw := make([]byte, nonceBytes)
	if _, err := rand.Read(raw); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(raw)
}

// hubTarget builds the request target - path plus sorted, escaped query - that a hub call
// both SENDS and SIGNS. One function for both so the two can never drift by a character,
// which in a signing scheme is the difference between working and 401.
//
// url.Values.Encode sorts by key, so the ordering is a property of the encoder rather than of
// the caller's map iteration.
func hubTarget(path string, q url.Values) string {
	q.Set(nonceParam, newNonce())
	return path + "?" + q.Encode()
}

// requestTarget is the server's reconstruction of what the client signed: the raw path and
// raw query exactly as they arrived. RawQuery rather than a re-encode of the parsed values,
// so no normalization of ours can turn a valid signature into an invalid one.
//
// A hub mounted under a path PREFIX would produce a target the client never signed, and every
// request would fail closed. That is the correct direction to fail, and the hub is mounted at
// the root (cmd/roger-tower/hub.go) - but it is the reason this is written down.
func requestTarget(r *http.Request) string {
	if r.URL.RawQuery == "" {
		return r.URL.Path
	}
	return r.URL.Path + "?" + r.URL.RawQuery
}

// validNonce bounds what a nonce may be before it is stored. Only a Station's own key holder
// can ever reach the store (signatures are verified first), so this is not defending against
// an outsider - it stops a buggy or hostile node turning the cache into an arbitrary-length
// string heap, and it keeps the wire format one thing rather than whatever anyone sends.
func validNonce(n string) bool {
	if len(n) < 2*nonceBytes || len(n) > 64 {
		return false
	}
	_, err := hex.DecodeString(n)
	return err == nil
}

// nonceRing is one Station's replay memory: two generations, checked together and rotated as
// a pair, so an entry survives at least a full rotation interval without any per-entry
// bookkeeping or a sweeper goroutine.
type nonceRing struct {
	cur     map[string]struct{}
	prev    map[string]struct{}
	rotated time.Time
}

// nonceGate is the Server's replay guard across all Stations.
type nonceGate struct {
	mu    sync.Mutex
	rings map[string]*nonceRing
}

// fresh records a nonce for a Station and reports whether it had NOT been seen. A false
// answer is a replay (or a collision, which at 128 bits of randomness it is not).
//
// The caller must have verified the request's signature first. That ordering is what bounds
// this map: see "BOUNDING THE CACHE".
func (g *nonceGate) fresh(stationID, nonce string, now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.rings == nil {
		g.rings = map[string]*nonceRing{}
	}
	r, ok := g.rings[stationID]
	if !ok {
		r = &nonceRing{cur: map[string]struct{}{}, prev: map[string]struct{}{}, rotated: now}
		g.rings[stationID] = r
	}
	if now.Sub(r.rotated) >= protocol.SigMaxSkew || len(r.cur) >= maxNoncesPerStation {
		r.prev, r.cur, r.rotated = r.cur, map[string]struct{}{}, now
	}
	if _, seen := r.cur[nonce]; seen {
		return false
	}
	if _, seen := r.prev[nonce]; seen {
		return false
	}
	r.cur[nonce] = struct{}{}
	return true
}

// forget drops a Station's replay memory when the Station itself is dropped. A
// re-registration starts clean, which is safe: a nonce from before the drop can only be
// replayed inside its timestamp window, and a Station that left and came back inside five
// minutes is served by a node that will not reuse a nonce anyway.
func (g *nonceGate) forget(stationID string) {
	g.mu.Lock()
	delete(g.rings, stationID)
	g.mu.Unlock()
}

// authResult says whether a hub request is the Station's own node, and - when it is not -
// gives the node a sentence it can act on rather than a bare 401. The relay plane is best
// effort and silent by default (cmd/rogerai/relayfabric.go), so a node that can no longer
// authenticate would otherwise poll into a wall forever without saying why.
type authResult struct {
	ok  bool
	why string
}

// authNode authenticates a hub request as the registered node for stationID.
//
// body is the exact bytes read from the request (nil for a GET) - the signature covers their
// digest, so the handler must read the body BEFORE calling this and hand over what it read,
// never a re-serialization.
func (s *Server) authNode(r *http.Request, stationID string, body []byte) authResult {
	if stationID == "" {
		return authResult{why: "this request names no Station"}
	}
	s.mu.RLock()
	node, known := s.nodes[stationID]
	legacy := s.AllowLegacyBearer
	s.mu.RUnlock()
	if !known {
		return authResult{why: "no node is registered for this Station on this tower"}
	}

	pubHex := r.Header.Get(protocol.HeaderPubkey)
	sigHex := r.Header.Get(protocol.HeaderSig)
	tsHdr := r.Header.Get(protocol.HeaderTS)
	if pubHex == "" && sigHex == "" && tsHdr == "" {
		return s.authLegacyBearer(r, node, legacy)
	}

	// FROM HERE THE REQUEST CLAIMS TO BE SIGNED, and a signed request is never allowed to
	// fall back to the bearer path. A downgrade an attacker can provoke - by stripping the
	// signature headers, or by answering 401 until the node gives up on them - is not a
	// security property, so there is exactly one way in per request and the claim decides it.
	if len(node.AssertionKey) != ed25519.PublicKeySize {
		return authResult{why: "this tower holds no assertion key for that Station, so it cannot " +
			"check a signature: its registration predates signed polls and Roger Core has not " +
			"re-sent it yet"}
	}
	if !strings.EqualFold(pubHex, hex.EncodeToString(node.AssertionKey)) {
		return authResult{why: "signed by a key that is not this Station's attached assertion key"}
	}
	ts, err := strconv.ParseInt(tsHdr, 10, 64)
	if err != nil {
		return authResult{why: "the request timestamp is not a unix second count"}
	}
	if _, ok := protocol.VerifyRequest(pubHex, sigHex, ts, r.Method, requestTarget(r), body); !ok {
		return authResult{why: "the signature does not verify for this method, path and body, " +
			"or its timestamp is outside the accepted window (check this machine's clock)"}
	}
	// ONLY NOW is anything recorded. Everything above rejects without storing, which is what
	// keeps the nonce cache un-growable by anyone but the key holder.
	nonce := r.URL.Query().Get(nonceParam)
	if !validNonce(nonce) {
		return authResult{why: "a signed hub request carries a hex " + strconv.Itoa(nonceBytes) +
			"-byte nonce in its query"}
	}
	if !s.nonces.fresh(stationID, nonce, time.Now()) {
		return authResult{why: "this exact request has already been made - a replay is refused"}
	}
	return authResult{ok: true}
}

// authLegacyBearer is the pre-signature path, kept for exactly one release. See
// Server.AllowLegacyBearer for why it exists and what ends it.
func (s *Server) authLegacyBearer(r *http.Request, node NodeAuth, allowed bool) authResult {
	if !allowed {
		return authResult{why: "this hub requires a signed request; bearer tokens are no longer accepted"}
	}
	if node.LegacyToken == "" {
		return authResult{why: "this request is unsigned and this Station has no legacy token: " +
			"sign hub requests with the Station's assertion key"}
	}
	tok := bearer(r)
	if tok == "" {
		return authResult{why: "this request carries neither a signature nor a token"}
	}
	if !constantTimeEqual(node.LegacyToken, tok) {
		return authResult{why: "not the registered node for this Station"}
	}
	return authResult{ok: true}
}
