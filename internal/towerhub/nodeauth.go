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
// The two things the house scheme does not carry, the hub needs, and both ride in the request
// TARGET rather than in a header - because protocol.CanonicalRequest already binds the target,
// and a second canonical form is how a signing scheme grows a hole:
//
//   - A NONCE (`?nonce=<hex>`), so one signed request is one request. See below.
//   - THE TOWER ID (`?tower=<id>`), so one signed request is one request AT THIS HUB. The
//     canonical string binds the method, the target, the timestamp and a body digest - not the
//     HOST - so nothing in a captured signature said where it was going. Both sides already
//     know the id (Core assigns it at attach and hands the tower its own), so binding it costs
//     nothing and needs no fork of CanonicalRequest.
//
// # A SIGNATURE IS GOOD AT ONE HUB, ONCE - AND THE TOWER ID IS ONLY A THIRD OF THAT
//
// This is worth being exact about, because the tower id is the obvious fix and on its own it
// would have been a decorative one. The nonce ring is per PROCESS and in memory, and the three
// ways a captured signature actually came back to life all involve the SAME tower id:
//
//   - THE HUB RESTARTS. A redeploy inside the five-minute window is a Tuesday, and the new
//     process remembers nothing. Closed by nonceGate.since: nothing signed before this process
//     started is accepted.
//   - CORE'S ANSWER BRIEFLY OMITS THE STATION. The refresher unregisters it, and forgetting its
//     ring used to mean re-registration started clean. Closed by the tombstone in forget: the
//     memory goes, the floor stays.
//   - TWO HUB PROCESSES ANSWER ONE ENDPOINT. NOT CLOSED, and it cannot be by anything in this
//     file: two processes cannot agree on a nonce without shared state. A tower runs one hub
//     process per endpoint today (the settle spool and this ring both assume it), and that is
//     now a deployment CONSTRAINT rather than an accident - written down in
//     docs/relay-selection-design.md section 5 so that whoever puts a load balancer in front of
//     two of these knows what they are turning off.
//
// What the tower id itself buys is the fourth case: a signature cannot be carried to a
// DIFFERENT tower that happens to have the same Station registered. Core scopes its node list
// by tower so that should not arise - but "should not arise" is a property of a handler at
// Core, and this is a property of the bytes.
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
//     nonceRetention or holds maxNoncesPerStation entries. Memory per Station is bounded at
//     2 x maxNoncesPerStation regardless of traffic.
//
// # TWO THINGS THE FIRST VERSION OF THIS GOT WRONG
//
// Both were found by independent review after it shipped to this branch, and both are the same
// kind of mistake: a bound that was reasoned about rather than enforced. A replay gate that is
// nearly right is a replay gate that is wrong, so the reasoning is now written beside the code
// that makes it true.
//
// RETENTION HAS TO COVER THE WHOLE ACCEPTANCE SPAN, NOT HALF OF IT. protocol.VerifyRequest
// accepts a timestamp within SigMaxSkew in EITHER direction, so a single signature is
// acceptable across 2 x SigMaxSkew of tower time - not one. Rotating generations on SigMaxSkew
// therefore forgot a nonce while its own signature was still good: if the signing node's clock
// LEADS the tower's by L, the request stays acceptable for L past the moment the gate stopped
// remembering it, and a captured poll dequeues a job after two rotations. Proved end to end
// with a six-second lead against a real HTTP hub. nonceRetention is 2 x SigMaxSkew, and the
// invariant it exists for is stated where it is enforced.
//
// The cheaper fix - refuse a timestamp more than a few seconds in the FUTURE, since no node
// has a legitimate need to be ahead of its tower - was rejected on purpose. Plenty of nodes are
// ahead: an unsynchronised clock is the ordinary condition of a machine somebody runs in a
// spare room, and this hub refusing it is a node that silently stops earning. Remembering
// longer costs bounded memory. Refusing costs an honest operator their income, which is the
// exact harm this whole file exists to prevent, so the memory is the right thing to spend.
//
// THE CAP IS AN OUTSIDER'S LEVER, WHICH THE FIRST VERSION DENIED IN SO MANY WORDS. It claimed
// a node needed hours to reach maxNoncesPerStation at the real cadence and that reaching it was
// "a fleet-management problem and not an outsider's lever". Wrong on both counts: the nonce is
// recorded when the request AUTHENTICATES, which is before the long poll blocks, and
// ServeLoop's floor on an empty poll cycle is 200ms - so an on-path attacker who forwards each
// poll and answers 204 himself turns the node into a signing oracle at about five requests per
// second per worker and evicts two full generations in a couple of minutes, inside one skew
// window. Proved: after 4104 genuine signed polls, a poll captured before them was accepted and
// dequeued the job.
//
// So eviction is bounded by a FLOOR rather than by a claim about traffic. Every rotation
// records the newest timestamp the dropped generation ever held, and a request whose timestamp
// is at or before that floor is refused outright. Either the gate still remembers the nonce or
// it refuses the timestamp - there is no window between the two, at any traffic, for any cap.
// The floor is measured in the SIGNING NODE'S clock domain (it is a ts, not a wall clock), so a
// node whose clock is consistently off is compared against its own past rather than ours; at
// the ordinary cadence it sits two generations behind and refuses nothing.
//
// THE RESIDUAL, stated precisely: an attacker who drives a Station's own node to sign faster
// than maxNoncesPerStation per generation pushes that Station's floor forward, and a request
// timestamped behind the floor is then refused - including an honest one from a node whose
// clock LAGS by more than the storm is long. That is a denial available to anyone who can do
// the driving, since being on the path already means being able to drop the request, and it
// fails closed rather than open.
//
// # THE LEGACY BEARER, AND WHAT ENDS IT
//
// A node released before this change presents a bearer token and cannot sign, so a hub built
// after it accepts either for one release (Server.AllowLegacyBearer) rather than taking a
// provider who did nothing off the fabric. The first version of that was too generous by a long
// way, and it undid the change for exactly the population it was written for: the token was
// registered for EVERY Station, including ones whose node had upgraded and was already signing,
// and Core returns the same token forever (it is never rotated). So a token lifted off the
// cleartext wire at any point BEFORE a node upgraded still opened that node's queue afterwards,
// repeatably, from off-path, for a whole release. Those operators are the ones who ran the
// vulnerable build on a hostile network, and upgrading bought them nothing.
//
// The test is BEHAVIOUR, not a version or a claim - the same discipline Core's audit leniency
// uses. The first request this tower verifies as a genuine signature from a Station proves that
// Station's node signs, and from that instant the bearer is refused for that Station. An old
// node never signs and keeps earning; an upgraded node closes its own hole on its first poll,
// seconds after it starts. An attacker holding the token cannot produce the signature that
// flips the latch and cannot unflip it, because the only thing that clears it is the Station
// being dropped by Core.
//
// TWO THINGS DELIBERATELY NOT DONE. Registering the token only when the tower holds no
// assertion key is the obvious one-liner - and Core sends an assertion key for every
// self-attached Station, so it would refuse every un-upgraded node on the fleet:
// AllowLegacyBearer=false wearing a disguise, on a promise made to operators one commit ago.
// Rotating the token at Core on each re-attach is the other, and against this attacker it is
// theatre: a node old enough to present a bearer presents it in the clear every twenty-five
// seconds, so the attacker captures the replacement as easily as the original. A bearer on this
// link is only ever safe once the node holding it stops sending it, which is the thing the
// latch detects.

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

