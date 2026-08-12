// Package dispatch hands one unit of work to one Station and verifies the one answer that
// comes back.
//
// It is the object in the middle of the joined network's most sensitive exchange: Core
// selected a Station, the Tower relays to it, and neither the Tower nor the Station is
// trusted. Everything here exists so that what comes back can be checked rather than
// believed.
//
// # THE TWO SIGNATURES
//
//	the GRANT     signed by Core. It authorizes exactly one attempt, on exactly one Station,
//	              under exactly one Tower, over exactly one request, until exactly one
//	              deadline. A Station verifies it before executing; a relay that alters any
//	              field breaks it.
//	the RECEIPT   signed by the STATION, with the assertion key recorded at attachment. It
//	              commits to the attempt AND to a digest of the exact bytes returned. A Tower
//	              holds a perfectly valid identity of its own and that buys it nothing here:
//	              it cannot produce this signature, so it cannot fabricate a result.
//
// The grant commits to the REQUEST digest and the receipt to the RESPONSE digest, which is
// what makes "the Station was given different plaintext" and "the Tower changed the answer"
// two separately detectable things rather than one indistinguishable mess.
//
// # WHAT THIS DELIBERATELY DOES NOT DO: MONEY
//
// The full contract in features/tower/job_and_settlement.feature binds a grant to a funding
// reservation CAS, an attempt-event chain, and a compensation ledger head. NONE of that
// exists yet. So nothing here holds, settles, credits or debits anything, and no field
// implies it does: Tower-backed work is UNCOMPENSATED in this version, which is the order
// the plan itself sets out - canary free traffic before ordinary paid workloads, and the
// compensated tier only after real-fund allocation is proven.
//
// A grant that quietly implied payment authority would be the single worst thing to get
// wrong in this package, so the prices a Station offered are deliberately NOT carried here.
// When settlement exists it will be built against the ledger, not retrofitted onto this.
//
// # SINGLE INSTANCE
//
// The registry is in-process. Two broker instances issuing grants would each enforce
// one-use over their own half, which is exactly the guarantee that must not be approximate -
// so a multi-instance deployment needs this moved behind the same durable CAS the admission
// registry uses. Stated here because it is a limit, not a design.
package dispatch

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"rogerai.fm/roger/v5/internal/towerobj"
)

// TypeGrant and Version identify the signed object, so a grant can never be replayed as some
// other object that happens to share a field set.
const (
	TypeGrant   = "dispatch.grant"
	TypeReceipt = "dispatch.receipt"
	Version     = 1
)

// The refusals. Each is distinct because the caller must do something different about it,
// and the HTTP layer maps them to different statuses.
var (
	ErrNotFound         = errors.New("no such attempt")
	ErrAlreadyClaimed   = errors.New("this attempt has already been claimed")
	ErrNotClaimed       = errors.New("this attempt has not been claimed")
	ErrAlreadySettled   = errors.New("this attempt has already settled")
	ErrExpired          = errors.New("this attempt is past its deadline")
	ErrResultMismatch   = errors.New("the result does not match the signed response digest")
	ErrReceiptSignature = errors.New("the receipt is not signed by the Station's assertion key")
	ErrContextMismatch  = errors.New("the receipt is for a different attempt")
)

// Target is the Station Core selected, as Core knows it - never as anyone claimed it.
type Target struct {
	TowerID   string
	StationID string
	// StationEpoch fences a rehome: work granted under the old origin cannot be completed
	// after the Station has moved.
	StationEpoch int64
	Model        string
	Modality     string
	// AssertionKey is the key from the ATTACHMENT record. It is what the receipt is checked
	// against, and taking it from anywhere else would make "signed by the Station" mean
	// "signed by whoever told us which key to use".
	AssertionKey ed25519.PublicKey
	// SessionKey is the Station's X25519 secure-session key, also from the attachment record.
	// It is what the request is sealed to, so the Tower carrying it cannot read the content.
	SessionKey []byte
}

// Grant authorizes one attempt.
type Grant struct {
	JobID        string
	AttemptID    string
	TowerID      string
	StationID    string
	StationEpoch int64
	Model        string
	Modality     string
	// RequestDigest binds the exact bytes the Station must be given.
	RequestDigest string
	Deadline      time.Time
	Nonce         string
	// Signed is the canonical signed object, relayed verbatim to the Station.
	Signed []byte
}

