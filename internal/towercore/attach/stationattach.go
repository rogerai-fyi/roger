// Package stationattach is how a Station becomes something Roger Core will believe.
//
// It is the foundation the rest of the Tower network stands on, and it was missing.
// towerinv verifies a leaf against "the key Core recorded at attachment" and inv.Policy
// is asked for that key - but nothing recorded one. Without this package no leaf can ever
// verify, so no inventory can admit anything, so dispatch has nothing to dispatch to.
//
// WHAT AN ATTACHMENT IS. An owner-authorized binding of a Station ID to TWO independent
// keys, under exactly one origin:
//
//   - the ASSERTION key (A) signs the Station's offers. This is the key towerinv checks.
//   - the SECURE-SESSION key (K) terminates the end-to-end channel to the Station. A Tower
//     relays that channel and cannot mint it.
//
// They are separate keys on purpose: a Tower that could speak on the session channel must
// still not be able to sign an offer, and a leaked offer key must not hand over live
// traffic. Presenting one key for both purposes is refused rather than tolerated.
//
// THE FOUR PROPERTIES, and what each one stops:
//
//   - ONE AUTHORIZATION, CONSUMED ONCE, IN THE SAME TRANSACTION AS THE ATTACHMENT. Two
//     processes racing one invitation must produce exactly one origin. A read-then-write
//     would let both win and leave two origins for one Station, which is capacity the
//     operator does not have and a second identity nobody authorized.
//
//   - A LOST RESPONSE IS NOT A SECOND ATTACHMENT. A retry presenting the same authorization
//     and the same keys gets the SAME outcome back, because the caller could not tell a lost
//     reply from a refusal and would otherwise be stuck. A retry presenting the same
//     authorization with DIFFERENT keys is refused: that is not a retry, it is reuse.
//
//   - ORIGIN PRESENCE IS CLOSED. Joined requires exactly one admitted Tower; direct requires
//     the Tower field to be absent. Neither "joined with no Tower" nor "direct with a Tower"
//     is a shape this network has a meaning for, so both are refused before anything is
//     consumed rather than normalised into whichever the reader assumed.
//
//   - ORIGIN KIND IS IMMUTABLE IN V1. A Station admitted direct never becomes joined, or the
//     reverse. Its earnings lineage, capacity and held compensation are bound to that
//     identity; migrating the kind under a stable Station ID would silently move all of it.
//     The old identity must reach terminal revoked state and a NEW Station ID be allocated.
//
// NOTHING IS PARTIALLY COMMITTED. Every refusal below happens before the authorization is
// consumed, so a failed attachment leaves no origin, no binding, and nothing for a caller to
// retry around.
//
// Spec: features/tower/station_attachment.feature.
package attach

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Origin kinds. Standalone Towers are deliberately absent: a standalone Station creates no
// RogerAI authority at all, so it never reaches this package.
const (
	OriginDirect = "direct"
	OriginJoined = "joined"
)

// Attachment lifecycle states. A fresh attachment is ALWAYS quarantine: admission proves
// who a Station is, never that it is any good, and eligibility is decided later by
// Core-observed evidence.
const (
	StateQuarantine = "quarantine"
	StateActive     = "active"
	StateRevoked    = "revoked"
	StateDetached   = "detached"
	// StateDormant is "this machine has not been seen for a long time", and it is the state
	// the idle sweep assigns. It is NOT terminal: a Station in it carries no traffic, appears
	// in no Tower's node list and is published in no projection, but the SAME machine, with
	// the same Station ID and the same keys, may attach again and pick up where it left off.
	//
	// # WHY IT HAD TO EXIST
	//
	// DetachIdle used to write StateDetached, which is terminal AND unrecoverable: checkBindings
	// answers "this Station ID has been retired and cannot be reattached", and ReapTerminal does
	// not even free the row for a fresh Station under that id for a month. So one crossing of
	// one horizon - seven days with no stamp - turned a temporary absence into a permanent loss
	// of an operator's Station identity. A fortnight's holiday did it. A fortnight of downtime
	// did it. And because the stamp is written by exactly one thing (publishRoutable joining a
	// node id to a live registration), the liveness mirror being broken for a week on the
	// instance holding a Tower's link retired EVERY self-attached Station behind that Tower,
	// irrecoverably, with no operator action and nothing to appeal to.
	//
	// A single thin dependency in front of an irreversible action is the wrong shape whatever
	// the dependency is. So the sweep's job is now to stop the table growing and stop dead rows
	// being published - which is all it was ever for - and the irreversible half is a SEPARATE,
	// much later pass (RetireDormant) or an owner's explicit Revoke.
	//
	// A dormant Station KEEPS ITS KEYS RESERVED. That is what makes the recovery promise real
	// rather than nominal: an assertion key is public and rides in the clear on every hub poll,
	// so if dormancy freed it, anyone could bind it to a Station of their own and the rightful
	// owner's return would be refused for a key they never gave up.
	StateDormant = "dormant"
)

// ErrRejected is every refusal. The reason is wrapped for operators; callers branch on this
// sentinel alone, because a Station learning WHICH check refused it is a probing oracle.
var ErrRejected = errors.New("the attachment was refused")

// ErrUnavailable is a store that could not answer. Distinct from a refusal on purpose: a
// backend blink must never be reported to an operator as "your keys are wrong".
var ErrUnavailable = errors.New("the attachment service is temporarily unavailable")

