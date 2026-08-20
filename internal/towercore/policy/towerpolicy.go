// Package towerpolicy is Roger Core answering, from its own records, the questions
// towerinv is forbidden to answer for itself.
//
// towerinv does cryptography, structure, sequencing and arithmetic. It cannot know whether
// an owner is suspended, whether a Station is banned, or what a model may cost - so it asks,
// through inv.Policy. Until now nothing implemented that interface, which meant the
// inventory slice could not run at all. This is the implementation, and its inputs are
// Core's own registry: the Station attachment record, the ban sets, the owner account, and
// the public price ceilings. Not one of them comes from a Tower.
//
// EVERYTHING HERE FAILS CLOSED. That is the single most important property in the package
// and the reason it is written as it is. inv.Policy has no error returns - Station
// hands back a Registration, ModelAllowed hands back a bool - so a read that fails has
// nowhere to report itself. The tempting implementation returns the zero value and moves
// on, which for a bool means "not allowed" (safe) but for a ban lookup means "not banned"
// (catastrophic): a database blink would quietly re-admit every banned Station on the
// network at the exact moment nobody could see why.
//
// So a failed read is recorded as a REFUSAL, not as an absence:
//
//   - if the ban sets cannot be read, every Station reports Unavailable
//   - if the attachment cannot be read, the Station reports Known=false
//   - if the owner cannot be read, the Station reports OwnerPresent=false
//
// The cost of being wrong in that direction is an operator who is temporarily not routable
// and complains. The cost of being wrong in the other direction is a banned Station serving
// customer traffic. Those are not comparable, so the choice is not a close call.
//
// The ban sets are CACHED with a refresh interval rather than read per leaf. An inventory
// can carry ten thousand leaves, and ten thousand ban lookups per revision would put the
// database on a path the relay-link design explicitly keeps it off. A stale-by-seconds ban
// set is acceptable; a per-leaf query is not. Revocation urgency is handled by Forget on the
// inventory set, which is immediate.
package policy

import (
	"crypto/ed25519"
	"encoding/hex"
	"strings"
	"sync"
	"time"

	"rogerai.fm/roger/v5/internal/towercore/attach"
	"rogerai.fm/roger/v5/internal/towercore/inv"
)

// Stations is the attachment registry read. Implemented by *attach.Registry.
type Stations interface {
	Station(stationID string) (attach.Attachment, bool, error)
}

// Bans is Core's ban state. Implemented by the store.
type Bans interface {
	BannedOwners() (map[string]string, error)
	BannedNodes() (map[string]string, error)
}

// Owners resolves an owner account from its pubkey, so a suspended or deleted account can
// be refused even while its Station attachment still looks healthy.
type Owners interface {
	OwnerByPubkey(pubkey string) (Owner, bool, error)
}

// Owner is the slice of an account this package needs. Kept narrow deliberately: a policy
// that could see the whole account record would grow reasons to consult things it should
// not.
type Owner struct {
	Suspended bool
}

// Config supplies the parts that are policy rather than registry.
type Config struct {
	// ModelAllowed and ModalityAllowed report whether Core will route this at all. A nil
	// ModelAllowed refuses everything, because "no allow-list configured" must not mean
	// "allow anything".
	ModelAllowed    func(model string) bool
	ModalityAllowed func(modality string) bool

	// PriceBand is the public floor and ceiling for a model, in MICRO-USD per 1,000,000
	// tokens. Integer units throughout: the signed offer format refuses JSON numbers
	// precisely so money never travels as a float, and converting to one here to compare
	// would put the rounding back. A nil PriceBand refuses every model.
	PriceBand func(model string) (floor, ceiling int64, ok bool)

	// BanRefresh is how long a cached ban set is trusted. Zero means 30 seconds.
	BanRefresh time.Duration
	Now        func() time.Time
}

// Policy implements inv.Policy.
type Policy struct {
	stations Stations
	bans     Bans
	owners   Owners
	cfg      Config

	mu       sync.RWMutex
	banned   map[string]bool // owner pubkey or node id -> banned
	loadedAt time.Time
	// loadFailed marks the last refresh as failed, so callers refuse. failedAt paces the
	// RETRY: without it a failed load makes the cache permanently non-fresh, and every leaf
	// of a ten-thousand-leaf inventory issues two more queries against a database that is
	// already failing - exactly the per-leaf lookup pattern this cache exists to prevent,
	// arriving at the worst possible moment.
	loadFailed bool
	failedAt   time.Time
}

