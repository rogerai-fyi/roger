package agent

// tower.go is `roger share` serving THROUGH A TOWER (Option C, Topology 2) - the capability
// that used to require the separate roger-station binary and its invite-file ceremony, folded
// into the one binary providers actually run.
//
// # THE FLOW
//
//	1. The node mints (or reloads) its persistent STATION identity - the assertion key that
//	   signs receipts and the X25519 session key consumers seal requests to - beside its
//	   ordinary node key, under the same data dir.
//	2. It SELF-ATTACHES: one signed call to Roger Core with its keys, model, and ITS OWN
//	   per-token price. Core assigns a live tower and returns the hub endpoint + the bearer
//	   token this node polls with. A lost reply is safe: the same call is answered
//	   idempotently with the existing registration.
//	3. It pins Core's grant key (fetched from Core itself, not from the tower - the tower is
//	   exactly the party a forged grant would come from), and runs ServeLoop workers: poll
//	   the tower's hub, ServeSealed each job (open the sealed request, verify the grant,
//	   serve the local model, sign the TOKEN receipt, seal the answer to the consumer), and
//	   return it. The tower carries only ciphertext; settlement pays this node 90% of its
//	   listed price, the tower 10%, the platform 20%.
//
// # WHAT THE OPERATOR SEES
//
// Nothing. This runs beside an ordinary `roger share` (cmd/rogerai/relayfabric.go), best
// effort and silent: the node has already registered, gone on air and printed its one line by
// the time this starts, and the relay fabric is an ADDITIONAL plane it serves on rather than a
// fabric it was moved to. There is no flag - `roger share --tower` used to be one, and it was
// wrong in shape: it made a provider choose a serving fabric for the life of the process, when
// which relay carries a request is Core's decision at the moment a consumer tunes in. Prices
// are the share's ordinary $/1M-token prices, converted to the tower path's micro-USD
// integers.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	randv2 "math/rand/v2"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/station"
	"rogerai.fm/roger/v6/internal/towercore/link"
	"rogerai.fm/roger/v6/internal/towerhub"
)

// TowerAttachment is what self-attach resolved: where to poll, and as whom.
type TowerAttachment struct {
	StationID string `json:"station_id"`
	TowerID   string `json:"tower_id"`
	Endpoint  string `json:"endpoint"`
	// HubToken is the pre-signature bearer credential. THIS NODE NO LONGER SENDS IT: hub
	// requests are signed with the Station's assertion key instead (internal/towerhub's
	// nodeauth.go), which is what stops an on-path attacker on a plaintext link from lifting a
	// reusable credential and polling this Station's queue. It is still parsed because Core
	// still mints one for towers serving nodes that have not updated, and a field silently
	// dropped from a wire type is how the next reader concludes it was never there.
	HubToken string `json:"hub_token"`
	// TowerKeyHash is the fingerprint of the identity key Core ADMITTED this relay under - hex
	// sha256 of its raw Ed25519 public key. It is what lets this node tell the relay's own
	// statements from an on-path attacker's.
	//
	// It is needed for exactly one thing, and that one thing is load-bearing. A node signs every
	// hub request over a target naming the hub's PROCESS EPOCH, and it can only learn that value
	// from the hub's own 401 - which is unauthenticated, on a link that is plaintext by
	// construction. Believing it means signing over whatever the party in front of the node
	// names: a genuine Ed25519 signature, fresh nonce, fresh timestamp, over bytes no hub has
	// ever seen. With this fingerprint the node checks the hub's signature over its own epoch
	// instead (internal/towerhub, HubKeyHeader), so the epoch is the tower's value rather than
	// the attacker's.
	TowerKeyHash string `json:"tower_key_hash"`
	// EndpointTLSSPKI is the hub certificate pin Core published for Endpoint: hex sha256 over
	// the SubjectPublicKeyInfo of the certificate that hub presents. Non-empty means this node
	// polls over https and accepts THAT CERTIFICATE AND NO OTHER; empty means the relay serves
	// plaintext, which is what every relay did before this field existed and is still legal.
	//
	// IT IS A DIFFERENT KEY FROM TowerKeyHash, DOING A DIFFERENT JOB, and the two are worth
	// telling apart. TowerKeyHash is the relay's long-term IDENTITY, and it authenticates one
	// statement - the hub's process epoch - inside a channel anyone can read. This pin
	// authenticates the CHANNEL, so that everything else the hub says (a job, a 204, a 401, a
	// completion accepted) comes from the relay rather than from whoever is on the path, and so
	// that this station's assertion public key stops riding every poll in the clear.
	//
	// Optional on purpose: see ServeTower. Making it mandatory would take every relay whose
	// operator has not turned TLS on off the air, and that is the founder's call to make on a
	// date, not a side effect of shipping the capability.
	EndpointTLSSPKI string `json:"endpoint_tls_spki"`
	State           string `json:"state"`
	Note            string `json:"note"`
}

// microsPerDollarPer1M converts the share's float $/1M-token price to the tower path's
// integer micro-USD per 1M tokens. Rounded, not truncated: a float that lands a hair under
// the operator's listed price must not shave a micro off what they charge (audit N2).
func microsPerDollarPer1M(price float64) int64 {
	if price <= 0 {
		return 0
	}
	return int64(math.Round(price * 1_000_000))
}

// hubBaseURL turns Core's advertisement of a relay's data plane - an endpoint, and a hub
// certificate pin that may be empty - into a base URL and a client that will verify whatever
// answers, and says whether the result is PLAINTEXT.
//
// # THE COMMENT THAT USED TO BE HERE WAS FALSE, AND THE FIX IS WHY THIS SIGNATURE CHANGED
//
// It said "an endpoint that carries its own scheme is honored verbatim - this is how a
// TLS-fronted hub is reached". No such endpoint can exist. Both places a relay endpoint enters
// the system validate it with net.SplitHostPort - internal/towercore/link/towerlink.go on the
// tower's Hello, and cmd/roger-tower/serve.go on its own configuration - and
// net.SplitHostPort("https://relay.example:443") fails with "too many colons in address". So an
// endpoint carrying a scheme was refused at ingress and never reached here, the scheme branch
// was dead code, and the "http://" branch was the only one that had ever run. A tower operator
// who obtained a certificate and passed --hub-tls-cert got a TLS listener that every node in the
// fleet connected to in plaintext and failed against: the flags were not a path to safety, they
// were a trap.
//
// What replaced the dead branch is not a scheme in the endpoint - that would have been a
// breaking change to a field three clients concatenate onto - but a PIN advertised beside it.
// Core relays the fingerprint of the certificate the tower's hub presents, and this node accepts
// that certificate and no other. No public certificate authority is involved and no domain name
// is needed, which matters because the operators this fabric is built on are volunteers on home
// connections who can obtain neither. The whole argument is in internal/towerhub/pin.go.
//
// WHAT RIDES IN THE CLEAR ON AN UNPINNED LINK, AND WHAT NO LONGER DOES. Not the payload: the job
// and its result are sealed to keys the tower does not hold, and that was always true. It used
// to be the node's per-Station HUB BEARER TOKEN as well, on every long poll, forever - so
// anything on the path could capture it and poll that Station's queue until the attachment was
// revoked. That is gone: hub requests are SIGNED per request with the Station's assertion key
// (internal/towerhub's nodeauth.go), so what an observer captures authenticates nothing a second
// time. What is left is traffic shape, this station's assertion public key on every poll, and
// the fact that nothing authenticates the hub's ANSWERS. The second return value exists so the
// node can still say so, because "unencrypted" remains true of a relay whose operator has not
// turned TLS on, and the operator is owed the sentence.
func hubBaseURL(endpoint, pin string, hc *http.Client) (base string, client *http.Client, plaintext bool, err error) {
	base, client, err = towerhub.Reach(endpoint, pin, hc)
	if err != nil {
		return "", nil, false, err
	}
	return base, client, pin == "", nil
}