func reject(cause error) error { return fmt.Errorf("%w: %w", ErrRejected, cause) }

// Origin is where a Station serves from.
type Origin struct {
	Kind    string `json:"kind"`
	TowerID string `json:"tower_id,omitempty"`
}

// check enforces the closed presence rule. Both halves matter: a joined origin with no
// Tower would be routable through nobody, and a direct origin carrying a Tower ID would
// invite a later reader to treat it as joined.
func (o Origin) check() error {
	switch o.Kind {
	case OriginJoined:
		if strings.TrimSpace(o.TowerID) == "" {
			return errors.New("a joined origin needs exactly one admitted Tower")
		}
		return nil
	case OriginDirect:
		if o.TowerID != "" {
			return errors.New("a direct origin must carry no Tower ID")
		}
		return nil
	default:
		return fmt.Errorf("unknown origin kind %q", o.Kind)
	}
}

// Authorization is the one-use invitation an owner obtained for a specific Station and a
// specific pair of keys. It is spent by Admit, in the same transaction that records the
// attachment.
type Authorization struct {
	ID        string
	Network   string
	StationID string
	Owner     string // owner pubkey
	Origin    Origin
	// AssertionKey and SessionKey are the EXACT keys this invitation is for. Attaching with
	// any other key is not the attachment that was authorized.
	AssertionKey string
	SessionKey   string
	CeilingHash  string
	// SecretHash is sha256 of the one-use invitation secret, hex encoded. The plaintext is
	// shown to the operator ONCE at invite and never stored, so a database read cannot hand
	// somebody an attachment they were not given. An authorization with no verifier is
	// unusable rather than open - see validate.
	SecretHash string
	Role       string
	// HubToken is the bearer token the serving node USED to present to its Tower's data-plane
	// hub (Option C, Topology 2). Minted by Core at SELF-attach (the invite+redeem-in-one
	// path), empty on the classic operator-invite flow. Stored plaintext like the broker's
	// node BridgeToken: the Tower must compare the exact value the node presents.
	//
	// A current node does not present it. It signs each hub request with AssertionKey instead,
	// because the hub link is plaintext by construction and a reusable secret on it is a
	// denial-of-earnings primitive for anyone on the path (internal/towerhub/nodeauth.go). This
	// stays minted for one release so a node built before that change still authenticates, and
	// goes with towerhub.Server.AllowLegacyBearer.
	HubToken string
	// NodeID is the BROKER node id this station is the same machine as - the id under which
	// `roger share` registered, heartbeats, and is probed. It is the join between the two
	// halves of one provider.
	//
	// Without it the edge fabric is blind. Placement on the edge path has nothing to rank
	// candidates by, because reliability, TTFT and TPS are all recorded against the broker
	// node id while an edge row is keyed by station id - two names for one machine, with no
	// way to get from either to the other. Carrying it here is what lets a scorer ask "how
	// good is this station" and get an answer measured rather than assumed.
	//
	// It is CHECKED, not believed: the attach handler requires a live registration under
	// this id whose pubkey is the one that signed the attach. Empty only on the classic
	// operator-invite flow, which has no `roger share` half.
	NodeID string
	// Model/Modality/PriceIn/PriceOut are the self-attached node's OFFER: what it serves and
	// what the consumer pays (micro-USD per 1,000,000 tokens), band-checked by the broker at
	// attach. Empty/zero on the classic flow, whose offers ride the Tower's signed inventory.
	Model     string
	Modality  string
	PriceIn   int64
	PriceOut  int64
	IssuedAt  time.Time
	ExpiresAt time.Time
	// Consumed and ConsumedBy record the spend. ConsumedBy is the Station ID that resulted,
	// which is what makes a lost-response retry answerable.
	Consumed   bool
	ConsumedBy string
}

// Attachment is what Core records, and what inv.Policy later reads.
type Attachment struct {
	StationID    string
	Owner        string
	AssertionKey string
	SessionKey   string
	Origin       Origin
	// Epoch increments only on a fenced rehome. It is what lets an old origin's in-flight
	// work be refused after the move.
	Epoch       int64
	CeilingHash string
	State       string
	AttachedAt  time.Time
	AuthID      string
	// HubToken is the node's pre-signature bearer token for its Tower's data-plane hub (see
	// Authorization.HubToken for why it is on its way out). The Tower reads it alongside
	// AssertionKey, which is what it actually verifies a signed poll against; empty means this
	// attachment predates (or never used) the self-attach path.
	HubToken string
	// NodeID is the broker node id this station is the same machine as - see
	// Authorization.NodeID for why the join exists. Empty on the classic flow.
	NodeID string
	// The self-attached node's offer (see Authorization). Model empty = classic flow.
	Model    string
	Modality string
	PriceIn  int64
	PriceOut int64
	// AuditProvenAt is when this Station first ANSWERED a content audit - proof, by
	// behaviour rather than by claim, that it retains transcripts and will produce them.
	//
	// It exists so a temporary leniency can retire itself per node instead of on a flag day.
	// Hub nodes could not answer audits at all until the transcript plane shipped, so a
	// "cannot produce" from one had to be treated softly or every honest tower running older
	// node binaries would be quarantined for a feature that did not exist. A node that has
	// answered once has demonstrated the capability, and from then on its misses mean what
	// they mean for everybody else. Zero = never answered (yet).
	AuditProvenAt time.Time
}