// New builds the policy. A zero Config is SAFE but useless - it refuses everything, which is
// the correct direction for a misconfiguration.
func New(s Stations, b Bans, o Owners, cfg Config) *Policy {
	if cfg.BanRefresh <= 0 {
		cfg.BanRefresh = 30 * time.Second
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Policy{stations: s, bans: b, owners: o, cfg: cfg, loadFailed: true}
}

// Station is the read towerinv makes for every leaf.
func (p *Policy) Station(stationID string) inv.Registration {
	// A ban set we could not load makes EVERY Station UNAVAILABLE - refused, but not accused.
	// See the package doc: the zero value of "banned" is the dangerous direction, and
	// reporting a ban that did not happen is the dishonest one.
	banned, ok := p.isBanned(stationID)
	if !ok {
		return inv.Registration{Unavailable: true}
	}

	at, found, err := p.stations.Station(stationID)
	if err != nil || !found {
		// Unknown, or unreadable. Either way this is not a Station Core recorded, and a leaf
		// signed by an unknown key is exactly what the registry exists to refuse.
		return inv.Registration{}
	}

	key, kerr := hex.DecodeString(strings.TrimSpace(at.AssertionKey))
	if kerr != nil || len(key) != ed25519.PublicKeySize {
		// A stored key we cannot parse is not a key. Reporting Known with a nil key would
		// hand towerinv something that can never verify and produce a confusing refusal.
		return inv.Registration{}
	}

	ownerBanned, ok := p.isBanned(at.Owner)
	if !ok {
		return inv.Registration{Unavailable: true}
	}

	reg := inv.Registration{
		Known:      true,
		Key:        ed25519.PublicKey(key),
		Banned:     banned || ownerBanned,
		KeyRevoked: at.State == attach.StateRevoked,
		// Quarantine is the state a Station is ADMITTED into, so this is the common case for
		// anything new rather than an edge. It is reported separately from Banned because the
		// operator has done nothing wrong and the message they see should say so.
		Quarantined: at.State == attach.StateQuarantine,
	}

	// Detached and DORMANT are not revoked - the key is not burnt - but neither is serving, so
	// neither may be routable. Reporting them banned is the honest mapping onto the fields
	// towerinv has.
	//
	// Dormant is listed explicitly rather than being folded into a !Live() test, because the two
	// mean different things everywhere else in this system - one is recoverable and one is not -
	// and a reader arriving here needs to see that the difference makes no difference TO
	// ROUTING. A sleeping Station carries no work; that it can wake up is somebody else's
	// question.
	if at.State == attach.StateDetached || at.State == attach.StateDormant {
		reg.Banned = true
	}

	if p.owners == nil {
		return reg // no owner source configured: OwnerPresent stays false, so nothing routes
	}
	owner, ofound, oerr := p.owners.OwnerByPubkey(at.Owner)
	if oerr != nil {
		// Unreadable owner: refuse rather than assume present. An account we cannot check is
		// an account whose suspension we cannot see.
		return reg
	}
	reg.OwnerPresent = ofound
	reg.OwnerSuspended = ofound && owner.Suspended
	return reg
}

// ModelAllowed reports whether Core routes this model publicly at all.
func (p *Policy) ModelAllowed(model string) bool {
	if p.cfg.ModelAllowed == nil {
		return false
	}
	return p.cfg.ModelAllowed(model)
}

// ModalityAllowed is the same question for a modality.
func (p *Policy) ModalityAllowed(modality string) bool {
	if p.cfg.ModalityAllowed == nil {
		return false
	}
	return p.cfg.ModalityAllowed(modality)
}

// PriceBand is the public floor and ceiling, in micro-USD per 1,000,000 tokens.
func (p *Policy) PriceBand(model string) (int64, int64, bool) {
	if p.cfg.PriceBand == nil {
		return 0, 0, false
	}
	floor, ceiling, ok := p.cfg.PriceBand(model)
	if !ok || floor < 0 || ceiling < floor {
		// An incoherent band is a misconfiguration, and a misconfigured band must not admit
		// an offer at any price.
		return 0, 0, false
	}
	return floor, ceiling, true
}

// isBanned answers from the cached set, refreshing when stale. The second return is false
// when the set could not be loaded at all - the caller then refuses.
func (p *Policy) isBanned(id string) (bool, bool) {
	if id == "" {
		return false, true
	}
	p.mu.RLock()
	now := p.cfg.Now()
	if !p.loadFailed && now.Sub(p.loadedAt) < p.cfg.BanRefresh {
		b := p.banned[id]
		p.mu.RUnlock()
		return b, true
	}
	// Failed recently: keep refusing, but do NOT hammer the store once per leaf.
	if p.loadFailed && !p.failedAt.IsZero() && now.Sub(p.failedAt) < p.cfg.BanRefresh {
		p.mu.RUnlock()
		return false, false
	}
	p.mu.RUnlock()
	return p.refreshAndCheck(id)
}

func (p *Policy) refreshAndCheck(id string) (bool, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Another goroutine may have refreshed - or failed - while we waited for the write lock.
	now := p.cfg.Now()
	if !p.loadFailed && now.Sub(p.loadedAt) < p.cfg.BanRefresh {
		return p.banned[id], true
	}
	if p.loadFailed && !p.failedAt.IsZero() && now.Sub(p.failedAt) < p.cfg.BanRefresh {
		return false, false
	}
	if p.bans == nil {
		// No ban source configured. That is a misconfiguration, not an empty ban list, and it
		// must not read as "nobody is banned".
		p.loadFailed, p.failedAt = true, now
		return false, false
	}

	owners, oerr := p.bans.BannedOwners()
	nodes, nerr := p.bans.BannedNodes()
	if oerr != nil || nerr != nil {
		// Keep whatever we had, but mark the load failed so callers refuse. A ban set that is
		// merely STALE would be tolerable; one we have never successfully loaded is not, and
		// distinguishing them here would be a subtlety with a catastrophic failure mode.
		p.loadFailed, p.failedAt = true, now
		return false, false
	}

	set := make(map[string]bool, len(owners)+len(nodes))
	for k := range owners {
		set[k] = true
	}
	for k := range nodes {
		set[k] = true
	}
	p.banned, p.loadedAt, p.loadFailed, p.failedAt = set, now, false, time.Time{}
	return set[id], true
}

// Invalidate drops the cached ban set so the next read reloads it. Called when a ban is
// made, so a revocation does not wait out the refresh interval.
func (p *Policy) Invalidate() {
	p.mu.Lock()
	defer p.mu.Unlock()
	// Clear the failure pacing too: an operator making a ban is a reason to try the store
	// again immediately, whatever it did last time.
	p.loadedAt, p.failedAt = time.Time{}, time.Time{}
}
