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
// fails closed rather than open. It is also the ONLY refusal that turns on a timestamp now, and
// it says so in its own sentence rather than borrowing the replay one.
//
// # AND A THIRD THING, WHICH IS WHY THERE IS AN EPOCH
//
// The gate used to carry a process-start floor as well - `since = time.Now()`, refusing any
// request stamped before this process began - so that a redeploy inside the skew window did not
// hand back every signature captured before it. It compared a TOWER WALL CLOCK to a NODE-DOMAIN
// timestamp, which is the mistake the ring's own floor is careful to avoid, and it failed in
// both directions at once: a node leading by L kept its captured signatures replayable for L
// seconds after every restart, and a node lagging by L was refused for L seconds after every
// restart and told it had made a replay. Proved both ways - a 60s lead replayed after a restart
// and got the job; a 45s lag got a 401 saying "already been made".
//
// The comparison cannot be repaired, because a signature's only tie to time is the timestamp
// its signer chose: a fresh request from a node leading by L is byte-for-byte the same claim as
// a stale one from a node leading by L plus its age. Separating them needs memory of that node
// from before the restart, and a restart is the loss of exactly that memory.
//
// So the process is named in the signature instead. Server.epoch is minted per process, rides
// in the signed target as `?hub=`, and is published on every node-facing response so a client
// learns it and re-signs - one extra round trip per hub restart, which is rarer than a poll by
// several orders of magnitude. A captured request names the run it was made for, and that run
// is over. No clock is consulted, so there is nothing left for a clock to be wrong about.
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
	// hubParam names the hub PROCESS a request was signed for. See Server.epoch: it is what
	// makes a signature captured before a restart worthless after one, without any reasoning
	// about whose clock is ahead of whose.
	hubParam = "hub"
)

// HubEpochHeader carries the hub's process epoch on every node-facing response, including the
// 401 a client gets for not knowing it yet. It is how a client learns the value it must sign:
// there is no other channel - Core assigns the tower id but knows nothing about when a tower
// last restarted - and it is public by construction, since an on-path observer can read it off
// any response. Publishing it costs nothing, because knowing the epoch is not what an attacker
// lacks. Being able to SIGN over it is.
const HubEpochHeader = "X-Roger-Hub-Epoch"

// HubKeyHeader and HubProofHeader are what turn the epoch above from a value the ANSWERING
// PARTY chose into a value THIS TOWER chose.
//
// # THE HOLE THEY CLOSE
//
// The epoch is published on an unauthenticated 401 over a channel that is plaintext by
// construction, and a client that simply believed it would re-sign against whatever it was
// told. So anyone on the path could answer a node's poll with a forged "401 + a made-up epoch"
// and collect a genuine Ed25519 signature over a target naming that epoch, with a fresh nonce
// and a fresh timestamp - not a replay of anything, but an UNCONSUMED signature no nonce ring
// has recorded. The epoch CHECK below was always exact; its PROVENANCE was not, and everything
// the epoch bought was conditional on that 401 being honest.
//
// # WHAT THEY CARRY
//
// HubKeyHeader is this tower's ADMITTED IDENTITY KEY in hex - the Ed25519 key Core enrolled it
// under and verifies its every request against - and HubProofHeader is that key's signature
// over hubEpochStatement: the label, the tower id, the epoch, and THE NONCE OF THE REQUEST
// BEING REFUSED. Core hands the node the key's fingerprint in the attach response, so the node
// checks the epoch against a key it got from the party it already trusts for the tower id, the
// endpoint and the grant key, rather than against whoever answered the socket.
//
// The key material is public and the proof is over public values; nothing here is a secret and
// publishing it costs nothing. What an attacker cannot do is produce the signature.
//
// # WHY THE NONCE IS IN THE STATEMENT
//
// Without it the proof is a bearer token for an epoch: captured once, it would let an on-path
// attacker point a node at a dead epoch whenever it suited them. Binding the client's own
// freshly minted nonce makes the proof answer one request and no other. The cost is one
// signature per epoch refusal rather than one per process - paid only on the refusal path, and
// bounded with the rest of the pre-auth work by the listener's connection cap.
const (
	HubKeyHeader   = "X-Roger-Hub-Key"
	HubProofHeader = "X-Roger-Hub-Proof"
)

// hubEpochProofLabel domain-separates this statement from every other use of the tower's
// identity key - the link Hello, the settle forward, the audit forward. A key that signs two
// kinds of statement with no label is a key whose signatures can be moved between them.
const hubEpochProofLabel = "rogerai tower hub epoch proof v1"