// SelfAttached reports whether this attachment came from the one-call self-attach path - a
// `roger share` node that SERVES BY POLLING its Tower's data-plane hub - as opposed to a
// classic operator-invited Station the Tower reaches some other way.
//
// It exists because three separate readers were asking that question by testing HubToken != "",
// including the one that decides which Stations Core even tells a Tower about. HubToken is the
// credential signed hub polls replaced, and the instruction written beside it says to delete the
// field one release from now; followed literally, that would have emptied every Tower's node
// list and taken the relay fabric offline. A predicate keyed on the fields that describe WHAT
// THIS ATTACHMENT IS survives the deletion of a credential, which is the whole point of not
// keying on one.
//
// The OR is deliberate, and so is the order. Self-attach requires a node id and a model and
// mints a hub token; the classic flow supplies none of the three. Any one of them is therefore
// proof, and demanding all three would mean a future flow that stops setting one silently
// de-lists a fleet. The two failure directions are nothing like each other: a false negative
// takes a paying node off the network, a false positive registers a Station on a hub it never
// polls, which is inert. HubToken is listed last because it is the one that disappears.
func (a Attachment) SelfAttached() bool {
	return a.NodeID != "" || a.Model != "" || a.HubToken != ""
}

// Live reports whether this attachment may carry public work at all. Quarantine is live-
// but-not-yet-eligible; revoked and detached are terminal for this Station ID; dormant is
// neither - it carries no work and can come back. Nothing that routes, publishes or pays may
// widen to include dormant, which is why it is not in this list.
func (a Attachment) Live() bool {
	return a.State == StateQuarantine || a.State == StateActive
}

// Recoverable reports whether this Station ID can be attached to again by the machine that
// holds it. It is the one state where "not live" does not mean "gone", and it is a named
// predicate rather than an inline comparison because the whole point of the soft/terminal split
// is that the two are asked about separately from now on.
func (a Attachment) Recoverable() bool { return a.State == StateDormant }

// Held reports whether this attachment still RESERVES its assertion and session keys. It is
// broader than Live on purpose: a dormant Station is not serving, and its keys are still its
// own, because an assertion key is public material that rides in the clear on every hub poll
// and freeing it would let anybody take the identity a sleeping operator is entitled to return
// to. Terminal states release their keys, as they always have.
func (a Attachment) Held() bool { return a.Live() || a.Recoverable() }

// Proof is what a Station presents. Every field must match the authorization exactly; this
// type exists so the comparison is explicit rather than a pile of arguments.
type Proof struct {
	AuthID string
	// Secret is the one-use invitation material the operator handed over. It proves the
	// presenter was GIVEN this invitation, which possession of the two keys does not: the
	// operator chose those keys at invite time, so anyone who learned them and the
	// authorization id could otherwise attach in the Station's place.
	Secret       string
	Network      string
	StationID    string
	Owner        string
	Origin       Origin
	AssertionKey string
	SessionKey   string
}