// ErrPrivateShareNeverRelays refuses to put a PRIVATE band on the public relay fabric.
//
// A private share is hidden from /discover and /market and routable only by frequency code; the
// relay fabric is public placement by definition. The two are mutually exclusive, and before the
// flag removal the CLI said so out loud (`--tower` with `--private` was a usage error).
//
// The refusal now lives HERE, at the network act, and not only in the branch structure of
// cmd/rogerai/main.go. As shipped, the only thing keeping a private band off the fabric was that
// `go joinRelayFabric(cfgRun)` happened to sit inside `if !*private {` - a placement, not a rule.
// Nothing in joinRelayFabric, ServeTower or towerEdgeAttach ever looked at the band. Merging the
// two agentStart branches - which the duplicated setup in that function visibly invites - would
// have published private bands to the public fabric with no compile error and no failing test.
var ErrPrivateShareNeverRelays = errors.New(
	"a private band is never offered to the relay fabric: it is reachable by frequency code only, " +
		"and the fabric is public placement")

// ErrCoreKeysUnpinned marks a failure to pin Roger Core's grant and envelope keys.
//
// It is a sentinel because of what the node cannot do without those keys: tell a real Core-signed
// grant from one the TOWER forged. The tower is the party in front of the node and the exact party
// a forged grant would come from, so this is not "a fetch failed", it is "the trust assumption
// this whole plane rests on is not established". It must never be swallowed.
var ErrCoreKeysUnpinned = errors.New("Roger Core's grant key could not be pinned")

// ErrHubChannelPlaintext is the standing notice that this node's hub link is unencrypted. It is
// carried as an error because it travels the channel errors travel - the one thing that is not
// swallowed - and because it is, in fact, a defect: see hubBaseURL.
//
// ITS TEXT CHANGED WHEN SIGNED POLLS SHIPPED, and the change is the point rather than a tidy-up.
// It used to say the polling token rides in the clear, which was true and was the reason to care.
// No credential is transmitted now, so repeating that sentence would be teaching operators to
// fear the wrong thing - and an alarm that overstates its case is the one people learn to skip.
//
// IT CHANGED AGAIN, because the first rewrite went one word too far. "Traffic shape" was not the
// whole residual: X-Roger-Pubkey puts the Station's long-term ASSERTION PUBLIC KEY on the wire on
// every single poll, in the clear. That is not a session token and not a nonce - it is the
// identity the node's receipts are verified against and its earnings are paid to, stable for the
// life of the station, and it makes every poll a linkable identifier tying that identity to an
// IP address, across networks, across towers, and across re-attachments. Nothing an attacker
// captures lets them TAKE anything, which is what the signing change bought; being permanently
// identifiable is a different harm and it belongs in the same sentence rather than under it.
//
// AND ONCE MORE, FOR TWO REASONS. The residual was still understated: nothing on an unpinned
// link authenticates the hub's ANSWERS, so a party on the path can inject the status codes this
// node reasons about its own work with - a 204 for "nothing to do" while real jobs go elsewhere,
// a 401 that reads as a revoked attachment. And the notice can now name a fix, which is the
// difference between a warning and a complaint: the relay's operator passes --hub-tls, Core
// publishes the fingerprint, and this node verifies it with no certificate authority and no
// domain name involved. A standing alarm nobody can act on is one people learn to skip.
var ErrHubChannelPlaintext = errors.New(
	"this node's relay hub link is UNENCRYPTED (plain http): the sealed job and its answer stay " +
		"private, and this node proves who it is by signing every request rather than by sending " +
		"anything reusable, so nothing an observer captures here works twice. Three things still " +
		"leak or bend. The shape of the traffic - when you poll, how big each job is - which your " +
		"relay operator can see in any case. Your station's ASSERTION PUBLIC KEY, which every " +
		"request carries in the clear: it is stable for the life of this station and it is the " +
		"key your receipts and your earnings are tied to, so anyone watching this link can link " +
		"that identity to this address, and anyone watching two links can tell it is the same " +
		"operator on both. And the relay's ANSWERS are unauthenticated, so anyone on the path can " +
		"feed this node a 204 or a 401 the relay never sent. The relay's operator closes all " +
		"three by running their hub with --hub-tls, which needs no certificate authority and no " +
		"domain name")

// ErrHubRefusedThisNode marks a hub that will not accept this node's identity at all - a 401 on
// the polling route, repeated, rather than a blip.
//
// It exists because of what the relay plane does with ordinary transport errors: retries them
// forever and prints them to a writer `roger share` discards. That is right for a tower that is
// down and wrong for a tower that has decided this node is nobody, which never resolves on its
// own. The most likely cause is a version split - a relay running a roger-tower from before
// signed polls cannot verify a signature and will refuse every request from a current node - and
// an operator can only act on that if someone tells them.
var ErrHubRefusedThisNode = errors.New(
	"this node's relay hub refuses its identity (401): it is not serving any work through this " +
		"relay. The usual cause is a relay running a roger-tower older than signed hub polls; a " +
		"revoked attachment and a badly wrong system clock look the same from here")

// Notice is how the relay plane reports something the operator must not miss.
//
// # WHY THIS IS NOT THE io.Writer
//
// ServeTower used to have one output seam, an io.Writer, carrying both "attached, serving" and
// "you did this work and will not be paid". `roger share` passes io.Discard for it - correctly,
// because the ordinary share has already printed its on-air line and a stream of relay progress
// underneath it would describe a plane the operator did not opt into. But one writer for two
// kinds of message means discarding one discards both, and what went into the bin included
// towerhub.ErrNotCarried (the hub took the completion and never couriered the receipt: the node
// computed and will not be paid), a failed result return, every audit failure, transcripts
// evicted inside their audit window, and the key-pinning failure above.
//
// So the writer was the wrong seam, not the wrong setting. Routine progress and consequential
// errors now travel separately: the first is still discardable, the second is not.
type Notice func(error)

// notify is Notice's nil-safe call.
func (n Notice) notify(err error) {
	if n != nil && err != nil {
		n(err)
	}
}

// hubRefusedIdentity reports whether a hub error is an authentication refusal - the one
// transport failure that will never come right by retrying, because nothing about the next
// request will differ from this one.
func hubRefusedIdentity(err error) bool {
	var he *towerhub.HTTPError
	return errors.As(err, &he) && he.Status == http.StatusUnauthorized
}

// costlyRelayError reports whether a worker-level error is one the operator must be told about,
// as opposed to the transport chatter a long-polling loop produces all day.
//
// The line is drawn at WORK DONE. A failed poll costs nothing - there was no job. A completion
// the hub would not take, or took and did not courier, means the GPU time was spent and nobody
// will pay for it. Everything else backs off and retries, which is what the loops are for.
func costlyRelayError(err error) bool {
	return errors.Is(err, towerhub.ErrNotCarried) || errors.Is(err, towerhub.ErrResultUndelivered)
}