// nonceParam is the query parameter carrying each hub request's anti-replay nonce, and
// towerParam the one naming the hub the request was signed FOR. Both are in the query rather
// than in headers so protocol.CanonicalRequest binds them unmodified - see the scheme note
// above.
const (
	nonceParam = "nonce"
	towerParam = "tower"
)

// nonceBytes is how much randomness a nonce carries. 16 bytes makes an accidental collision
// (which would refuse an honest request) impossible in practice at any traffic a hub sees.
const nonceBytes = 16

// maxNoncesPerStation caps one Station's live nonce generation. See "BOUNDING THE CACHE".
const maxNoncesPerStation = 2048

// nonceRetention is how long a generation lives before it rotates on age, and it is NOT
// protocol.SigMaxSkew: a timestamp is accepted up to SigMaxSkew in either direction, so one
// signature is acceptable across TWICE that span of tower time, and a gate that forgets sooner
// hands the difference to whoever captured the request. See "TWO THINGS THE FIRST VERSION OF
// THIS GOT WRONG".
const nonceRetention = 2 * protocol.SigMaxSkew

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
	//
	// It is accepted only until this tower sees the Station SIGN once - see the latch in
	// authNode. Registering it says "this Station may still be running an old node", never
	// "this Station's queue is open to whoever holds this string".
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
// towerID is the hub this request is FOR, and an empty one is written as no parameter at all
// rather than as an empty value: a Server compares the parameter to its own id with plain
// equality, so "no tower named" and "a tower with no id" have to be the same string on the
// wire. In production neither is empty - Core assigns the id and tells both sides.
//
// url.Values.Encode sorts by key, so the ordering is a property of the encoder rather than of
// the caller's map iteration.
func hubTarget(towerID, path string, q url.Values) string {
	q.Set(nonceParam, newNonce())
	if towerID != "" {
		q.Set(towerParam, towerID)
	}
	return path + "?" + q.Encode()
}