// Store is the durable half. Admit MUST consume the authorization and write the attachment
// atomically - see the package doc for why a read-then-write loses the race.
type Store interface {
	// PutAuthorization records a fresh invitation.
	PutAuthorization(a Authorization) error
	// PutAuthorizationCapped records one ONLY if the owner is under max live invitations,
	// and reports whether it was written.
	//
	// The cap is enforced WHERE THE WRITE HAPPENS. Counting first and inserting after is a
	// check-then-act: concurrent calls all read the same count, all pass, and all insert -
	// overshooting by the caller's concurrency once per TTL window. A cap that only holds
	// when nobody is trying is not a cap. The enrollment-token layer learned this already
	// (admit.PutTokenCapped) and this is the same shape, for the same reason.
	PutAuthorizationCapped(a Authorization, max int) (bool, error)
	// Reap deletes expired UNCONSUMED invitations immediately, and consumed ones once they
	// are past retryHorizon.
	//
	// Consumed rows are what answer a lost-response retry, so they cannot go at once - but
	// "cannot go at once" is not "must be kept forever". Without a horizon an operator
	// looping invite -> redeem grows the table without bound, which is the same vector the
	// per-owner cap closes for UNREDEEMED rows and would otherwise leave open behind it.
	Reap(before time.Time, retryHorizon time.Duration) (int64, error)
	// CountLiveAttachments reports how many live Stations an owner holds, so the attach path
	// can be capped by the write the same way minting is.
	CountLiveAttachments(owner string) (int, error)
	// Authorization reads one back.
	Authorization(id string) (Authorization, bool, error)
	// Admit consumes authID and records at, in ONE transaction. It returns false with no
	// error when the authorization was already consumed - the caller then decides whether
	// this is an idempotent retry or divergent reuse.
	Admit(authID string, at Attachment) (bool, error)
	// ByStation and ByAssertionKey are the uniqueness and lookup reads. There is no
	// BySessionKey and there must not be one: the session key has no uniqueness rule (see
	// checkBindings for why the one that existed was a denial primitive protecting nothing),
	// so a lookup whose only purpose was to serve that rule is how it comes back.
	ByStation(stationID string) (Attachment, bool, error)
	// ByStations is ByStation for a whole placement's worth of Stations, in ONE round trip.
	//
	// IT EXISTS FOR THE CONNECTION POOL, not for the rows. Edge placement re-checks every
	// candidate against this registry before it ranks them, and it did that one ByStation at
	// a time - N sequential queries per authorize, on the pool the wallets, holds and
	// settlement share (internal/store's poolLimits caps maxOpen at 8, because production is
	// a small shared managed Postgres). At thirty candidates that is thirty serialized round
	// trips standing between a consumer and a placement, and under concurrent authorize load
	// it starves the money path: the symptom is a payment timeout, not slow routing, which is
	// why this is worth a store method rather than a comment about being careful.
	//
	// SAME SEMANTICS AS ByStation, deliberately, INCLUDING that it returns rows in any state.
	// Its callers decide about liveness themselves (dispatch refuses anything not Live), and a
	// batch form that quietly dropped terminal rows would answer "no such Station" where the
	// singular form answers "that Station is revoked" - a distinction the next caller may need
	// and cannot recover once it is gone. Absent ids are simply absent from the map, so
	// len(result) <= len(stationIDs) and a caller must not index it blindly.
	ByStations(stationIDs []string) (map[string]Attachment, error)
	// TouchRoutable stamps "the machine behind this Station was seen alive just now" onto each
	// of these attachments - the durable half of the detach path below.
	//
	// It is stamped by whichever instance publishes the Station as routable, because that is
	// the only place in the system holding both halves of the join at once: the attachment,
	// and this broker's live view of the node id written on it. Any instance's stamp counts,
	// so a node heartbeating to one broker keeps its attachment fresh everywhere.
	TouchRoutable(stationIDs []string, at time.Time) error
	// DetachIdle retires the live attachments behind one Tower whose machine has not been seen
	// alive since `before`, and reports which ones it retired.
	//
	// THE ATTACHMENT TABLE HAD NO WAY TO SHRINK. Nothing assigned StateDetached outside
	// terminal reaping, so an attachment lived until its owner revoked it - and a machine that
	// ran `roger share` once and pressed Ctrl-C stayed a live attachment, and a republished
	// routable row, for as long as the database existed. An eligibility gate keeps such a row
	// from taking traffic; it does nothing about a table that only grows.
	//
	// Measured on COALESCE(last_routable, attached_at), so a row written before the stamp
	// existed is judged from when it attached rather than treated as infinitely stale. The
	// horizon its caller passes is DAYS rather than minutes on purpose: the harm being fixed
	// is unbounded growth, which is slow, and the cost of being wrong is an operator's node
	// having to re-attach, which is not.
	//
	// IT RETIRES ONLY ROWS THAT CARRY A NODE ID, AND THAT SCOPE IS THE CORRECTNESS ARGUMENT
	// RATHER THAN AN OPTIMIZATION. A sweep may only judge a row it could have found evidence
	// FOR; measured against a row whose liveness is unknowable it is not a retirement, it is
	// a timer.
	//
	// TouchRoutable is the one and only source of that evidence, and publishRoutable stamps
	// it by joining the attachment's node id to this broker's live registrations - so a row
	// with no node id can never be stamped, by anybody, ever. Without this filter the
	// COALESCE fell back to attached_at forever, the row crossed the horizon on schedule, and
	// a CLASSIC operator-invited Station - which carries no node id, is skipped by
	// publishRoutable's stamping loop by construction, and has no roger-share half to
	// heartbeat - was retired seven days after it attached, every time, on its own Tower's
	// housekeeping tick. The state it assigned was terminal AND unrecoverable (checkBindings
	// answers "this Station ID has been retired and cannot be reattached"), so that was a
	// permanent loss of an operator's Station on a fixed timer, produced by the sweep that was
	// added to stop the table growing.
	//
	// IT WRITES StateDormant NOW, NOT StateDetached, and that is the other half of the same
	// lesson. Scoping the sweep to rows it can find evidence for stopped it retiring a
	// population it could never see; it did nothing about the population it CAN see going
	// quiet for ordinary reasons. Seven days with no stamp is a holiday, a house move, a
	// fortnight of downtime, or - since the stamp has exactly one writer - a liveness mirror
	// that was broken for a week on the instance holding this Tower'"'"'s link. Every one of those
	// used to end an operator'"'"'s Station identity forever.
	//
	// Dormant does everything the sweep was for: the row stops being live, stops being
	// published, stops appearing in the Tower'"'"'s node list, and stops counting against the
	// owner'"'"'s live cap. What it no longer does is decide, on a seven-day timer and a single
	// dependency, that a machine is never coming back. RetireDormant below is where that
	// decision is made, much later, and Revoke is where an owner makes it immediately.
	//
	// The alternative considered was to give classic attachments a liveness source of their
	// own. There is none to give: their machine is reached through the Tower's signed
	// inventory and never registers with a broker, so there is nothing on this side of the
	// wire that has ever seen it. A retirement pass for those rows would have to be written
	// against evidence that does not exist yet, and inventing one to justify a sweep is how
	// this defect happened in the first place. The table still shrinks for the population it
	// was growing from - self-attach is the frictionless attach/revoke loop, and every
	// self-attached row carries a proved node id (toweredgeattach.go refuses the attach
	// without one).
	DetachIdle(towerID string, before time.Time) ([]string, error)
	// RetireDormant moves long-dormant attachments to the terminal StateDetached, and reports
	// how many. It is the second half of the soft/terminal split: DetachIdle takes a Station
	// out of service on a horizon measured in days, and this takes its IDENTITY on one measured
	// in months.
	//
	// It is deliberately NOT scoped to a Tower and NOT run from publishRoutable. The sweep that
	// takes a row out of service belongs beside the thing that publishes rows, per Tower, on
	// the tick that already holds the id; the pass that ends an identity is fleet-wide
	// housekeeping and belongs beside the other reap, where a reader looking for irreversible
	// deletions finds all of them in one place.
	//
	// Measured on the same COALESCE(last_routable, attached_at) as DetachIdle, so the clock a
	// Station is judged by never changes underneath it: one horizon takes it out of service and
	// a much later one takes its name, both counted from the last time anybody saw the machine.
	// A Station that comes back before the second horizon keeps everything.
	RetireDormant(before time.Time) (int64, error)
	// ByTower lists the LIVE attachments whose origin is the given Tower - what that Tower's
	// hub must serve (Option C: the tower reads each node's HubToken from here).
	ByTower(towerID string) ([]Attachment, error)
	ByAssertionKey(key string) (Attachment, bool, error)
	// SetState moves an attachment through its lifecycle.
	SetState(stationID, state string) (bool, error)
	// ReapTerminal deletes revoked/detached attachments attached before the horizon. DORMANT IS
	// NOT TERMINAL and is never reaped here - RetireDormant is what makes a dormant row
	// terminal, and only then does this delete it. Terminal
	// rows are kept a while for forensics, but not forever: without a reap, an attach ->
	// revoke -> attach loop (frictionless on the self-attach path) grows the table without
	// bound - the same vector the invitation reap closes one table over.
	ReapTerminal(before time.Time) (int64, error)
	// MarkAuditProven records that a Station answered a content audit, once. Idempotent:
	// the FIRST answer is the proof, and re-stamping it would let a node that has since
	// stopped answering look freshly capable.
	MarkAuditProven(stationID string, at time.Time) (bool, error)
}