// hubEpochStatement is the exact bytes a hub signs and a node verifies. One function for both
// sides, for the same reason hubTarget is one function for both sides of the request target: a
// second copy of a canonical form is how a signing scheme grows a hole.
func hubEpochStatement(towerID, epoch, nonce string) []byte {
	return []byte(hubEpochProofLabel + "\n" + towerID + "\n" + epoch + "\n" + nonce)
}

// HeaderDoorTS and HeaderDoorSig carry the DOOR SIGNATURE: a second, cheaper signature over
// this request's method and target WITH NO BODY, which is the only kind of proof a hub can check
// before it has read the body.
//
// # WHY A PUBLIC KEY COULD NOT BE THE ADMISSION CREDENTIAL
//
// knownCredential exists because /complete and /audit/transcript must read the whole body before
// they can authenticate anything - the signature covers a digest of the bytes that arrived, so
// verifying a re-serialization would verify the wrong thing. It asked "is this X-Roger-Pubkey a
// key this tower has registered for SOMEBODY", and that question has a free answer: the pubkey
// is on the plaintext wire on every single poll, and a hostile Station on the same tower holds a
// registered one BY DEFINITION - its own. So the door opened for anybody, and behind it sat a
// 16MB buffer, a two-minute read timeout and no connection cap. Twelve and a half megabytes were
// buffered pre-auth in the review's reproducer, presenting nothing but a public key.
//
// # WHAT THIS IS AND IS NOT
//
// It is a proof of POSSESSION, not an authorization. It says the caller holds the private half
// of a key this tower has registered, which is the one thing a header-only check can establish
// and the one thing a public identifier never could. authNode still decides everything, against
// the Station the body names, with the full signature over the bytes that actually arrived.
//
// It is DOMAIN-SEPARATED from the real signature by the method it covers (see doorMethod), so a
// door signature can never be presented as a request signature or the reverse. It is NOT
// recorded in the nonce ring, deliberately: the ring is bounded precisely by "nothing is stored
// until a signature has verified against a named Station", and a pre-auth write would hand an
// attacker the growable map that ordering exists to deny them. So it is REPLAYABLE inside the
// skew window by someone on the path - who could equally just drop the packet - and it is not
// replayable by anyone else, which is the population that was making this tower buffer megabytes
// for free.
//
// The remaining pre-auth cost is one Ed25519 verify per request that names a registered key, and
// the listener's connection cap is what bounds that (cmd/roger-tower/hub.go).
const (
	HeaderDoorTS  = "X-Roger-Hub-Door-TS"
	HeaderDoorSig = "X-Roger-Hub-Door-Sig"
)

// doorMethod domain-separates the door signature from the request signature by putting a label
// where CanonicalRequest expects the method. Same canonical form, same verifier, no fork - and
// no string that a real HTTP method could ever equal, so the two signatures are good for exactly
// one thing each.
func doorMethod(method string) string { return "roger-hub-door-v1 " + method }

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

// newEpoch mints one hub process epoch. Same randomness and same failure posture as newNonce:
// a hub that could not read crypto/rand would otherwise mint a predictable epoch, which is the
// one property this value has to have.
func newEpoch() string {
	raw := make([]byte, nonceBytes)
	if _, err := rand.Read(raw); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(raw)
}