// requestTarget is the server's reconstruction of what the client signed: the raw path and
// raw query exactly as they arrived. RawQuery rather than a re-encode of the parsed values,
// so no normalization of ours can turn a valid signature into an invalid one.
//
// EscapedPath rather than Path, so the reconstruction is unambiguous. Path is percent-DECODED,
// and concatenating a decoded path with a raw query lets two different requests produce one
// identical canonical string: `/poll?station=st-1&nonce=N` and `/poll%3Fstation=st-1&nonce=N`
// did exactly that. Neither of today's four routes reads anything from its path, so nothing was
// exploitable - but "one canonical string means one request" was false, and the next route that
// takes a path segment is where that stops being harmless.
//
// A hub mounted under a path PREFIX would produce a target the client never signed, and every
// request would fail closed. That is the correct direction to fail, and the hub is mounted at
// the root (cmd/roger-tower/hub.go) - but it is the reason this is written down.
func requestTarget(r *http.Request) string {
	if r.URL.RawQuery == "" {
		return r.URL.EscapedPath()
	}
	return r.URL.EscapedPath() + "?" + r.URL.RawQuery
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
	// curMax/prevMax are the newest request timestamp each generation has held, and floor is
	// the newest one this ring has ever FORGOTTEN. A request at or before the floor is refused
	// on sight, which is what makes early (size-driven) rotation safe: the gate never has to
	// answer "was this nonce one of the ones I dropped?", because everything that could have
	// been is already refused. These are request timestamps, not wall clocks, so a node with a
	// consistently offset clock is measured against its own past.
	curMax  time.Time
	prevMax time.Time
	floor   time.Time
	// tombstoned marks a ring whose Station has been unregistered: it holds a floor and no
	// entries, and it is swept once nothing it could refuse is inside its timestamp window any
	// more. See forget.
	tombstoned bool
}

// nonceGate is the Server's replay guard across all Stations.
type nonceGate struct {
	mu sync.Mutex
	// since is the floor this whole gate starts life with: nothing signed before this process
	// began is accepted by it. A nonce ring is memory, so a hub that restarts remembers
	// nothing - and a redeploy inside the five-minute window would otherwise hand every
	// signature an attacker captured before it a second life, with no attack needed beyond
	// waiting for a deploy. Refusing the era instead of remembering it costs a node with a
	// LAGGING clock one refused poll per lag-second after a restart, and stops mattering
	// altogether once the process is older than the skew window, because protocol's own
	// timestamp check refuses everything older than that anyway.
	since time.Time
	rings map[string]*nonceRing
}