// NewInvite mints a one-use invitation and returns it alongside the PLAINTEXT secret, which
// is the only time that value exists outside the caller. Store the Authorization; show the
// secret once; never write it down.
func NewInvite(a Authorization, ttl time.Duration, now time.Time) (Authorization, string, error) {
	if err := a.Origin.check(); err != nil {
		return Authorization{}, "", err
	}
	switch {
	case a.ID == "", a.Network == "", a.StationID == "", a.Owner == "":
		// (a general presence check; the specific Station-ID shape is enforced just below,
		// because an ill-formed id is a different and more dangerous failure than a missing one)
		return Authorization{}, "", errors.New("an invitation needs an id, a network, a Station and an owner")
	case !ValidStationID(a.StationID):
		// THE NAME-INJECTION GATE. A Station ID flows into the edge certificate's DNS name, so
		// anything but the minted shape - a dot, a wildcard, whitespace - is refused here,
		// before it can ever reach the CA. See stationid.go for the wildcard-cert review.
		return Authorization{}, "", errors.New("a Station ID must be of the form st-<hex>")
	case a.AssertionKey == "" || a.SessionKey == "":
		return Authorization{}, "", errors.New("an invitation names both keys or it names neither")
	case a.AssertionKey == a.SessionKey:
		return Authorization{}, "", errors.New("the assertion and secure-session keys must be different keys")
	case ttl <= 0:
		return Authorization{}, "", errors.New("an invitation needs a positive lifetime")
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return Authorization{}, "", err
	}
	secret := hex.EncodeToString(raw)
	sum := sha256.Sum256([]byte(secret))
	a.SecretHash = hex.EncodeToString(sum[:])
	a.IssuedAt, a.ExpiresAt = now, now.Add(ttl)
	a.Consumed, a.ConsumedBy = false, ""
	return a, secret, nil
}

// Config bounds the admission.
type Config struct {
	Network string
	// Skew is how far ahead of us an issue time may sit before we call it a forgery.
	Skew time.Duration
	// MaxLiveStationsPerOwner bounds how many attached Stations one account may hold. Zero
	// disables the cap, which is right for a focused test and wrong for a deployment: capping
	// only the INVITATION narrows the growth vector without closing it, because an operator
	// can loop invite -> redeem and grow the attachment table instead.
	MaxLiveStationsPerOwner int
	Now                     func() time.Time
}

func (c *Config) defaults() {
	if c.Network == "" {
		c.Network = "roger-public"
	}
	if c.Skew <= 0 {
		c.Skew = 60 * time.Second
	}
	if c.Now == nil {
		c.Now = time.Now
	}
}

// Registry admits Stations and answers what Core knows about them.
type Registry struct {
	cfg   Config
	store Store
}

func New(cfg Config, s Store) *Registry {
	cfg.defaults()
	return &Registry{cfg: cfg, store: s}
}