// # RE-ATTACHMENT: WHAT A NODE DOES WHEN ITS RELAY STOPS BEING ITS RELAY
//
// Everything from here to ServeTower exists to close a hole that was not a bug in any one line:
// ServeTower attached ONCE per `roger share` process and the serve workers retried a failing hub
// every two seconds forever, so ANY permanent change on the relay's side stranded every node
// behind it until a human restarted the share. A tower turning TLS on did it. A certificate
// rotation did it, and does it again on every renewal. A tower going away, losing its lease or
// being revoked did it. So did anything else Core would answer differently if it were asked
// again - and nothing ever asked it again.
//
// WHY IT WAS QUIET, WHICH IS THE PART THAT MADE IT EXPENSIVE. Since the `--tower` flag was
// removed the same process holds an ordinary broker registration and long-poll throughout, so a
// stranded node keeps serving and keeps earning on the classic path. Nothing goes down. What
// stops is the relay plane, and the only symptom is an operator eventually noticing that one of
// their two income lines went to zero - if they were watching two.
//
// THE FIX IS NOT A TIMER. "Re-attach every N minutes" would put an attach at Core for every node
// in the fleet on a schedule, forever, in exchange for recovering a case that almost never
// happens; and because the event that strands one node strands every node on that tower at the
// same instant, the schedule would arrive as a spike rather than as a trickle. So the trigger is
// EVIDENCE - a relay that has stopped answering, continuously, for long enough that it is not
// having a bad minute - and the response is a jittered exponential backoff so a broken tower
// produces a slow drip of attaches rather than a stampede.
//
// # WHAT THIS RECOVERS, AND WHAT IT ONLY MAKES VISIBLE
//
// The list above is the list of things that STRANDED a node. It is not the list of things asking
// Core again fixes, and the two were written as though they were the same. Core's attach handler
// answers a live attachment from its idempotent-retry branch, which never re-runs placement: the
// tower named in the reply is the tower this Station was placed on the first time, and nothing
// in the system rewrites that for a live Station. So what a re-attach re-reads is the TOWER'S
// LINK - the endpoint, the certificate pin, the identity fingerprint - and what it recovers is
// exactly the failures that change one of those:
//
//   - a tower turning TLS on, or off
//   - a certificate rotation, which is the same bill on every renewal
//   - a tower that moved: a new address, a new port, a rescheduled container, a renewed lease
//   - anything else Core would answer differently about the SAME tower
//
// A tower that stops EXISTING - lease lost, revoked, switched off - is a different case, and
// asking Core again does not solve it. Core refuses (there is no relay plane for a tower with no
// link session), the node backs off and asks again, and the operator is told. That is bounded
// and visible instead of silent and permanent, which is worth having, and it is not a recovery.
// Moving a live Station onto another tower is section 6 of docs/relay-selection-design.md; it
// needs a settle-time fence that does not exist yet, because an in-flight attempt settles
// against the origin the attachment names. TestATowerThatStopsExistingIsNotRecoveredByReattaching
// pins the limitation so that nobody has to re-derive it from a hopeful test name.

// hubPollTimeout is the deadline on every hub call a tenancy makes, and it is declared HERE,
// beside the streak constants, rather than inline at the http.Client it configures - because it
// is not only a timeout, it is the dominant term in how long a single failure takes to arrive.
// It has to be longer than the tower's own long-poll TTL or an ordinary empty poll would be cut
// short and reported as a failure; everything below is derived from it.
const hubPollTimeout = 60 * time.Second

// hubFailureQuiet is how long the workers must go without a single complaint before a failure
// streak is considered over. A healthy serve loop is SILENT - an empty long poll is not an error
// and reports nothing - so a stretch of quiet is strong evidence, and it is what stops one bad
// poll an hour accumulating into a "standing" failure on a node that has served all day.
//
// IT IS DERIVED, AND THE DERIVATION IS THE FIX FOR A DEFECT THAT MADE THIS WHOLE MECHANISM
// UNREACHABLE FOR THE COMMONEST OUTAGE THERE IS.
//
// This was a flat sixty seconds, chosen as "a minute of quiet", which sounds like a judgement
// about evidence and is in fact a race against a number in another package. The workers report
// one error per FAILURE, and a failure costs hubPollTimeout (waiting for an answer that is not
// coming) plus towerhub.PollBackoff (the wait before trying again): sixty-two seconds, against a
// quiet window of sixty. Every error therefore arrived AFTER the window it was supposed to
// extend, restarted the streak instead of continuing it, and the standing window below was never
// reached - not once, not ever, on a node polling for hours.
//
// And it was not an exotic failure that produced it. It was a hub that accepts the connection
// and answers nothing: powered off with the socket still listening, an IP reassigned under a
// running listener, a firewall or NAT rule that black-holes rather than refuses. That is
// precisely "a tower going away", the case this file exists for. The refusing variant - a closed
// port, an RST in milliseconds - recovered in ninety seconds exactly as designed, which is why
// the tests all passed: they were written against the failure that answers fast.
//
// So the number is no longer chosen. The quiet window is the slowest single failure this loop
// can produce, plus a margin, which makes it STRUCTURALLY impossible for one error to outlast
// it however slow the failure gets. Change the timeout, change the backoff, and this moves with
// them; that relationship is the actual invariant and it is now written down in code rather than
// re-derived by whoever reads it next. TestASlowFailureStillAccumulatesIntoAStreak asserts it at
// production values.
var hubFailureQuiet = hubPollTimeout + towerhub.PollBackoff + hubQuietMargin

// hubQuietMargin is what separates "the errors stopped" from "the errors are just slow". It is
// the only judgement call left in hubFailureQuiet: a gap longer than one whole failure plus this
// is a relay that answered something, or a worker that had nothing to complain about, and either
// way it is not the same streak.
const hubQuietMargin = 30 * time.Second

// hubStandingWindow is how long a relay must be continuously unusable before this node stops
// believing in it. Long enough that a redeploy, a lease renewal or a bad network minute has had
// its chance, short enough that an operator does not lose an afternoon of relay earnings to a
// certificate they never saw rotate.
//
// IT IS WALL CLOCK, AND HOW MANY FAILURES FIT INSIDE IT IS NOT FIXED - which is the sentence the
// first version of this comment got wrong. It said "three poll cycles, or forty-five two-second
// backoffs", costing the window against towerhub.PollBackoff alone as though the request in
// front of the backoff were free. It is not: a hub that REFUSES fails in milliseconds and
// produces the forty-five errors that sentence imagines, while a hub that ACCEPTS AND HANGS
// costs hubPollTimeout per failure and produces two. Both trip this window, because it is
// measured in seconds rather than in complaints, and the quiet window above is what guarantees
// the second case accumulates at all.
var hubStandingWindow = 90 * time.Second

// reattachBackoffBase and reattachBackoffCap bound the wait before asking Core again.
//
// The FIRST wait is deliberately long rather than immediate, and nothing is lost by it: the node
// is registered, discoverable, probed and earning on the classic path the whole time this is
// happening, which is exactly why the stranding was quiet in the first place. What the wait buys
// is that a tower restarting with TLS on does not turn its whole fleet into a simultaneous
// attach at Core.
var (
	reattachBackoffBase = 30 * time.Second
	reattachBackoffCap  = 15 * time.Minute
)

// firstAttachAttempts is how many times a node asks for its FIRST relay before giving up until
// the next `roger share`.
//
// IT IS BOUNDED WHERE A RE-ATTACH IS NOT, and the asymmetry is the whole of the reasoning. A
// later attach is retried for the life of the process because this node WAS on the fabric - its
// absence is a change, and the population that asks is the population behind one broken tower.
// A first attach that keeps being refused is a different population: "no relay is free" is a
// fleet-wide condition, so retrying it forever would put every node in the fleet at Core's door
// on a schedule, which is the spike this design refused on its first page.
//
// Five, on the jittered exponential backoff below, is between four and eight minutes of asking -
// long enough to cover a Core redeploy or a tower reconnecting, short enough that a fabric with
// genuinely nothing free is not being polled all afternoon by nodes that are already registered,
// discoverable and earning on the classic path.
var firstAttachAttempts = 5