// fresh records a nonce for a Station and reports whether the request may proceed: it must
// carry a nonce this ring has not seen AND a timestamp newer than anything the ring has
// forgotten. A false answer is a replay, a collision (which at 128 bits of randomness it is
// not), or a timestamp from before this Station's floor.
//
// ts is the request's own signed timestamp; now is the tower's clock, which decides rotation.
// The caller must have verified the request's signature first. That ordering is what bounds
// this map: see "BOUNDING THE CACHE".
func (g *nonceGate) fresh(stationID, nonce string, ts, now time.Time) bool {
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
	if now.Sub(r.rotated) >= nonceRetention || len(r.cur) >= maxNoncesPerStation {
		// The generation being dropped is the one that was already prev. Whatever it held is
		// now unanswerable, so its newest timestamp becomes the floor - the ring trades "I
		// remember that nonce" for "I refuse that whole era", which is the same refusal from
		// the attacker's side and costs one time.Time instead of unbounded memory.
		if r.prevMax.After(r.floor) {
			r.floor = r.prevMax
		}
		r.prev, r.prevMax = r.cur, r.curMax
		r.cur, r.curMax, r.rotated = map[string]struct{}{}, time.Time{}, now
	}
	// Strictly BEFORE for the gate floor and at-or-before for the ring's. The difference is
	// the resolution of the thing being compared: a request timestamp is unix SECONDS, so a
	// request signed in the same second the process started is a fresh request and must be
	// served, while a timestamp equal to one this ring has already forgotten is exactly the
	// replay the floor is there to refuse.
	if ts.Before(g.since) || !ts.After(r.floor) {
		return false
	}
	if _, seen := r.cur[nonce]; seen {
		return false
	}
	if _, seen := r.prev[nonce]; seen {
		return false
	}
	r.cur[nonce] = struct{}{}
	r.tombstoned = false
	if ts.After(r.curMax) {
		r.curMax = ts
	}
	return true
}

// forget releases a Station's replay memory when the Station itself is dropped, and leaves a
// TOMBSTONE where it was: the maps go, the floor stays.
//
// It used to delete the ring outright, and the old comment argued that was safe because a
// re-registered Station is served by a node that will not reuse a nonce. That reasons about the
// honest node and forgets the attacker, who is the only party a replay gate is for. The
// refresher unregisters any Station missing from a single answer from Core, so a transient
// omission and a re-registration - inside the five-minute window, entirely outside anybody's
// control - was enough to make every signature captured before it work again.
//
// The tombstone is one struct with two nil maps and a time in it, and it is swept once it is
// older than nonceRetention, at which point protocol's own timestamp check refuses everything
// it was protecting anyway. Sweeping here rather than on a timer means the cost is paid by the
// churn that creates it.
func (g *nonceGate) forget(stationID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if r, ok := g.rings[stationID]; ok {
		if r.prevMax.After(r.floor) {
			r.floor = r.prevMax
		}
		if r.curMax.After(r.floor) {
			r.floor = r.curMax
		}
		r.cur, r.prev = map[string]struct{}{}, map[string]struct{}{}
		r.curMax, r.prevMax = time.Time{}, time.Time{}
		r.rotated = time.Now()
		r.tombstoned = true
	}
	for id, r := range g.rings {
		if r.tombstoned && time.Since(r.rotated) > nonceRetention {
			delete(g.rings, id)
		}
	}
}

// authResult says whether a hub request is the Station's own node, and - when it is not -
// gives the node a sentence it can act on rather than a bare 401. The relay plane is best
// effort and silent by default (cmd/rogerai/relayfabric.go), so a node that can no longer
// authenticate would otherwise poll into a wall forever without saying why.
type authResult struct {
	ok  bool
	why string
}

