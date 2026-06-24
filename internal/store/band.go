package store

import (
	"sync"
	"time"
)

// Band is an owner-issued PRIVATE channel: a "frequency code" that makes a node
// reachable only to whoever knows the code, while staying hidden from the public
// /discover + /market views. It is the discovery analogue of a Grant (a grant is
// a private ACCESS key; a band is private DISCOVERY visibility). See BANDS-DESIGN.
//
// The user-facing code is a cosmetic dotted-decimal frequency plus a secret tail,
// e.g. "147.520 MHz · 8F3K-9M2Q". ONLY the 8-char Crockford-base32 tail is the
// secret: it is stored as sha256(canonical tail) in CodeHash and is NEVER stored
// or logged in the clear (shown ONCE at mint). CodeDisplay is the cosmetic full
// string for re-display on the owner's own dashboard; it is NOT secret and is NEVER
// folded into the lookup key (resolve hashes the tail alone).
type Band struct {
	ID          string   `json:"id"`           // "band_<rand>" - the DB id (NOT the secret)
	CodeHash    string   `json:"-"`            // sha256(canonical secret tail); the code is shown once at mint
	CodeDisplay string   `json:"code_display"` // cosmetic "147.520 MHz · 8F3K-9M2Q" (NOT secret; safe to log/show owner)
	Owner       string   `json:"owner"`        // issuing owner pubkey (store.Owner.Pubkey)
	Label       string   `json:"label"`        // optional human label ("friends", "self:hermes-box")
	NodeID      string   `json:"node_id"`      // the private node this band routes to
	Models      []string `json:"models"`       // allowed models; empty = any model the node offers
	ExpiresAt   int64    `json:"expires_at"`   // unix; 0 = never (Phase 1 is always 0; Phase 2 packs add expiry)
	Revoked     bool     `json:"revoked"`
	CreatedAt   int64    `json:"created_at"`
}

// Expired reports whether the band has passed its expiry (0 = never).
func (b Band) Expired(now time.Time) bool {
	return b.ExpiresAt != 0 && now.Unix() >= b.ExpiresAt
}

// Active reports whether the band is live (not revoked, not expired) as of now.
func (b Band) Active(now time.Time) bool {
	return !b.Revoked && !b.Expired(now)
}

// modelDenied reports whether the band restricts models and `model` is not allowed.
func (b Band) ModelDenied(model string) bool {
	if len(b.Models) == 0 {
		return false // empty = any model the node offers
	}
	for _, m := range b.Models {
		if m == model {
			return false
		}
	}
	return true
}

// BandQuota is the number of ACTIVE private bands an owner may hold for free.
// Phase 1 is a flat 1; Phase 2 ($5 packs) adds purchased slots here (owner-keyed),
// and the CountActiveBands cap check at register slots straight in unchanged.
func BandQuota(owner string) int {
	_ = owner
	return 1
}

// --- Mem band storage ----------------------------------------------------
//
// A small map set on Mem, mirroring the grantStore: its own mutex so band ops
// never contend with the wallet/ledger lock or the grant lock.

type bandStore struct {
	mu     sync.Mutex
	bands  map[string]Band   // id -> band
	byHash map[string]string // code_hash -> id (the resolve lookup)
	byNode map[string]string // node_id -> id (idempotent re-register: one band per node)
}

func newBandStore() *bandStore {
	return &bandStore{
		bands: map[string]Band{}, byHash: map[string]string{}, byNode: map[string]string{},
	}
}

func (m *Mem) CreateBand(b Band) error {
	m.bs.mu.Lock()
	defer m.bs.mu.Unlock()
	if b.CreatedAt == 0 {
		b.CreatedAt = time.Now().Unix()
	}
	m.bs.bands[b.ID] = b
	m.bs.byHash[b.CodeHash] = b.ID
	if b.NodeID != "" {
		m.bs.byNode[b.NodeID] = b.ID
	}
	return nil
}

func (m *Mem) BandByCodeHash(hash string) (Band, bool, error) {
	m.bs.mu.Lock()
	defer m.bs.mu.Unlock()
	id, ok := m.bs.byHash[hash]
	if !ok {
		return Band{}, false, nil
	}
	b, ok := m.bs.bands[id]
	return b, ok, nil
}

func (m *Mem) BandByNode(nodeID string) (Band, bool, error) {
	m.bs.mu.Lock()
	defer m.bs.mu.Unlock()
	id, ok := m.bs.byNode[nodeID]
	if !ok {
		return Band{}, false, nil
	}
	b, ok := m.bs.bands[id]
	return b, ok, nil
}

func (m *Mem) BandsByOwner(owner string) ([]Band, error) {
	m.bs.mu.Lock()
	defer m.bs.mu.Unlock()
	var out []Band
	for _, b := range m.bs.bands {
		if b.Owner == owner {
			out = append(out, b)
		}
	}
	return out, nil
}

func (m *Mem) SetBandRevoked(id, owner string, revoked bool) (bool, error) {
	m.bs.mu.Lock()
	defer m.bs.mu.Unlock()
	b, ok := m.bs.bands[id]
	if !ok || b.Owner != owner { // owner-scoped: never touch another owner's band
		return false, nil
	}
	b.Revoked = revoked
	m.bs.bands[id] = b
	return true, nil
}

// CountActiveBands counts an owner's non-revoked, non-expired bands as of now -
// the free-cap enforcement point (compared against BandQuota at register).
func (m *Mem) CountActiveBands(owner string, now time.Time) (int, error) {
	m.bs.mu.Lock()
	defer m.bs.mu.Unlock()
	n := 0
	for _, b := range m.bs.bands {
		if b.Owner == owner && b.Active(now) {
			n++
		}
	}
	return n, nil
}