// reattachStreakReset is how long a tenancy must have LASTED for the backoff to start over. A
// relay that STOOD for ten minutes and then broke is a fresh event, not the eleventh attempt at
// an old one; without this a node that recovers, serves for an hour and breaks again would begin
// its next recovery at the fifteen-minute cap - fifteen minutes off the relay plane for an
// outage that has nothing to do with the one before it.
//
// IT IS TENANCY DURATION AND NOT WORK CARRIED, which the first version of this comment claimed.
// The distinction matters because they are not the same evidence and only one of them is
// available here: a node has no say in whether any consumer tuned in, so a relay that held up
// perfectly through a quiet ten minutes would be judged as harshly as one that was broken the
// whole time. Duration is also the stronger signal for the question actually being asked. A
// tenancy ends only when the relay has been continuously unusable for hubStandingWindow, so ten
// minutes of tenancy IS ten minutes of a hub answering its polls - an empty long poll is a
// successful poll - whether or not there was work to hand out.
var reattachStreakReset = 10 * time.Minute

// streakAfterTenancy folds one finished tenancy into the consecutive-failure count the backoff
// is computed from: a tenancy that lasted starts the count over, a short one carries it on.
//
// IT IS A FUNCTION BECAUSE IT HAD NO COVERAGE AS A LINE. Every test in this package shortens the
// re-attach timings through fastReattach, which sets reattachStreakReset to an hour so that no
// test's tenancy ever reaches it - deliberately, because none of them are about the backoff's
// memory. The consequence was that the reset never executed under test at all: a decision on the
// operator-visible recovery path, reachable in production on any node whose relay breaks twice in
// a day, with nothing asserting it in either direction. Pulling it out of the loop is what makes
// it addressable without standing up a ten-minute tenancy.
func streakAfterTenancy(consecutive int, lasted time.Duration) int {
	if lasted >= reattachStreakReset {
		return 0
	}
	return consecutive
}

// ErrRelayReattaching is what the operator is told when this node gives up on its relay and goes
// back to Core for a current one.
//
// It is a notice rather than a silence because the two halves of the sentence are both news. The
// first is that something is wrong with a plane they never opted into and cannot see. The second
// is that the node is handling it - which matters because the previous version of this software
// told them, in the pin-mismatch case, to restart their share by hand, and an operator who
// learns to do that will keep doing it long after it stopped being necessary.
var ErrRelayReattaching = errors.New(
	"this node's relay has stopped carrying work in a way that will not come right by retrying, so " +
		"the node is asking Roger Core for its current relay instead of polling this one forever. " +
		"Your ordinary share is unaffected: it has been registered, discoverable and earning " +
		"throughout, because the relay fabric is an additional plane rather than a replacement for it")

// ErrRelayReattachFailed marks a RE-attachment that Core would not answer. It is separated from
// the first attach of a process - which is best-effort and silent by design, because "no relay is
// free right now" is an ordinary answer to a question nobody asked - because this one is not
// speculative: this node WAS on the fabric a moment ago, and is now off it.
var ErrRelayReattachFailed = errors.New("this node could not get back onto the relay fabric")

// errHubStanding is the internal signal from one tenancy to the loop that supervises it: this
// relay is finished, re-attach. It never reaches an operator; ErrRelayReattaching does.
var errHubStanding = errors.New("this relay has stopped being usable")

// staleAdvertisement reports whether a hub error means THE THING CORE TOLD THIS NODE IS NO LONGER
// TRUE - the endpoint, or the certificate that answers at it - as opposed to something neither
// Core nor this node can do anything about.
//
// It is written as an EXCLUSION LIST on purpose. The default for an unrecognised failure is "ask
// Core again", because the opposite default is the one that shipped, and the one that shipped
// stranded nodes in silence. Three things are excluded, each for its own reason:
//
//   - A REFUSED IDENTITY (401). The hub is there, it is answering, and it has decided this node is
//     nobody. Core cannot change that by repeating itself: attach is idempotent for a live
//     attachment, so a re-attach hands back the same tower, the same endpoint and the same keys,
//     and the next poll is refused exactly as this one was. The STALE-EPOCH flavour of a 401 -
//     the ordinary one, produced by every hub redeploy - never reaches here at all, because
//     towerhub's signedDo learns the hub's proved epoch and re-sends once; what reaches here is a
//     hub that will not have this node, and ErrHubRefusedThisNode already hands the operator the
//     sentence they can act on.
//
//     ON AN UNPINNED LINK THIS EXCLUSION IS ALSO A SUPPRESSION SWITCH FOR ANYONE ON THE PATH, and
//     that has to be said rather than left for the next reader to find. ErrHubChannelPlaintext
//     states the premise outright: on a link with no certificate pin, anyone between this node
//     and its relay can feed it a 204 or a 401 the relay never sent. A 401 is excluded here and a
//     204 is not an error at all, so an injector holds this node's failure streak at zero
//     indefinitely - it never reaches hubStandingWindow, never asks Core again, and never learns
//     that its relay moved or that its certificate changed. The reasoning above is still right
//     for an HONEST hub, which is the only party a pinned link lets answer; what an unpinned link
//     adds is that "the hub has decided this node is nobody" is a sentence this node cannot
//     attribute to the hub. The suppression is not new - before re-attachment there was nothing
//     to suppress - and the answer is not a different classifier, because a node that re-attached
//     on 401s would hand every mismatched pair in the fleet a permanent load at Core. The answer
//     is the pin. See docs/relay-selection-design.md section 5.0 item 10.
//
//   - TWO HUB PROCESSES BEHIND ONE ENDPOINT. The only state that DETECTS this is the client's
//     retired-epoch memory, and a re-attach builds a fresh client with an empty one - so the node
//     would flap between the two processes, detect it again, re-attach again, and turn a hard stop
//     that names an unsupported deployment into a loop that hides it. The address is not stale
//     here; what is behind it is wrong, and saying so is the whole point.
//
//   - A COMPLETION THE HUB TOOK BUT DID NOT COURIER, or a result that could not be handed back.
//     Both cost the operator real money and both are already loud - but both PROVE the relay is
//     up and handing this node work, which is the opposite of the evidence this function is for.
//
// Everything else has one property in common: a dial that is refused, a TLS handshake against a
// listener that was plaintext when this node attached, a "malformed HTTP response" from something
// that is no longer a hub, a 404 or a 410 from a route that has moved, a hub that has been
// answering 503 for minutes. None of them can be fixed by this node retrying, and Core is the
// only party that can hand out a different endpoint or a different pin.
func staleAdvertisement(err error) bool {
	switch {
	case err == nil, errors.Is(err, context.Canceled):
		return false
	case hubRefusedIdentity(err):
		return false
	case errors.Is(err, towerhub.ErrHubMultipleProcesses):
		return false
	case costlyRelayError(err):
		return false
	}
	return true
}

// hubFailureStreak decides when a relay has STOPPED BEING A RELAY, as opposed to having a bad
// minute. One error cannot draw that line and a clock must not: see the block comment above.
//
// It is written for concurrent callers because every serve worker reports into it, and on a
// broken hub all of them report at once.
type hubFailureStreak struct {
	mu    sync.Mutex
	first time.Time
	last  time.Time
}