// Admit runs the whole admission. On success the Station is recorded in QUARANTINE.
//
// The ordering below is deliberate: everything that can refuse runs BEFORE the authorization
// is spent, so no refusal leaves a consumed invitation the owner cannot use again.
func (r *Registry) Admit(p Proof) (Attachment, error) {
	now := r.cfg.Now()

	auth, ok, err := r.store.Authorization(p.AuthID)
	if err != nil {
		return Attachment{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if !ok {
		return Attachment{}, reject(errors.New("no such invitation"))
	}

	// A consumed authorization is either the caller retrying after a lost reply, or somebody
	// trying to mint a second identity from one invitation. The difference is whether the
	// proof is IDENTICAL to the one that won.
	if auth.Consumed {
		return r.replay(auth, p)
	}

	if err := r.validate(auth, p, now); err != nil {
		return Attachment{}, err
	}

	// Uniqueness, read before the commit. The commit itself is what settles a race; these
	// give a clear refusal in the ordinary case.
	revived, err := r.checkBindings(auth.ID, p)
	if err != nil {
		return Attachment{}, err
	}

	// A CAP ON LIVE ATTACHMENTS, not just on invitations. Capping the mint alone narrows the
	// growth vector without closing it: an operator can loop invite -> redeem and grow the
	// attachment table instead, one Tower and two requests at a time.
	if r.cfg.MaxLiveStationsPerOwner > 0 {
		live, cerr := r.store.CountLiveAttachments(p.Owner)
		if cerr != nil {
			return Attachment{}, fmt.Errorf("%w: %v", ErrUnavailable, cerr)
		}
		if live >= r.cfg.MaxLiveStationsPerOwner {
			return Attachment{}, reject(fmt.Errorf(
				"this account already holds %d attached Stations", live))
		}
	}

	at := Attachment{
		StationID:    p.StationID,
		Owner:        p.Owner,
		AssertionKey: p.AssertionKey,
		SessionKey:   p.SessionKey,
		Origin:       p.Origin,
		Epoch:        1,
		CeilingHash:  auth.CeilingHash,
		State:        StateQuarantine,
		AttachedAt:   now,
		AuthID:       auth.ID,
		HubToken:     auth.HubToken,
		// The join travels with the authorization, not the attach parameters: it is a fact
		// Core established when it issued the invitation, not something the attaching party
		// restates and could restate differently.
		NodeID:   auth.NodeID,
		Model:    auth.Model,
		Modality: auth.Modality,
		PriceIn:  auth.PriceIn,
		PriceOut: auth.PriceOut,
	}
	if revived.StationID != "" {
		// A DORMANT STATION WAKING UP CARRIES TWO THINGS FORWARD, and neither is cosmetic.
		//
		// THE EPOCH ADVANCES. It is the fence that lets an old origin's in-flight work be
		// refused after a move, and a revival may well land on a different Tower - Core picks
		// the first live one with an endpoint, and months have passed. Reusing the old epoch
		// would leave anything still holding the previous one indistinguishable from the
		// present.
		//
		// THE AUDIT PROOF STAYS. AuditProvenAt records that this Station has ANSWERED a content
		// audit, which is a fact about the machine and its software rather than about the
		// current attachment, and it is what retires a temporary leniency per node. Dropping it
		// would put a proven operator back behind the tolerance written for nodes that could not
		// answer at all - a downgrade for having been away.
		at.Epoch = revived.Epoch + 1
		at.AuditProvenAt = revived.AuditProvenAt
	}

	won, err := r.store.Admit(auth.ID, at)
	if err != nil {
		// A store REFUSAL passes through unchanged. Wrapping everything as an outage is the
		// bug this fix was supposed to close and then reinstated one layer up: the handler
		// answers "try again in a moment" to a Station ID that is already attached, and the
		// caller retries forever against something that will never change.
		if errors.Is(err, ErrRejected) {
			return Attachment{}, err
		}
		return Attachment{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if !won {
		// Somebody else consumed it between our read and our write. That is exactly the race
		// this design expects, and the loser answers from the winner's record rather than
		// refusing a caller who did nothing wrong.
		fresh, ok, ferr := r.store.Authorization(auth.ID)
		if ferr != nil {
			return Attachment{}, fmt.Errorf("%w: %v", ErrUnavailable, ferr)
		}
		if !ok {
			return Attachment{}, reject(errors.New("no such invitation"))
		}
		return r.replay(fresh, p)
	}
	return at, nil
}

// replay answers a caller presenting an already-consumed authorization. Identical proof gets
// the committed outcome; anything else is reuse and is refused.
func (r *Registry) replay(auth Authorization, p Proof) (Attachment, error) {
	at, ok, err := r.store.ByStation(auth.ConsumedBy)
	if err != nil {
		return Attachment{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if !ok {
		return Attachment{}, reject(errors.New("this invitation has already been used"))
	}
	// The secret is part of "identical proof". Without it, holding the authorization id and
	// the two PUBLIC keys is enough to confirm an attachment exists and read its record -
	// a probing oracle, even though no attachment can be minted this way.
	sum := sha256.Sum256([]byte(p.Secret))
	secretOK := auth.SecretHash != "" &&
		subtle.ConstantTimeCompare([]byte(hex.EncodeToString(sum[:])), []byte(auth.SecretHash)) == 1
	same := secretOK &&
		at.StationID == p.StationID &&
		at.Owner == p.Owner &&
		at.AssertionKey == p.AssertionKey &&
		at.SessionKey == p.SessionKey &&
		at.Origin == p.Origin
	if !same {
		return Attachment{}, reject(errors.New("this invitation has already been used"))
	}
	return at, nil
}

// validate is the refusal table. Every row leaves the invitation unspent.
func (r *Registry) validate(auth Authorization, p Proof, now time.Time) error {
	switch {
	case auth.ExpiresAt.IsZero() || !now.Before(auth.ExpiresAt):
		return reject(errors.New("this invitation has expired"))
	case auth.IssuedAt.After(now.Add(r.cfg.Skew)):
		return reject(errors.New("this invitation is not valid yet"))
	}

	// The network is checked against OUR configuration, not against the two sides agreeing
	// with each other - two peers can agree on the wrong network all day.
	if p.Network != r.cfg.Network || auth.Network != r.cfg.Network {
		return reject(errors.New("this invitation is for another network"))
	}

	if err := p.Origin.check(); err != nil {
		return reject(err)
	}
	if err := auth.Origin.check(); err != nil {
		return reject(err)
	}

	switch {
	case auth.StationID != p.StationID:
		return reject(errors.New("this invitation is for another Station"))
	case auth.Owner != p.Owner:
		return reject(errors.New("this invitation belongs to another owner"))
	case auth.Origin != p.Origin:
		return reject(errors.New("this invitation is for another origin"))
	case auth.AssertionKey != p.AssertionKey:
		return reject(errors.New("the assertion key is not the one this invitation names"))
	case auth.SessionKey != p.SessionKey:
		return reject(errors.New("the secure-session key is not the one this invitation names"))
	}

	// Two purposes, two keys. One key doing both jobs means compromising the offer signer
	// hands over live traffic as well, and there would be no separation left to rotate.
	if p.AssertionKey == "" || p.SessionKey == "" {
		return reject(errors.New("attachment needs both an assertion key and a secure-session key"))
	}
	if p.AssertionKey == p.SessionKey {
		return reject(errors.New("the assertion and secure-session keys must be different keys"))
	}

	// The one-use secret, checked last because it is the most expensive to get wrong: a
	// timing signal here would let somebody walk the verifier a byte at a time. An
	// authorization stored WITHOUT a verifier is unusable rather than open - a row that lost
	// its hash must not become an invitation anyone can redeem.
	if auth.SecretHash == "" {
		return reject(errors.New("this invitation has no verifier and cannot be redeemed"))
	}
	sum := sha256.Sum256([]byte(p.Secret))
	if subtle.ConstantTimeCompare([]byte(hex.EncodeToString(sum[:])), []byte(auth.SecretHash)) != 1 {
		return reject(errors.New("the invitation secret does not match"))
	}
	return nil
}

// checkBindings enforces the uniqueness rules and the immutability of origin kind, and - when
// the Station ID in front of it is a DORMANT one coming back - reports the row being revived so
// the caller can carry its history forward.
func (r *Registry) checkBindings(authID string, p Proof) (revived Attachment, err error) {
	existing, ok, err := r.store.ByStation(p.StationID)
	if err != nil {
		return Attachment{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if ok {
		// A racer that read the invitation BEFORE the winner committed arrives here after it
		// did. That is a retry, not a conflict: the attachment in front of us is the one this
		// very invitation produced, and refusing it would turn a lost response into a
		// permanent failure. Let it through to the store, which reports the authorization
		// already consumed, and the replay path answers with the committed outcome.
		if existing.AuthID == authID {
			return Attachment{}, nil
		}
		// THE SAME MACHINE, COMING BACK. A dormant Station presenting the SAME assertion key,
		// the SAME session key, the SAME owner and the SAME origin kind is not a new claimant to
		// a used name - it is the identity that was put to sleep, and the four things it has to
		// match are the four that describe who it is. agent.AttachTower produces exactly this
		// call, because the Station identity on disk is persistent by design: the same id and
		// the same keys, every run, forever.
		//
		// Everything below still applies to it. A different key, a different owner or a
		// different origin kind is refused with the sentence for that mismatch rather than with
		// the retirement one, which is the same distinction the two epoch refusals just got:
		// "you are somebody else" and "this Station is finished" want different sentences.
		if existing.Recoverable() && existing.Origin.Kind == p.Origin.Kind &&
			existing.AssertionKey == p.AssertionKey && existing.SessionKey == p.SessionKey &&
			existing.Owner == p.Owner {
			return existing, nil
		}
		switch {
		case existing.Origin.Kind != p.Origin.Kind:
			// The whole point of v1's immutability rule. Earnings lineage, capacity and held
			// compensation hang off this identity; letting the kind change would move all of
			// it silently.
			return Attachment{}, reject(errors.New(
				"this Station was admitted under a different origin kind, and origin kind cannot change: " +
					"revoke it and attach a new Station ID"))
		case existing.AssertionKey != p.AssertionKey:
			return Attachment{}, reject(errors.New("this Station ID is already bound to another assertion key"))
		case existing.Recoverable():
			// Dormant, but not the same machine - the keys or the owner do not match, and the
			// branch above already let the real one through. Say which, rather than borrowing
			// the terminal sentence: this Station is asleep and answerable, just not to you.
			return Attachment{}, reject(errors.New(
				"this Station ID is dormant and can only be reattached by the machine that holds " +
					"its keys, which these are not"))
		case !existing.Live():
			return Attachment{}, reject(errors.New("this Station ID has been retired and cannot be reattached"))
		default:
			// Already attached and still live. Two invitations can exist for one Station ID -
			// the invite route only refuses one whose Station is ALREADY attached - so
			// redeeming the second must be refused here rather than silently replacing the
			// first, which would reset its state, epoch and lineage.
			return Attachment{}, reject(errors.New("this Station is already attached"))
		}
	}

	// THERE IS DELIBERATELY NO UNIQUENESS RULE ON THE SECURE-SESSION KEY, and the absence is
	// load-bearing rather than an omission. Do not add one back.
	//
	// There used to be one, and the sentence it gave for itself was: "A secure-session key
	// belonging to another Station would let one machine terminate another's end-to-end
	// channel." That is false, and the whole of it is checkable from this file plus two others.
	//
	// NOTHING ROUTES BY THE SESSION KEY. A consumer is placed onto a STATION, and the key it
	// seals to is read out of THAT Station's own row - cmd/rogerai-broker/toweredge.go hands out
	// station_session_key beside relay_name, and towerdispatch.go seals to the SessionKey of the
	// attachment it is dispatching to. No routing, placement or dispatch path ever resolved a
	// Station FROM a session key - the only lookup that did was BySessionKey, whose sole caller
	// was this check, so it is deleted with it. Two rows carrying one key are therefore two
	// destinations and not one: nobody's traffic moves. A Station that names a key it cannot
	// open receives ciphertext it cannot open, serves nothing and earns nothing - and it cannot
	// hand the work to the machine that CAN open it either, because the grant names its own
	// relay and the receipt that closes the attempt has to be signed by ITS assertion key,
	// which the other machine does not hold. The rule was protecting against nothing.
	//
	// IT DID NOT EVEN BOUND THE CASE IT NAMED. Nothing anywhere proves the presenter holds the
	// private half of a session key: it is X25519, so it cannot sign, and the possession proof
	// has the assertion key merely VOUCH for it. A caller may present thirty-two zero bytes and
	// be admitted. "A Station that cannot open its own envelopes" was always reachable, so the
	// rule's only observable effect was to refuse the SECOND of two attaches naming one key.
	//
	// AND THAT EFFECT WAS A WEAPON. The key is self-serve: /tower/edge/authorize returns
	// station_session_key to any signed-in, funded consumer that asks for that Station's model.
	// So an attacker with their own account, their own assertion keypair and their own
	// registered node id could attach naming a VICTIM's session key and have the victim refused
	// right here, on every attach, indefinitely - station.InitOrOpen keeps that key on disk with
	// no re-mint path. The same shape as the assertion-key squat, bought for one request. The
	// cheapest close was to DELETE the rule rather than defend it: deleting removes a denial
	// primitive, where proving possession of the session key would have added a round trip and a
	// new primitive to protect a rule that earns nothing. docs/relay-selection-design.md 5.6
	// carries the argument in full, including why deriving the session key from the assertion
	// key - the direct analogue of what closed the Station id - was rejected.
	//
	// THE SPEC IS NOT BEING CONTRADICTED, IT IS BEING HALVED CORRECTLY, and this is the part
	// worth reading before anybody puts the rule back. features/tower/station_attachment.feature
	// specifies TWO clauses that go together: "A Station proves both independent private keys
	// during attachment" (K proved by a CSR bound to the attachment challenge) and the defect row
	// "secure-session key already bound to another Station". In THAT world the pair is coherent -
	// nobody can name a K they do not hold, so a duplicate can only ever be a genuine collision.
	// What was built is the second clause alone: there is no CSR and no inner TLS session (the
	// envelope package says so in its own comment), so K is asserted rather than proved. The
	// clause that refuses duplicates is the one that hurts the honest party; the clause that
	// makes it safe is the one that is missing.
	//
	// SO THIS IS A REMOVAL WITH A CONDITION ON ITS RETURN. The day K is genuinely proved - the
	// spec's CSR, a challenge Core seals to K and the node returns, or a static-static X25519
	// agreement against Core's envelope key - uniqueness costs nothing again and should come back
	// in the same commit as the proof, never before it and never without it.
	//
	// The ASSERTION key's rule below is a different rule and stays. That key SIGNS: two Stations
	// signing with one key are one signer wearing two identities, which is a statement about
	// evidence and money rather than about who can read what.
	if bound, ok, err := r.store.ByAssertionKey(p.AssertionKey); err != nil {
		return Attachment{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	} else if ok && bound.StationID != p.StationID {
		return Attachment{}, reject(errors.New("that assertion key is already bound to another Station"))
	}
	return Attachment{}, nil
}

// Station is the read inv.Policy needs: what Core knows about a Station ID.
func (r *Registry) Station(stationID string) (Attachment, bool, error) {
	at, ok, err := r.store.ByStation(stationID)
	if err != nil {
		return Attachment{}, false, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return at, ok, nil
}

// Revoke retires a Station identity terminally. Terminal is the point: the spec requires a
// cross-kind migration to go through revocation and a NEW Station ID, so a revoked identity
// must never come back.
// ByTower lists the live attachments served through one Tower.
func (r *Registry) ByTower(towerID string) ([]Attachment, error) {
	return r.store.ByTower(towerID)
}

// MarkAuditProven records that a Station answered a content audit - see
// Attachment.AuditProvenAt for why that fact is worth keeping.
func (r *Registry) MarkAuditProven(stationID string, at time.Time) (bool, error) {
	return r.store.MarkAuditProven(stationID, at)
}

// ByAssertionKey resolves the live attachment holding this assertion key, if any - what the
// self-attach path uses to answer a lost-response retry idempotently.
func (r *Registry) ByAssertionKey(key string) (Attachment, bool, error) {
	return r.store.ByAssertionKey(key)
}

func (r *Registry) Revoke(stationID string) (bool, error) {
	ok, err := r.store.SetState(stationID, StateRevoked)
	if err != nil {
		return false, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return ok, nil
}

// Promote moves a quarantined Station to active. It is deliberately separate from Admit:
// admission proves identity, and only Core-observed evidence earns eligibility.
func (r *Registry) Promote(stationID string) (bool, error) {
	at, ok, err := r.store.ByStation(stationID)
	if err != nil {
		return false, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if !ok || at.State != StateQuarantine {
		return false, nil
	}
	moved, err := r.store.SetState(stationID, StateActive)
	if err != nil {
		return false, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return moved, nil
}