// Receipt is the Station's signed statement about what it returned.
type Receipt struct {
	AttemptID string `json:"attempt_id"`
	// RequestDigest is over the exact request bytes the Station received. It is what a
	// sampled transcript audit checks the stored request against.
	RequestDigest string `json:"request_digest"`
	// ResponseDigest is over the exact bytes the Station produced.
	ResponseDigest string `json:"response_digest"`
	// Usage is what the Station claims it spent, IN THE SIGNATURE. It is the claim the
	// Station is paid on, so it must not be alterable by the relay carrying the receipt.
	Usage Usage `json:"usage"`
	// Signed is the canonical Station-signed object.
	Signed []byte `json:"signed"`
}

// Config is how a registry is built.
type Config struct {
	Network string
	// Signer is Core's grant key.
	Signer ed25519.PrivateKey
	// Lifetime bounds an attempt. It is the only thing limiting how long a Station may hold
	// work that nobody is waiting for any more.
	Lifetime time.Duration
	// EdgeLifetime bounds an EDGE attempt, which lives on a different clock: the grant has
	// to survive the consumer receiving it, dialling the Tower, and the model completing -
	// not just a queue hop. Zero means Lifetime.
	EdgeLifetime time.Duration
	Now          func() time.Time
}

// The three states an attempt passes through, in order. They are strings because they are
// written to a durable store and read back by another process, where an integer would be a
// number nobody can interpret from a psql prompt at three in the morning.
const (
	StateIssued  = "issued"
	StateClaimed = "claimed"
	StateSettled = "settled"
)

// Record is one attempt as a store holds it.
//
// It carries the Station's assertion key because the RECEIPT is verified against it, and the
// instance verifying is very often not the instance that issued: taking the key from the
// attempt Core recorded is what keeps "signed by the Station" true across a fleet of brokers.
type Record struct {
	AttemptID     string
	JobID         string
	TowerID       string
	StationID     string
	StationEpoch  int64
	Model         string
	Modality      string
	RequestDigest string
	Nonce         string
	Deadline      time.Time
	Grant         []byte
	// Request is the exact body the grant commits to by digest.
	//
	// Stored, not merely hashed, because ANY instance may have to hand this work out and the
	// Tower needs the bytes rather than a promise about them. Keeping only the digest would
	// make cross-instance dispatch impossible: the instance holding the request would be the
	// only one that could serve the poll, which is the single-broker assumption this store
	// exists to remove.
	Request      []byte
	AssertionKey []byte
	// ConsumerKey is the account an EDGE grant was issued to, so the acknowledgement can be
	// bound to the authorized consumer rather than accepted from anyone who learns the id.
	// Empty on the relayed path, which has no consumer. A review found the ack unbound.
	ConsumerKey []byte
	State       string
}

func (r Record) grant() Grant {
	return Grant{
		JobID: r.JobID, AttemptID: r.AttemptID, TowerID: r.TowerID, StationID: r.StationID,
		StationEpoch: r.StationEpoch, Model: r.Model, Modality: r.Modality,
		RequestDigest: r.RequestDigest, Deadline: r.Deadline, Nonce: r.Nonce, Signed: r.Grant,
	}
}

// Store is where attempts live.
//
// THE STATE TRANSITIONS ARE THE WHOLE INTERFACE, and each is a COMPARE-AND-SWAP rather than
// a read followed by a write. That is not a performance choice: "at most one attempt reaches
// executing state" and "at most one result can settle" are the two guarantees this package
// exists for, and a check-then-act cannot provide either - two callers both read "issued",
// both proceed, and the work happens twice.
//
// It is an interface because a single broker can hold this in memory and a fleet of them
// cannot. With more than one instance the guarantee has to be enforced somewhere both can
// see, or each enforces it over its own half and neither enforces it at all.
type Store interface {
	// Put records a freshly issued attempt.
	Put(Record) error
	// ClaimByID moves ONE named attempt from issued to claimed, for this Tower.
	ClaimByID(attemptID, towerID string, now time.Time) (Record, error)
	// ClaimNext takes any issued attempt for this Tower and claims it in the same step.
	//
	// This is also the QUEUE. A separate list of pending work would need its own single-
	// delivery rule; taking the claim as the act of dequeuing means the guarantee is already
	// there and there is only one thing to get right.
	ClaimNext(towerID string, now time.Time) (Record, bool, error)
	// Get reads one back.
	Get(attemptID string) (Record, bool, error)
	// Settle moves claimed to settled, once.
	Settle(attemptID string, now time.Time) (Record, error)
	// Reap drops attempts past their deadline.
	Reap(before time.Time) (int64, error)
}

// Registry issues grants and admits exactly one result for each.
type Registry struct {
	cfg   Config
	store Store
}