// observe folds one worker error into the streak and reports whether this relay should now be
// treated as finished.
func (h *hubFailureStreak) observe(err error, now time.Time) bool {
	if !staleAdvertisement(err) {
		// Not evidence about the address - but it may still be evidence AGAINST a streak. A
		// completion the hub answered, or a job it handed out and then could not take back,
		// proves the relay is up; letting either sit in the middle of a streak and keep it alive
		// would have a node abandon a working relay because that relay was losing its receipts.
		// That is a different (and worse) problem, and re-attaching to the same tower does not
		// fix it.
		if costlyRelayError(err) {
			h.mu.Lock()
			h.first, h.last = time.Time{}, time.Time{}
			h.mu.Unlock()
		}
		return false
	}
	// A CERTIFICATE THAT IS NOT THE ONE CORE NAMED SKIPS THE WINDOW, and it is the only failure
	// that does. Every other symptom might be ninety seconds of bad luck; this one cannot be, by
	// construction. The pin is read at attach and held for the life of the tenancy, so no number
	// of retries can produce a different outcome - the two causes are an on-path attacker and a
	// relay that replaced its certificate without Core learning the new one, and both are
	// resolved (or not) by asking Core rather than by waiting. Waiting would buy forty-five more
	// doomed handshakes and nothing else.
	//
	// It does NOT skip the backoff, which is what keeps this safe against the attacker half of
	// that pair: a party who can hold the handshake down still cannot make this node attach
	// faster than the backoff allows.
	if errors.Is(err, towerhub.ErrHubCertificateUnpinned) {
		return true
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.first.IsZero() || now.Sub(h.last) > hubFailureQuiet {
		h.first = now
	}
	h.last = now
	return now.Sub(h.first) >= hubStandingWindow
}

// reattachDelay is how long to wait before asking Core again, given how many times in a row this
// node has had to.
//
// EXPONENTIAL AND JITTERED, because the population this runs on is correlated. The event that
// strands one node - a tower restarting with TLS on, a lease expiring, a certificate rotation -
// strands every node on that tower in the same instant, so an un-jittered backoff would have all
// of them attach in the same second, wait the same amount, and attach in the same second again.
// The spread is a full factor of two rather than a token few percent, because half a spread in
// front of one door is still a queue.
func reattachDelay(consecutive int) time.Duration {
	if consecutive < 0 {
		consecutive = 0
	}
	d := reattachBackoffBase
	for i := 0; i < consecutive; i++ {
		if d >= reattachBackoffCap {
			break
		}
		d *= 2
	}
	if d > reattachBackoffCap {
		d = reattachBackoffCap
	}
	half := d / 2
	if half <= 0 {
		return d
	}
	return half + time.Duration(randv2.Int64N(int64(half))+1)
}

// waitFor sleeps unless ctx ends first. false means the share is shutting down.
func waitFor(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// AttachTower self-attaches this node as a servable station: keys from the persistent station
// identity under dir, the offer from cfg (model/modality/prices), the request signed with the
// node's account-bound key. Idempotent on retry with the same identity.
func AttachTower(cfg Config, priv ed25519.PrivateKey, dir string) (*station.Station, TowerAttachment, error) {
	// A PRIVATE BAND IS NEVER ATTACHED. Structural, at the network act, so the guarantee does not
	// depend on which branch of a caller happens to reach here. See ErrPrivateShareNeverRelays.
	if cfg.Private {
		return nil, TowerAttachment{}, ErrPrivateShareNeverRelays
	}
	// KEY-TRUST TRANSPORT (audit M2): attach ships this node's keys up and the tower and
	// endpoint it is placed on back, and the grant key is pinned over the same base - plaintext
	// http to a non-loopback broker is refused. (It used to be described as bringing a hub
	// bearer token back. Core still sends the field for a node too old to sign; this node
	// ignores it and never transmits it - see towerhub's nodeauth.go.)
	if err := protocol.TrustedBase(cfg.Broker); err != nil {
		return nil, TowerAttachment{}, err
	}
	// InitOrOpen, NOT Init. The station identity is PERSISTENT and this call is not: a host
	// mints its keys the first time it ever attaches and must present the SAME ones on every
	// later run, because Core recorded them on the attachment and verifies every receipt
	// against them. Init alone refuses a directory that already holds a Station - correctly,
	// since re-minting would strand that attachment - so calling it here meant the first
	// `roger share` on a machine reached the relay fabric and every subsequent one failed at
	// its first line. Silently, too: the caller treats the whole join as best-effort and
	// prints nothing, which is right for "no relay is free" and quite wrong for "this host
	// can never join again". A genuinely broken directory still errors out rather than
	// minting a second identity beside the one attachments name.
	st, err := station.InitOrOpen(filepath.Join(dir, "tower-station"))
	if err != nil {
		return nil, TowerAttachment{}, fmt.Errorf("station identity: %w", err)
	}
	modality := cfg.Modality
	if modality == "" {
		modality = "chat"
	}
	body, err := json.Marshal(map[string]any{
		// THE JOIN. This is the same node id `roger share` registers, heartbeats and is
		// probed under. Sending it is what lets Core rank this station by measured health
		// instead of guessing: reliability, TTFT and TPS are all recorded against the broker
		// node id, and a station row is keyed by station id, so without this the two halves
		// of one machine have no name in common. Core does not take our word for it - it
		// requires a live registration under this id signed by the same key signing here.
		"node_id":          cfg.NodeID,
		"station_id":       st.StationID,
		"assertion_key":    hex.EncodeToString(st.AssertionPub()),
		"session_key":      hex.EncodeToString(st.SessionPub()),
		"model":            cfg.Model,
		"modality":         modality,
		"price_in_micros":  microsPerDollarPer1M(cfg.PriceIn),
		"price_out_micros": microsPerDollarPer1M(cfg.PriceOut),
	})
	if err != nil {
		return nil, TowerAttachment{}, err
	}
	const path = "/tower/edge/attach"
	req, err := http.NewRequest(http.MethodPost, cfg.Broker+path, bytes.NewReader(body))
	if err != nil {
		return nil, TowerAttachment{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	pub, ts, sig := protocol.SignRequest(priv, http.MethodPost, path, body)
	req.Header.Set(protocol.HeaderPubkey, pub)
	req.Header.Set(protocol.HeaderTS, fmt.Sprintf("%d", ts))
	req.Header.Set(protocol.HeaderSig, sig)
	// AND THE ASSERTION KEY CO-SIGNS, which is a SECOND signature by a DIFFERENT key and not a
	// second copy of the first. The signature above is the account's: it proves who is asking.
	// It has never proved anything at all about the two keys in the body, so Core used to bind
	// whatever public key a signed-in caller named - and the assertion public key is in the
	// clear in a header of every hub poll on an unpinned link, twenty-five seconds apart, for
	// the life of the process. This is the Station saying "these are mine", over this exact
	// request: see protocol.AttachProof.
	//
	// It is minted AFTER SignRequest and from its return values on purpose. The proof names the
	// account key and the timestamp that signature used, so the two are bound to one request and
	// the proof is fresh exactly as long as the request is. Signing it first would mean guessing
	// a timestamp SignRequest had not chosen yet.
	req.Header.Set(protocol.HeaderAttachProof, st.SignAttachProof(link.PublicNetwork, pub, ts, body))
	resp, err := (&http.Client{Timeout: 30 * time.Second, CheckRedirect: protocol.NoDowngradeRedirect}).Do(req)
	if err != nil {
		return nil, TowerAttachment{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, TowerAttachment{}, fmt.Errorf("attach refused (%d): %s", resp.StatusCode, raw)
	}
	var at TowerAttachment
	if err := json.Unmarshal(raw, &at); err != nil {
		return nil, TowerAttachment{}, fmt.Errorf("unreadable attach response: %w", err)
	}
	// AN ENDPOINT IS ALL THIS NEEDS NOW. It used to also demand a hub token, which was right
	// when the token was how the node authenticated and is wrong now that it signs: refusing to
	// serve because Core did not send a credential we no longer use would strand a node over an
	// unused field.
	if at.Endpoint == "" {
		return nil, TowerAttachment{}, errors.New("attach answered without an endpoint")
	}
	// AND A FINGERPRINT FOR THE RELAY, WHICH IS NOT OPTIONAL. Without it this node cannot tell
	// the hub's own epoch from one an on-path attacker named, and the epoch is a value it signs
	// over - so "carry on without it" means emitting signatures over an attacker's choosing.
	// Refusing here is the same posture the node already takes on the credential itself (it
	// never sends a bearer, whatever the hub answers): a downgrade an attacker could provoke is
	// not a security property. Signed hub polls have not shipped in a tagged release, so
	// nothing in the field is stranded by this - but a Core older than the fingerprint is, and
	// the deployment order was already written down: Core, then Towers, then nodes.
	if at.TowerKeyHash == "" {
		return nil, TowerAttachment{}, errors.New(
			"attach answered without the relay's identity fingerprint (tower_key_hash): this node " +
				"cannot verify which hub process it is signing for without it, and signing for an " +
				"unverified one hands an on-path attacker a signature it chose. Roger Core must be " +
				"updated before the towers and nodes that talk to it")
	}
	return st, at, nil
}

// fetchCoreKeys pins Roger Core's grant-signing key AND its envelope key, from Core itself.
// The envelope key is what audit transcripts are sealed to, so the tower relays them exactly
// as blind as the jobs.
func fetchCoreKeys(broker string) (grantKey, envKey []byte, err error) {
	if err := protocol.TrustedBase(broker); err != nil {
		return nil, nil, err
	}
	resp, err := (&http.Client{Timeout: 20 * time.Second, CheckRedirect: protocol.NoDowngradeRedirect}).Get(broker + "/tower/dispatch/key")
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("grant key fetch: %d: %s", resp.StatusCode, raw)
	}
	var out struct {
		DispatchKey string `json:"dispatch_key"`
		EnvelopeKey string `json:"envelope_key"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, nil, err
	}
	grantKey, err = hex.DecodeString(out.DispatchKey)
	if err != nil || len(grantKey) != ed25519.PublicKeySize {
		return nil, nil, errors.New("the grant key is not a hex ed25519 public key")
	}
	envKey, err = hex.DecodeString(out.EnvelopeKey)
	if err != nil || len(envKey) != 32 {
		return nil, nil, errors.New("the envelope key is not a hex X25519 public key")
	}
	return grantKey, envKey, nil
}

// sealedExec adapts the station's sealed serve to the towerhub Executor seam.
type sealedExec struct{ e station.EdgeExecutor }

func (s sealedExec) Serve(ctx context.Context, grant, envelope []byte) ([]byte, []byte, string) {
	return s.e.ServeSealed(ctx, grant, envelope)
}

// transcriptSource adapts the station's transcript lookup to the audit-answer seam.
type transcriptSource struct{ e station.EdgeExecutor }

// EvictedYoung forwards the station's count of transcripts dropped inside their audit
// window, so the serve loop can say so instead of the operator discovering it as audit
// failures at Core.
func (t transcriptSource) EvictedYoung() int {
	if t.e.Transcripts == nil {
		return 0
	}
	return t.e.Transcripts.EvictedYoung()
}

func (t transcriptSource) SignedTranscript(attemptID string) (signed, request, response []byte, ok bool, err error) {
	tr, found, terr := t.e.Transcript(attemptID)
	if terr != nil || !found {
		return nil, nil, nil, false, terr
	}
	return tr.Signed, tr.Request, tr.Response, true, nil
}

// ServeTower runs the tower-serving fabric until ctx is done, RE-ATTACHING whenever the relay it
// was placed on stops being one.
//
// # WHY THIS IS A LOOP AND NOT A CALL
//
// It used to be a call: attach once, build a client from the endpoint and the certificate pin
// that answer named, spawn the workers, and let them retry a failing hub every two seconds for
// the life of the process. Every value that decides WHERE and HOW this node polls was read once
// and then frozen - so every permanent change on the relay's side was permanent for this node
// too. See the block comment above hubFailureQuiet for what that cost and why it was quiet.
//
// # WHAT IS PER-TENANCY AND WHAT SURVIVES ONE, WHICH IS THE WHOLE DESIGN
//
// A "tenancy" is one attachment: one tower, one endpoint, one pin, one hub process.
//
// PER-TENANCY, and rebuilt from scratch every time, because every one of them is a fact about a
// particular hub rather than about this node: the towerhub.Client (its cached process EPOCH, and
// its RETIRED-epoch memory - carrying either across a re-attach would have a fresh client accuse
// a perfectly ordinary hub of being two hub processes), the pinned TLS transport and its
// sockets, the tower id that rides in every signed target, the identity fingerprint the hub's
// epoch proof is checked against, the serve workers and the audit loop.
//
// SURVIVES EVERY TENANCY, because it belongs to this MACHINE:
//
//   - The station identity. It is persistent on disk and AttachTower opens rather than mints it
//     (InitOrOpen), so a re-attach presents the same keys Core recorded and is answered
//     idempotently with the same registration. This is verified by
//     TestAttachTowerReusesThePersistentStationIdentity, and it is the property that makes
//     re-attachment safe to do at all rather than a way to mint a second identity per outage.
//   - The executor, and with it the TRANSCRIPTS and the attempt cache. The transcripts are the
//     evidence behind receipts this node has already signed, and Core's audit will ask for them
//     by attempt id for as long as the settlement window is open. Rebuilding the executor on
//     every re-attach would answer those audits with "not retained" - and withholding is itself
//     a finding against the operator, so a relay hiccup would turn into a reputation event. The
//     attempt cache is the one-serve-per-attempt guard, and forgetting it across a re-attach
//     would let a replayed attempt be served twice.
//   - Core's grant and envelope keys. They are Core's, fetched from Core, and the relay is
//     precisely the party they exist to distrust; re-fetching them per tenancy would cost a round
//     trip to establish something that did not change.
//
// # THE FIRST ATTACH IS STILL THE FIRST ATTACH
//
// An error out of the FIRST attach is returned exactly as it always was, because the caller's
// contract is built on it: cmd/rogerai's joinRelayFabric treats this whole join as best-effort
// and silent, and picks out ErrCoreKeysUnpinned - the one failure meaning this node cannot tell
// a real grant from one its relay invented - from the returned error. "No relay is free right
// now" is an ordinary answer to a question the operator did not ask, and it stays silent. A
// later attach is a different event with a different meaning: this node WAS on the fabric, so
// its absence is a change rather than a non-event, and it is retried and said out loud.
func ServeTower(ctx context.Context, cfg Config, priv ed25519.PrivateKey, dir string, out io.Writer, notice Notice) error {
	var (
		exec                station.EdgeExecutor
		coreKey, coreEnvKey []byte
		consecutive         int
		firstTries          int
		gen                 int
	)
	for {
		if ctx.Err() != nil {
			return nil
		}
		st, at, err := AttachTower(cfg, priv, dir)
		if err != nil {
			if gen == 0 {
				// THE FIRST ATTACH IS RETRIED NOW, A FEW TIMES, AND THAT IS A CHANGE OF MIND.
				//
				// It used to return on the first failure, and the reasoning was that "no relay is
				// free right now" is an ordinary answer to a question the operator did not ask.
				// True, and it left generation zero holding exactly the defect this whole loop
				// exists to remove: a `roger share` that starts while Core is mid-redeploy, or
				// while its tower's link is reconnecting, gets no relay plane for the life of the
				// process and the only remedy is an operator restarting a share that is otherwise
				// working perfectly. A redeploy is minutes; a share runs for days.
				//
				// BOUNDED RATHER THAN ENDLESS, because the two cases are genuinely different.
				// A LATER attach is retried forever: that node WAS on the fabric, so its absence
				// is a change, and the population asking is the population behind one broken
				// tower. A FIRST attach that keeps being refused may be every node in the fleet
				// at once - "the fabric has nothing free" is a fleet-wide condition, not a
				// per-tower one - and turning that into a permanent poll at Core is the schedule
				// this design refused on the first page. firstAttachAttempts covers a redeploy
				// and stops well short of a standing load.
				//
				// It costs the operator nothing to wait: `roger share` calls this on a goroutine
				// of its own, after the node is registered, discoverable, probed and earning on
				// the classic path. And the contract the caller depends on is unchanged - the
				// same error is still RETURNED, just after this node has given the condition a
				// chance to end, and ErrPrivateShareNeverRelays is structural rather than
				// temporary so it never waits at all.
				firstTries++
				if errors.Is(err, ErrPrivateShareNeverRelays) || firstTries >= firstAttachAttempts {
					return err
				}
				if !waitFor(ctx, reattachDelay(firstTries-1)) {
					return nil
				}
				continue
			}
			// This node was serving through a relay a moment ago and now cannot get back on. That
			// is worth saying once, and worth asking again about: a tower whose lease is being
			// renewed, an instance of Core that is redeploying, or a fabric with nothing free
			// right now are all conditions that end.
			notice.notify(fmt.Errorf("%w: %w", ErrRelayReattachFailed, err))
			if !waitFor(ctx, reattachDelay(consecutive)) {
				return nil
			}
			consecutive++
			continue
		}
		if gen == 0 {
			// The station directory had something wrong with it that Open could repair rather
			// than refuse - a permissive mode, most likely. Repairing it silently would leave the
			// operator believing a key that has been readable was never readable. Said on the
			// first attach only: it is a property of the directory, and a re-attach re-opens the
			// same one, so repeating it per tenancy would be describing one fact many times.
			for _, w := range st.Warnings {
				notice.notify(errors.New(w))
			}
			coreKey, coreEnvKey, err = fetchCoreKeys(cfg.Broker)
			if err != nil {
				return fmt.Errorf("%w: %w", ErrCoreKeysUnpinned, err)
			}
			exec = station.EdgeExecutor{
				Station: st, CoreKey: coreKey, Network: link.PublicNetwork,
				Upstream: station.HTTPUpstream{URL: cfg.Upstream},
				Outbox:   station.NewOutbox(256),
				Seen:     station.NewAttemptCache(),
				// Transcripts make this node AUDITABLE: Core's sampled/adaptive audit asks for the
				// exact bytes behind a settled receipt, and a node that retains nothing can only
				// answer "not retained". Keep-all over the recent window (the store is bounded).
				Transcripts: station.NewTranscripts(0, 0),
			}
		}
		started := time.Now()
		terr := serveTowerTenancy(ctx, cfg, at, exec, coreEnvKey, out, notice)
		switch {
		case ctx.Err() != nil:
			return nil
		case errors.Is(terr, errHubStanding):
			// terr ITSELF, not errors.Unwrap(terr). A `fmt.Errorf("%w: %w", ...)` has an
			// `Unwrap() []error`, not an `Unwrap() error`, so errors.Unwrap returns NIL on it and
			// the cause would have been formatted as "%!w(<nil>)" - an operator handed a rendering
			// artefact where the reason should be. errors.Is still walks the tree either way,
			// which is what made it silent.
			notice.notify(fmt.Errorf("%w (relay %s at %s): %w", ErrRelayReattaching,
				at.TowerID, at.Endpoint, terr))
		case gen == 0:
			// The first tenancy could not be started at all - an endpoint Core advertised that
			// cannot be reached as advertised, most likely a malformed pin. It is asked about
			// again, on the same bounded budget as a refused first attach and for the same
			// reason: the plane Core publishes is read from the tower's LIVE link session, so an
			// endpoint that is malformed or unreachable this second is a thing a tower
			// reconnecting can fix without anybody restarting a share. When the budget is spent
			// the error is RETURNED exactly as it always was, so the caller's best-effort
			// handling is unchanged - it just happens a few minutes later, on a goroutine nobody
			// is waiting on. Looping here re-enters the gen == 0 block above, so Core's keys are
			// re-fetched and the executor rebuilt: free, because a tenancy that never started
			// served nothing, and correct, because a Core that could not be reached a minute ago
			// is exactly the condition being waited out.
			firstTries++
			if firstTries >= firstAttachAttempts {
				return terr
			}
			if !waitFor(ctx, reattachDelay(firstTries-1)) {
				return nil
			}
			continue
		case terr == nil:
			// Every worker returned without ctx being done. ServeLoop only returns on ctx, so this
			// is unreachable today; if it ever becomes reachable it is a stopped plane, not a
			// failure, and leaving the fabric silently is the wrong answer to it.
			return nil
		default:
			// A LATER tenancy could not be started. Same event as a failed re-attach: say it, and
			// ask Core again rather than leaving the plane for the life of the process.
			notice.notify(fmt.Errorf("%w: %w", ErrRelayReattachFailed, terr))
		}
		// A tenancy that STOOD for a while and then broke is a fresh event, not the next attempt
		// at an old one, so it starts the backoff over.
		consecutive = streakAfterTenancy(consecutive, time.Since(started))
		if !waitFor(ctx, reattachDelay(consecutive)) {
			return nil
		}
		consecutive++
		gen++
	}
}

// serveTowerTenancy serves ONE attachment: build a client for the endpoint and pin this
// attachment named, run the workers and the audit loop against it, and return when the relay is
// finished or the share is shutting down.
//
// It returns nil when ctx ended, an error wrapping errHubStanding when the relay stopped being
// usable, and any other error when the tenancy could not be started at all.
func serveTowerTenancy(ctx context.Context, cfg Config, at TowerAttachment,
	exec station.EdgeExecutor, coreEnvKey []byte, out io.Writer, notice Notice) error {
	// THE HUB CLIENT IS BUILT ONCE PER TENANCY, HERE, AND ITS TLS SETTINGS ARE NOT NEGOTIABLE
	// LATER. hubPollTimeout is longer than the tower's poll TTL so a long poll is not cut short -
	// it is declared with the streak constants because it is also the dominant term in how long a
	// single failure takes to surface, and hubFailureQuiet is derived from it. When Core published
	// a pin, the transport accepts exactly the certificate the pin names.
	//
	// TLS IS NOT REQUIRED, and that is a decision rather than an omission. Requiring it would take
	// every relay whose operator has not yet turned it on off the air, and with it every node
	// attached to one; the capability has to work before its deadline can be set. See
	// docs/relay-selection-design.md section 5.7 for the recommendation and what making it
	// mandatory would cost - a paragraph this function is named in, because re-attachment is one
	// of the two changes that turn "every node must be restarted by hand" into a migration
	// operators do not have to attend.
	base, hubHTTP, plaintext, err := hubBaseURL(at.Endpoint, at.EndpointTLSSPKI,
		&http.Client{Timeout: hubPollTimeout})
	if err != nil {
		return fmt.Errorf("this relay's data plane cannot be reached as advertised: %w", err)
	}
	// THE TENANCY'S SOCKETS GO WITH THE TENANCY. A pinned link gets a transport of its own (see
	// towerhub.Reach), and its idle connections are long-poll connections to a hub this node is
	// about to stop believing in; leaving them pooled would keep an outage's worth of sockets open
	// against a relay that has been replaced. An UNPINNED link is left alone deliberately: it has
	// no transport of its own, so it is sharing http.DefaultTransport with the ordinary share's
	// broker poll, and closing that would reach into a plane this one has no business touching.
	defer func() {
		if tr, ok := hubHTTP.Transport.(*http.Transport); ok && tr != nil {
			tr.CloseIdleConnections()
		}
	}()
	if plaintext {
		// ONCE PER TENANCY, and on the channel that is not discarded. This is a standing property
		// of the link rather than an event, so it is said at the moment the link is established
		// and not repeated per poll. The notice sink says a repeated message once, so a node that
		// re-attaches to the same plaintext relay does not say it twice - and one that lands on a
		// DIFFERENT plaintext relay does, because the sentence names the relay.
		notice.notify(fmt.Errorf("%w (relay %s at %s)", ErrHubChannelPlaintext, at.TowerID, base))
	}
	channel := "unencrypted"
	if !plaintext {
		// Named rather than assumed: an operator reading this line is entitled to know which of
		// the two channels they got, and "encrypted" alone would be the claim an unverified TLS
		// client could also make.
		channel = "encrypted, certificate verified against the fingerprint Roger Core published"
	}
	fmt.Fprintf(out, "tower: attached as %s via %s (%s, %s) - serving %s at your listed price\n",
		at.StationID, at.TowerID, at.Endpoint, channel, cfg.Model)

	client := &towerhub.Client{
		BaseURL: base,
		// THE TOWER IS PART OF WHAT IS SIGNED. Core named this tower in the attach response,
		// and the hub refuses a signature that names any other, so a request captured off this
		// plaintext link is good at this hub and nowhere else - not at a second instance behind
		// the same endpoint, and not at this one after a restart inside the skew window.
		TowerID: at.TowerID,
		// WHAT MAKES THE EPOCH THE HUB'S VALUE AND NOT THE ATTACKER'S. Core admitted this relay
		// under an identity key and handed over its fingerprint at attach; the hub signs its
		// process epoch with the private half, and this client refuses to adopt an epoch it cannot
		// check against this hash. Without it, a forged 401 on the plaintext link would make this
		// node sign over any epoch the party in front of it liked.
		TowerKeyHash: at.TowerKeyHash,
		// SIGNED, NOT BEARER. st.SignRequest signs each hub call with the assertion key this
		// Station's receipts are already verified against, so the plaintext link carries no
		// reusable credential for anyone on the path to lift. See towerhub's nodeauth.go.
		Sign: exec.Station.SignRequest,
		// Built above, because the certificate check belongs with the base URL that decided
		// there would be one. A client assembled here from scratch is a client that dials
		// https and verifies nothing.
		HTTP: hubHTTP,
	}

	// THE TENANCY'S OWN CONTEXT. Cancelling it is how a standing failure stops the workers and the
	// audit loop for THIS relay without touching the share's shutdown, which is the caller's.
	tctx, cancel := context.WithCancel(ctx)
	defer cancel()
	streak := &hubFailureStreak{}
	// Buffered and non-blocking: several workers usually notice the same standing failure within
	// milliseconds of each other, and the first one to say so is the one that ends the tenancy.
	standing := make(chan error, 1)
	trip := func(err error) {
		select {
		case standing <- err:
			cancel()
		default:
		}
	}

	// The audit-answer loop rides beside the workers: fetch what Core wants from this
	// Station (relayed by the hub) and answer with signed transcripts.
	// EVERY audit-plane error goes to the notice channel, not to out. An unanswered audit is a
	// finding against this operator at Core - withholding is itself a finding - and a transcript
	// evicted inside its window is evidence destroyed before it was asked for. Neither is
	// transport chatter, even when its immediate cause is. The sink is expected to say a
	// repeated thing once (see cmd/rogerai/relayfabric.go), which is what makes it safe to be
	// generous here rather than trying to classify a hub's HTTP status.
	//
	// IT DOES NOT FEED THE FAILURE STREAK, and that is deliberate. The audit plane failing while
	// the serve plane works is a real and separate problem (an old tower, a Core that cannot be
	// reached through this hub) and re-attaching would not fix it; letting it trip the streak
	// would have a node abandon a relay that is paying it.
	go towerhub.AnswerAudits(tctx, client, at.StationID, transcriptSource{exec}, coreEnvKey, 0, func(err error) {
		notice.notify(fmt.Errorf("relay audit: %w", err))
	})
	// THESE ARE ADDITIONAL TO THE CLASSIC POLL WORKERS, not a share of them. agent.Start
	// already spawns cfg.Parallel workers against the same local model, and since every public
	// share now offers itself to the relay fabric as well, `--parallel 4` is a ceiling of eight
	// concurrent generations rather than four. There is no shared limiter between the two
	// planes and this is not the place to invent one: a hub worker costs nothing while no
	// consumer is tuned in, so halving each plane would cut the throughput of the path most
	// requests actually take in order to bound a case few nodes reach. The flag's help says
	// "per serving plane" for exactly this reason.
	workers := cfg.Parallel
	if workers <= 0 {
		workers = 2
	}
	done := make(chan error, workers)
	for i := 0; i < workers; i++ {
		go func() {
			done <- towerhub.ServeLoop(tctx, client, at.StationID, sealedExec{exec}, func(err error) {
				// EVERY worker error is weighed for whether this relay is finished, BEFORE it is
				// classified for the operator - because the two questions have different answers.
				// A pin mismatch is both loud and terminal; a dial that is refused is neither loud
				// nor terminal on its own and terminal after ninety seconds of it.
				if streak.observe(err, time.Now()) {
					trip(err)
				}
				// Work already done, and nobody will pay for it: the operator hears about it.
				// A poll that could not reach the hub is retried by the loop and stays quiet.
				if costlyRelayError(err) {
					notice.notify(err)
					return
				}
				// A REFUSED IDENTITY IS NOT A BLIP. The loop will retry it every two seconds
				// until the process ends and never get anywhere, and the writer it would
				// otherwise be printed to is discarded, so this is the difference between an
				// operator learning their relay is too old and an operator seeing a station
				// that quietly never earns. The notice sink says a repeated message once.
				//
				// It is deliberately NOT a re-attach trigger - see staleAdvertisement. This is the
				// one standing failure Core cannot answer differently, so telling the operator is
				// the whole of the available remedy.
				if hubRefusedIdentity(err) {
					notice.notify(fmt.Errorf("%w: %w", ErrHubRefusedThisNode, err))
					return
				}
				// THE RELAY SAID SOMETHING IT COULD NOT PROVE, or the endpoint is answered by
				// two hub processes. Both are standing properties of the relay rather than
				// transport chatter, both mean this node is not earning through it, and neither
				// resolves by retrying - so they travel the channel that is not discarded,
				// beside the refused-identity alarm. The sink says a repeated thing once.
				if errors.Is(err, towerhub.ErrHubEpochUnproved) || errors.Is(err, towerhub.ErrHubMultipleProcesses) {
					notice.notify(err)
					return
				}
				// A CERTIFICATE THAT IS NOT THE ONE CORE NAMED, which without this line would be
				// indistinguishable from a hub that is down: a handshake failure arrives here as
				// an ordinary transport error, and the writer it would otherwise print to is
				// discarded. The two causes are an on-path attacker and a relay that changed its
				// certificate without Core learning the new one.
				//
				// THE INSTRUCTION USED TO BE "RESTART THIS SHARE" AND IT IS NOT ANY MORE. That was
				// true when the pin was read once and held for the life of the process; this node
				// now goes back to Core for the relay's current advertisement on its own, so
				// teaching the operator to restart would be teaching them a ritual that has
				// stopped being necessary and will outlive the reason for it.
				if errors.Is(err, towerhub.ErrHubCertificateUnpinned) {
					notice.notify(fmt.Errorf("%w (relay %s at %s): this node holds the "+
						"fingerprint Roger Core published when it attached and will not accept "+
						"another one. It is not waiting for you: it is asking Core for this "+
						"relay's current advertisement, and will pick up a replaced certificate "+
						"on its own", err, at.TowerID, at.Endpoint))
					return
				}
				fmt.Fprintf(out, "tower: %v\n", err)
			})
		}()
	}
	var first error
	for i := 0; i < workers; i++ {
		if werr := <-done; werr != nil && first == nil {
			first = werr
		}
	}
	// The standing failure is read AFTER the workers have drained, so a tenancy that ends because
	// its relay is finished is never mistaken for one that ended because the share is shutting
	// down - both cancel the same context, and only one of them wants a re-attach.
	select {
	case serr := <-standing:
		return fmt.Errorf("%w: %w", errHubStanding, serr)
	default:
	}
	if errors.Is(first, context.Canceled) {
		return nil
	}
	return first
}

// NodeKey exposes this host's persistent node key (the same identity `roger share` registers
// and `roger login` binds to an account) for the tower-serving path, which signs its
// self-attach with it.
func NodeKey() ed25519.PrivateKey { return loadOrCreateKey() }