// knownCredential is the CHEAP DOOR, and it exists to be called before a body is read.
//
// The signature covers a digest of the bytes that arrived, so /complete and /audit/transcript
// have to read the whole body before they can authenticate anything - that ordering is the
// same-slice design and it is not negotiable. What it meant in practice is that an
// unauthenticated stranger could make this tower buffer 16MB (8MB on the audit route) before
// being told no, on a listener with no connection cap and a two-minute read timeout. Proved
// with 8,388,608 wasted bytes.
//
// So this asks the one question that can be answered from the headers alone: does the caller
// present a credential this tower has registered for SOMEBODY? It cannot ask "for this
// Station", because on those two routes the Station is named INSIDE the body we have not read.
// That is fine - this is an admission gate, not an authorization. authNode still decides
// everything, against the Station the body names, with the signature over the bytes that
// actually arrived.
func (s *Server) knownCredential(r *http.Request) bool {
	pubHex := strings.ToLower(strings.TrimSpace(r.Header.Get(protocol.HeaderPubkey)))
	tok := bearer(r)
	if pubHex == "" && tok == "" {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for id, node := range s.nodes {
		if pubHex != "" && len(node.AssertionKey) == ed25519.PublicKeySize &&
			pubHex == hex.EncodeToString(node.AssertionKey) {
			return true
		}
		// The latch applies here too. A token belonging to a Station that has signed is not a
		// credential any more, so it does not buy the holder a body read either.
		if tok != "" && s.allowLegacyBearer && !s.signed[id] && node.LegacyToken != "" &&
			constantTimeEqual(node.LegacyToken, tok) {
			return true
		}
	}
	return false
}

// authNode authenticates a hub request as the registered node for stationID.
//
// body is the exact bytes read from the request - the signature covers their digest, so the
// handler must read the body BEFORE calling this and hand over what it read, never a
// re-serialization and never nil for a body that arrived. A GET has no body to sign, and hands
// over the empty read rather than nil, which hashes identically and means a GET that arrives
// carrying an unsigned body is refused instead of ignored.
func (s *Server) authNode(r *http.Request, stationID string, body []byte) authResult {
	if stationID == "" {
		return authResult{why: "this request names no Station"}
	}
	s.mu.RLock()
	node, known := s.nodes[stationID]
	signsAlready := s.signed[stationID]
	s.mu.RUnlock()
	if !known {
		return authResult{why: "no node is registered for this Station on this tower"}
	}

	pubHex := r.Header.Get(protocol.HeaderPubkey)
	sigHex := r.Header.Get(protocol.HeaderSig)
	tsHdr := r.Header.Get(protocol.HeaderTS)
	if pubHex == "" && sigHex == "" && tsHdr == "" {
		return s.authLegacyBearer(r, node, signsAlready)
	}

	// FROM HERE THE REQUEST CLAIMS TO BE SIGNED, and a signed request is never allowed to
	// fall back to the bearer path. A downgrade an attacker can provoke - by stripping the
	// signature headers, or by answering 401 until the node gives up on them - is not a
	// security property, so there is exactly one way in per request and the claim decides it.
	//
	// THE TOWER FIRST, before the ed25519 verify, because it is a string compare and this is
	// the check that answers a flood of signatures captured at some other hub. The id is public
	// - Core hands it to every node it places - so refusing on it leaks nothing.
	if r.URL.Query().Get(towerParam) != s.towerID {
		return authResult{why: "this signature names a different tower: a hub request is signed " +
			"for the hub it is sent to, and this one was not signed for this one"}
	}
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
	if !s.nonces.fresh(stationID, nonce, time.Unix(ts, 0), time.Now()) {
		return authResult{why: "this exact request has already been made - a replay is refused"}
	}
	// THE LATCH. This Station has now proved, by doing it, that its node signs - so the bearer
	// token Core still sends for it is not a credential here any more. Set after every other
	// check so that only a request that fully authenticated can flip it, and written under the
	// same lock the map is read under.
	if !signsAlready {
		s.mu.Lock()
		s.signed[stationID] = true
		s.mu.Unlock()
	}
	return authResult{ok: true}
}

// authLegacyBearer is the pre-signature path, kept for exactly one release. See
// Server.AllowLegacyBearer for why it exists, and the latch in authNode for what ends it per
// Station well before that.
func (s *Server) authLegacyBearer(r *http.Request, node NodeAuth, signsAlready bool) authResult {
	if !s.allowLegacyBearer {
		return authResult{why: "this hub requires a signed request; bearer tokens are no longer accepted"}
	}
	if signsAlready {
		// The point of the whole change, and the reason it is checked before the token is even
		// looked at: this Station's node has signed to this tower, so it is not the old build
		// the tolerance was written for, and whoever is presenting its token is not it.
		return authResult{why: "this Station's node authenticates by signature - a bearer token " +
			"is not accepted for it, whoever holds it"}
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