// Epoch is this hub run's identity, for a caller that mounts the Server itself and wants to
// hand it to a client out of band. Nothing in production needs it - clients learn it from
// HubEpochHeader - but a test that builds requests by hand does.
func (s *Server) Epoch() string { return s.epoch }

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
func hubTarget(towerID, hubEpoch, path string, q url.Values) string {
	q.Set(nonceParam, newNonce())
	if towerID != "" {
		q.Set(towerParam, towerID)
	}
	if hubEpoch != "" {
		q.Set(hubParam, hubEpoch)
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
//
// IT HAS NO PROCESS-START FLOOR ANY MORE, and deleting one is the fix rather than a
// simplification. It used to hold `since = time.Now()` - a TOWER WALL CLOCK - and refuse any
// request whose `ts` was before it. `ts` is a NODE-domain unix second, and comparing the two is
// exactly the mistake the ring's own floor documents avoiding two fields down ("these are
// request timestamps, not wall clocks, so a node with a consistently offset clock is measured
// against its own past").
//
// The consequences ran in both directions and neither was small. A node whose clock LEADS the
// tower's by L stamps everything L in the future, so every signature captured in the L seconds
// before a redeploy was still above the floor after it - the replay the floor existed to refuse,
// accepted, dequeuing the victim's job. And a node whose clock LAGS by L is refused for L
// seconds after every redeploy, up to the full five-minute skew, which is five minutes of an
// honest operator not earning per deploy - the precise harm this whole file says it exists to
// prevent, and it told them they had made a replay.
//
// No amount of care with that comparison can fix it, and it is worth saying why rather than
// leaving the next person to re-derive it: a signature's only tie to time is the timestamp its
// signer chose, so a fresh request from a node leading by L and a stale one from a node leading
// by L+age are the same bytes with the same claim. The tower cannot separate offset from age.
// A floor in the node's own domain would need memory of that node from before the restart,
// which is the one thing a restart destroys.
//
// So the restart hole is closed by binding the signature to the PROCESS instead of to a moment
// - see Server.epoch. That is the same move the previous round made for the tower id, one level
// finer, and it needs no clock at all.
type nonceGate struct {
	mu    sync.Mutex
	rings map[string]*nonceRing
}

// admit records a nonce for a Station and returns "" if the request may proceed, or the reason
// it may not: it must carry a nonce this ring has not seen AND a timestamp newer than anything
// the ring has forgotten.
//
// IT RETURNS A REASON RATHER THAN A BOOL because the two refusals are not the same event and
// the node can only act on one of them. "This exact request has already been made" is true of a
// verbatim replay and false of everything else, and it used to be printed for both - so a node
// pushed behind its own floor was told it had replayed a request it had never made, on a file
// whose authResult doc says the point is that a node "would otherwise poll into a wall forever
// without saying why". Saying the wrong why is worse than saying nothing, because it sends the
// operator looking for a second copy of their node.
//
// ts is the request's own signed timestamp; now is the tower's clock, which decides rotation.
// The caller must have verified the request's signature first. That ordering is what bounds
// this map: see "BOUNDING THE CACHE".
func (g *nonceGate) admit(stationID, nonce string, ts, now time.Time) string {
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
	// At-or-before the ring's floor, not strictly before: a request timestamp is unix SECONDS,
	// and a timestamp equal to one this ring has already forgotten is exactly the replay the
	// floor is there to refuse.
	if !ts.After(r.floor) {
		return "this request is older than the oldest one this tower still remembers for this " +
			"Station, so it cannot be checked for replay and is refused; if this machine's " +
			"clock is behind, correcting it will fix this"
	}
	if _, seen := r.cur[nonce]; seen {
		return replayedWhy
	}
	if _, seen := r.prev[nonce]; seen {
		return replayedWhy
	}
	r.cur[nonce] = struct{}{}
	r.tombstoned = false
	if ts.After(r.curMax) {
		r.curMax = ts
	}
	return ""
}

// replayedWhy is what a VERBATIM replay is told, and nothing else is told it. Tests pin the
// wording, which is the point: the last version of this sentence was pinned while being wrong
// for one of the two cases that reached it.
const replayedWhy = "this exact request has already been made - a replay is refused"

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
// IT IS TWO MAP LOOKUPS, NOT A SCAN. It was a linear walk of every registered Station calling
// hex.EncodeToString per station, under the read lock authNode also needs - so an
// unauthenticated stranger sending a header got a thousand allocations and a thousand
// comparisons per request on a thousand-station tower, on the one lock the serving path
// contends for. That is a CPU-and-lock amplifier standing where a memory amplifier used to be,
// which is not a trade worth making. The hex is precomputed at RegisterNode instead
// (setKeyIndexLocked), where it is paid once per registration rather than once per hostile
// packet.
//
// The token half is answered by an index too, and it is deliberately NOT constant-time: this
// door reveals only "somebody on this tower has this token", the same fact a 401-versus-204 on
// the real route reveals, and authLegacyBearer still does the constant-time compare against the
// ONE token registered for the Station the request actually names. Making the index
// constant-time would mean walking every token, which is the scan this is removing.
func (s *Server) knownCredential(r *http.Request) bool {
	pubHex := strings.ToLower(strings.TrimSpace(r.Header.Get(protocol.HeaderPubkey)))
	tok := bearer(r)
	if pubHex == "" && tok == "" {
		return false
	}
	s.mu.RLock()
	knownKey := pubHex != "" && s.keyHex[pubHex] > 0
	// The latch cannot be consulted here - this door does not know which Station the caller
	// claims to be, which is the whole reason it exists (on /complete and /audit/transcript the
	// Station is named inside the body nobody has read yet). So a token registered for ANY
	// unsigned Station opens the door, and authNode still refuses it for a Station that has
	// signed. Admission, not authorization.
	knownToken := tok != "" && s.allowLegacyBearer && s.tokens[tok] > 0
	s.mu.RUnlock()
	// A REGISTERED KEY IS NOT ENOUGH; POSSESSION OF IT IS. The map lookup is first because it is
	// free and an unregistered key must cost this tower nothing at all; the verify runs only for
	// a key this tower actually knows. See HeaderDoorSig for why the public half could never
	// have been the credential.
	if knownKey && s.doorProved(r, pubHex) {
		return true
	}
	// THE BEARER HALF IS UNCHANGED, and it cannot be improved without breaking the promise the
	// bearer exists to keep: a node old enough to present a token cannot produce a door
	// signature, so requiring one here would be AllowLegacyBearer=false in disguise. What keeps
	// it bounded is that a token is at least a SECRET rather than a public identifier - a
	// hostile Station holds its own and not a stranger's - and that this whole path is deleted
	// with the bearer one release from now.
	return knownToken
}

// doorProved verifies the door signature: possession of the private half of the key named in the
// request, over this request's method and target with no body. See HeaderDoorSig.
//
// It hands protocol.VerifyRequest a nil body, which is not the same as "the body is unchecked" -
// it is a signature over a DIFFERENT statement, one that deliberately says nothing about the
// body because the body has not been read. The real signature, over the real bytes, is still
// the only thing that authorizes anything.
func (s *Server) doorProved(r *http.Request, pubHex string) bool {
	ts, err := strconv.ParseInt(r.Header.Get(HeaderDoorTS), 10, 64)
	if err != nil {
		return false
	}
	_, ok := protocol.VerifyRequest(pubHex, r.Header.Get(HeaderDoorSig), ts,
		doorMethod(r.Method), requestTarget(r), nil)
	return ok
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
	// THE HUB PROCESS, for the same reason and by the same means. A tower id is stable across a
	// redeploy, and the nonce ring is not: a hub that restarts inside the skew window remembers
	// no nonce and would accept every signature captured before it went down. The epoch is
	// minted per process, rides in the signed target, and is handed back on this very response
	// (HubEpochHeader) so a client that does not know it yet learns it and re-signs. An on-path
	// attacker can read the new epoch as easily as the client can - and cannot sign over it,
	// which is the only thing that matters.
	// TWO CAUSES, TWO SENTENCES. "Carries no epoch" and "carries the wrong epoch" are different
	// events for the node reading them, and telling the first one it has "restarted since" sends
	// an operator hunting a redeploy that did not happen - the same class of mistake the nonce
	// gate's two refusals were separated for one round ago. A client's very FIRST request to a
	// hub carries no epoch by construction (there is no other way to learn one), so the empty
	// case is the ordinary opening move rather than a fault, and it should read like one.
	switch hub := r.URL.Query().Get(hubParam); {
	case hub == "":
		return authResult{why: "this signature names no hub run: a signed hub request carries the " +
			"epoch this hub published in the " + HubEpochHeader + " header, so sign again with it " +
			"(the first request to a hub never has it, and this is that answer)"}
	case hub != s.epoch:
		return authResult{why: "this signature was made for a different run of this hub - it has " +
			"restarted since; re-sign against the epoch in the " + HubEpochHeader + " header"}
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
	if why := s.nonces.admit(stationID, nonce, time.Unix(ts, 0), time.Now()); why != "" {
		return authResult{why: why}
	}
	// THE LATCH. This Station has now proved, by doing it, that its node signs - so the bearer
	// token Core still sends for it is not a credential here any more. Set after every other
	// check so that only a request that fully authenticated can flip it, and written under the
	// same lock the map is read under.
	if !signsAlready {
		s.mu.Lock()
		s.signed[stationID] = true
		s.mu.Unlock()
		// AND IT OUTLIVES THIS PROCESS. A latch that died with the hub handed the stolen bearer
		// its whole life back on every redeploy, because Core never rotates HubToken - see
		// SignedLatchStore. Written after the in-memory flip and outside the lock: the flip is
		// what this request depends on, and a slow disk must not hold the serving path's write
		// lock. Best effort - a store that will not write leaves the latch correct for this
		// process, which is where this started.
		if s.latchStore != nil {
			_ = s.latchStore.Add(stationID)
		}
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
