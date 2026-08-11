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
	"sync"
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
	// ResponseDigest is over the exact bytes the Station produced.
	ResponseDigest string `json:"response_digest"`
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
	Now      func() time.Time
}

type state int

const (
	issued state = iota
	claimed
	settled
)

type attempt struct {
	grant    Grant
	target   Target
	state    state
	deadline time.Time
}

// Registry issues grants and admits exactly one result for each.
type Registry struct {
	cfg Config
	mu  sync.Mutex
	by  map[string]*attempt
}

func New(cfg Config) *Registry {
	if cfg.Lifetime <= 0 {
		cfg.Lifetime = 2 * time.Minute
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Registry{cfg: cfg, by: map[string]*attempt{}}
}

// Issue mints a grant for one request on one Station.
func (r *Registry) Issue(t Target, request []byte) (Grant, error) {
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

	r.mu.Lock()
	defer r.mu.Unlock()
	r.by[g.AttemptID] = &attempt{grant: g, target: t, state: issued, deadline: g.Deadline}
	return g, nil
}

// Claim takes an issued attempt, exactly once.
//
// The CAS is the whole function. Two frames delivering the same grant concurrently must
// produce at most one execution, and a check followed by a separate write is not that - both
// would read "issued" and both would proceed.
func (r *Registry) Claim(attemptID, towerID string) (Grant, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	a, ok := r.by[attemptID]
	// A grant issued for another Tower is answered as NOT FOUND rather than as forbidden. An
	// attempt id is not a secret, and distinguishing the two would turn this into an oracle
	// for which attempts exist.
	if !ok || a.grant.TowerID != towerID {
		return Grant{}, ErrNotFound
	}
	if !r.cfg.Now().Before(a.deadline) {
		return Grant{}, ErrExpired
	}
	switch a.state {
	case claimed:
		return Grant{}, ErrAlreadyClaimed
	case settled:
		return Grant{}, ErrAlreadySettled
	}
	a.state = claimed
	return a.grant, nil
}

// Complete admits the one result an attempt may have.
func (r *Registry) Complete(attemptID string, rec Receipt, body []byte) (Grant, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	a, ok := r.by[attemptID]
	if !ok {
		return Grant{}, ErrNotFound
	}
	switch a.state {
	case issued:
		return Grant{}, ErrNotClaimed
	case settled:
		return Grant{}, ErrAlreadySettled
	}
	if !r.cfg.Now().Before(a.deadline) {
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
	if err := towerobj.Verify(a.target.AssertionKey, r.cfg.Network, TypeReceipt, Version,
		rec.Signed, "station_sig"); err != nil {
		return Grant{}, ErrReceiptSignature
	}
	// And the bytes must be the bytes the Station signed for. This is what catches a relay
	// that changed, truncated, prefixed, appended or substituted the answer.
	if rec.ResponseDigest == "" || rec.ResponseDigest != digestOf(body) {
		return Grant{}, ErrResultMismatch
	}
	a.state = settled
	return a.grant, nil
}

// Pending reports how many attempts are still held.
func (r *Registry) Pending() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.by)
}

// Reap drops attempts past their deadline, and reports how many went.
//
// An attempt table that only grows is a memory leak with a deadline attached; the deadline
// is exactly what makes dropping them safe, because nothing may settle after it anyway.
func (r *Registry) Reap() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.cfg.Now()
	n := 0
	for id, a := range r.by {
		if !now.Before(a.deadline) {
			delete(r.by, id)
			n++
		}
	}
	return n
}

// SignReceipt is what a STATION produces. It lives here so both sides use one definition of
// the signed bytes - two implementations of "what is signed" is two implementations that
// will eventually disagree, and the disagreement looks exactly like an attack.
func SignReceipt(priv ed25519.PrivateKey, network string, g Grant, body []byte) (Receipt, error) {
	rec := Receipt{AttemptID: g.AttemptID, ResponseDigest: digestOf(body)}
	raw, err := json.Marshal(map[string]any{
		"network":         network,
		"type":            TypeReceipt,
		"version":         towerobj.FormatInt(Version),
		"attempt_id":      rec.AttemptID,
		"station_id":      g.StationID,
		"response_digest": rec.ResponseDigest,
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
	var obj struct {
		Network       string `json:"network"`
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
	if obj.Network != network {
		return Grant{}, fmt.Errorf("this grant is for network %q, not %q", obj.Network, network)
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