// New builds a registry over the in-process store. Correct for one broker, and see Store for
// why that is not correct for two.
func New(cfg Config) *Registry { return NewWithStore(cfg, nil) }

// NewWithStore builds a registry over an explicit store.
func NewWithStore(cfg Config, store Store) *Registry {
	if cfg.Lifetime <= 0 {
		cfg.Lifetime = 2 * time.Minute
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if store == nil {
		store = NewMemStore()
	}
	return &Registry{cfg: cfg, store: store}
}

// Issue mints a grant and makes it collectable in one step.
//
// Callers that must do something BETWEEN those two - recording the attempt, which has to
// commit before a grant may be transmitted - use Mint and Publish instead.
func (r *Registry) Issue(t Target, request []byte) (Grant, error) {
	g, err := r.Mint(t, request)
	if err != nil {
		return Grant{}, err
	}
	if err := r.Publish(g, t, request); err != nil {
		return Grant{}, err
	}
	return g, nil
}

// Mint builds and signs a grant WITHOUT making it collectable.
//
// Nothing can be claimed against a minted grant, which is the point: the attempt has to be
// recorded first. "the lease or grant cannot be transmitted before that commit" - and a
// grant sitting in a queue is transmitted the moment a Tower polls, whatever the caller
// intended to do next.
func (r *Registry) Mint(t Target, request []byte) (Grant, error) {
	switch {
	case t.TowerID == "" || t.StationID == "":
		return Grant{}, errors.New("a grant names exactly one Tower and one Station")
	case t.Model == "" || t.Modality == "":
		return Grant{}, errors.New("a grant names the model and modality it authorizes")
	case len(t.AssertionKey) != ed25519.PublicKeySize:
		return Grant{}, errors.New("a grant needs the Station's attachment-recorded assertion key")
	case len(request) == 0:
		// A grant over nothing would let ANY empty request be substituted for any other, and
		// the digest would still match.
		return Grant{}, errors.New("a grant commits to a request, and there is none")
	}

	now := r.cfg.Now()
	g := Grant{
		JobID:         "job-" + randomHex(12),
		AttemptID:     "att-" + randomHex(12),
		TowerID:       t.TowerID,
		StationID:     t.StationID,
		StationEpoch:  t.StationEpoch,
		Model:         t.Model,
		Modality:      t.Modality,
		RequestDigest: digestOf(request),
		Deadline:      now.Add(r.cfg.Lifetime),
		Nonce:         randomHex(16),
	}
	body, err := json.Marshal(map[string]any{
		"network":        r.cfg.Network,
		"type":           TypeGrant,
		"version":        towerobj.FormatInt(Version),
		"job_id":         g.JobID,
		"attempt_id":     g.AttemptID,
		"tower_id":       g.TowerID,
		"station_id":     g.StationID,
		"station_epoch":  towerobj.FormatInt(g.StationEpoch),
		"model":          g.Model,
		"modality":       g.Modality,
		"request_digest": g.RequestDigest,
		"deadline":       towerobj.FormatInt(g.Deadline.Unix()),
		"nonce":          g.Nonce,
	})
	if err != nil {
		return Grant{}, err
	}
	signed, err := towerobj.Sign(r.cfg.Signer, r.cfg.Network, TypeGrant, Version, body, "core_sig")
	if err != nil {
		return Grant{}, err
	}
	g.Signed = signed
	return g, nil
}

// Store exposes the attempt store, for callers that enforce one-use on paths the Registry
// itself does not walk - the edge settlement, where the claim and the settle happen in one
// place rather than at collection and result time.
func (r *Registry) Store() Store { return r.store }

// Publish makes a minted grant collectable.
func (r *Registry) Publish(g Grant, t Target, request []byte) error {
	return r.store.Put(Record{
		AttemptID: g.AttemptID, JobID: g.JobID, TowerID: g.TowerID, StationID: g.StationID,
		StationEpoch: g.StationEpoch, Model: g.Model, Modality: g.Modality,
		RequestDigest: g.RequestDigest, Nonce: g.Nonce, Deadline: g.Deadline,
		Grant: g.Signed, Request: request, AssertionKey: t.AssertionKey, State: StateIssued,
	})
}

// Claim takes an issued attempt, exactly once.
//
// The CAS is the whole function. Two frames delivering the same grant concurrently must
// produce at most one execution, and a check followed by a separate write is not that - both
// would read "issued" and both would proceed.
func (r *Registry) Claim(attemptID, towerID string) (Grant, error) {
	rec, err := r.store.ClaimByID(attemptID, towerID, r.cfg.Now())
	if err != nil {
		return Grant{}, err
	}
	return rec.grant(), nil
}

// ClaimNext hands this Tower any one attempt waiting for it, claiming it in the same step.
//
// This is what a Tower's poll calls, and taking the claim AS the dequeue is what makes it
// safe for two brokers to be polled at once: both may see the same attempt, and exactly one
// compare-and-swap wins it.
func (r *Registry) ClaimNext(towerID string) (Grant, []byte, bool, error) {
	rec, ok, err := r.store.ClaimNext(towerID, r.cfg.Now())
	if err != nil || !ok {
		return Grant{}, nil, false, err
	}
	// The REQUEST comes back with the grant. A Tower handed an authorization without the
	// bytes it authorizes has nothing to relay, and this instance may never have seen them.
	return rec.grant(), rec.Request, true, nil
}

// Complete admits the one result an attempt may have.
func (r *Registry) Complete(attemptID string, rec Receipt, body []byte) (Grant, error) {
	a, ok, err := r.store.Get(attemptID)
	if err != nil {
		return Grant{}, err
	}
	if !ok {
		return Grant{}, ErrNotFound
	}
	switch a.State {
	case StateIssued:
		return Grant{}, ErrNotClaimed
	case StateSettled:
		return Grant{}, ErrAlreadySettled
	}
	if !r.cfg.Now().Before(a.Deadline) {
		return Grant{}, ErrExpired
	}
	// THE RECEIPT NAMES ITS ATTEMPT. A perfectly signed receipt for a different attempt is a
	// context mismatch, and checking the signature without checking this would let a valid
	// result for job A settle job B.
	if rec.AttemptID != attemptID {
		return Grant{}, ErrContextMismatch
	}
	// Verified against the ATTACHMENT's key, before the digest: a signature by anyone else
	// makes the digest it commits to meaningless.
	if err := towerobj.Verify(a.AssertionKey, r.cfg.Network, TypeReceipt, Version,
		rec.Signed, "station_sig"); err != nil {
		return Grant{}, ErrReceiptSignature
	}
	// And the bytes must be the bytes the Station signed for. This is what catches a relay
	// that changed, truncated, prefixed, appended or substituted the answer.
	if rec.ResponseDigest == "" || rec.ResponseDigest != digestOf(body) {
		return Grant{}, ErrResultMismatch
	}
	// THE STATE CHANGE IS LAST AND IS A CAS. Verification above is side-effect free and can
	// safely run twice; this cannot, and it is what makes "at most one result settles" true
	// when two brokers are handed the same result at once. Losing the swap means somebody
	// else settled it first, which is an answer rather than an error in our own logic.
	settled, err := r.store.Settle(attemptID, r.cfg.Now())
	if err != nil {
		return Grant{}, err
	}
	return settled.grant(), nil
}

// Pending reports how many attempts are still held.
func (r *Registry) Pending() int {
	n, _ := r.store.(interface{ Len() int })
	if n == nil {
		return 0
	}
	return n.Len()
}

// Reap drops attempts past their deadline, and reports how many went.
//
// An attempt table that only grows is a memory leak with a deadline attached; the deadline
// is exactly what makes dropping them safe, because nothing may settle after it anyway.
func (r *Registry) Reap() int {
	n, err := r.store.Reap(r.cfg.Now())
	if err != nil {
		return 0
	}
	return int(n)
}

// SignReceipt is what a STATION produces. It lives here so both sides use one definition of
// the signed bytes - two implementations of "what is signed" is two implementations that
// will eventually disagree, and the disagreement looks exactly like an attack.
// The receipt carries the Station's OWN usage claim, signed in. On the relayed path Core
// observed the bytes itself and this is corroboration; on the edge path it is the claim the
// Station is paid on, and it must be inside the signature - a usage figure carried beside
// the receipt would be writable by the Tower forwarding it, and "settlement never reads the
// Tower's numbers" is the whole point of the evidence design.
func SignReceipt(priv ed25519.PrivateKey, network string, g Grant, request, body []byte, u Usage) (Receipt, error) {
	if u.In < 0 || u.Out < 0 {
		return Receipt{}, errors.New("a receipt cannot claim negative usage")
	}
	// The REQUEST digest is committed to as well as the response, and it is what makes a
	// sampled transcript checkable at BOTH ends: an audit hashes the stored request and
	// response and both must match what the Station signed here. On the relayed path the
	// grant already carried a request digest, but the receipt did not commit to it - so a
	// Station could have served a different request than the grant authorized and nothing
	// downstream of the grant check would notice. Here it signs for exactly the bytes it saw.
	rec := Receipt{AttemptID: g.AttemptID, RequestDigest: digestOf(request), ResponseDigest: digestOf(body), Usage: u}
	raw, err := json.Marshal(map[string]any{
		"network":         network,
		"type":            TypeReceipt,
		"version":         towerobj.FormatInt(Version),
		"attempt_id":      rec.AttemptID,
		"station_id":      g.StationID,
		"request_digest":  rec.RequestDigest,
		"response_digest": rec.ResponseDigest,
		"usage_in":        towerobj.FormatInt(u.In),
		"usage_out":       towerobj.FormatInt(u.Out),
	})
	if err != nil {
		return Receipt{}, err
	}
	signed, err := towerobj.Sign(priv, network, TypeReceipt, Version, raw, "station_sig")
	if err != nil {
		return Receipt{}, err
	}
	rec.Signed = signed
	return rec, nil
}

// digestOf is the commitment used for both request and response bodies.
func digestOf(b []byte) string {
	sum := sha256.Sum256(b)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func randomHex(n int) string {
	raw := make([]byte, n)
	if _, err := rand.Read(raw); err != nil {
		// A predictable attempt id or nonce is not something to carry on through: the nonce
		// is what makes a grant one-use, and guessing one is guessing an authorization.
		panic("crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(raw)
}

// ParseGrant is the STATION's side of the grant: verify it came from Core, then read it.
//
// It lives here so there is ONE definition of what a grant is and what makes it valid.
// Two implementations of that - one issuing, one checking - is two implementations that
// will eventually disagree about a field, and a disagreement about whether an authorization
// is valid looks exactly like an attack from both ends.
//
// The checks are in the order that makes each meaningful: the SIGNATURE first, because every
// field below is worthless until we know Core wrote them; then that the grant is for THIS
// Station, because a valid grant for somebody else is not authorization; then the deadline;
// then the request digest, which is what catches a relay handing over different bytes than
// the ones Core authorized.
func ParseGrant(raw []byte, coreKey ed25519.PublicKey, network, stationID string, request []byte, now time.Time) (Grant, error) {
	if err := towerobj.Verify(coreKey, network, TypeGrant, Version, raw, "core_sig"); err != nil {
		return Grant{}, fmt.Errorf("this grant is not signed by Roger Core: %w", err)
	}
	// No network check below: towerobj.Verify BINDS the network into the signature, so a
	// grant for another network has already failed above. A second comparison here would be
	// a branch no input can reach, which is worse than no check - it reads as protection and
	// protects nothing.
	var obj struct {
		JobID         string `json:"job_id"`
		AttemptID     string `json:"attempt_id"`
		TowerID       string `json:"tower_id"`
		StationID     string `json:"station_id"`
		StationEpoch  string `json:"station_epoch"`
		Model         string `json:"model"`
		Modality      string `json:"modality"`
		RequestDigest string `json:"request_digest"`
		Deadline      string `json:"deadline"`
		Nonce         string `json:"nonce"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return Grant{}, fmt.Errorf("this grant cannot be read: %w", err)
	}
	// A grant for another Station is somebody else's authorization. A relay holding one and
	// pointing it at this Station is the attack this check exists for.
	if obj.StationID != stationID {
		return Grant{}, fmt.Errorf("this grant is for Station %q, not this one", obj.StationID)
	}
	epoch, err := strconv.ParseInt(obj.StationEpoch, 10, 64)
	if err != nil {
		return Grant{}, errors.New("this grant's Station epoch is not a number")
	}
	unix, err := strconv.ParseInt(obj.Deadline, 10, 64)
	if err != nil {
		return Grant{}, errors.New("this grant's deadline is not a time")
	}
	deadline := time.Unix(unix, 0)
	if !now.Before(deadline) {
		return Grant{}, ErrExpired
	}
	// THE BYTES MUST BE THE BYTES CORE AUTHORIZED. Without this a relay could pass a valid
	// grant alongside a request of its own choosing, and the Station's receipt would attest
	// to work nobody asked for.
	if digestOf(request) != obj.RequestDigest {
		return Grant{}, errors.New("the request does not match what this grant authorizes")
	}
	return Grant{
		JobID: obj.JobID, AttemptID: obj.AttemptID, TowerID: obj.TowerID,
		StationID: obj.StationID, StationEpoch: epoch, Model: obj.Model,
		Modality: obj.Modality, RequestDigest: obj.RequestDigest,
		Deadline: deadline, Nonce: obj.Nonce, Signed: raw,
	}, nil
}
