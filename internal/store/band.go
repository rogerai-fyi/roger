package store

import (
	"errors"
	"sync"
	"time"

	"rogerai.fm/roger/v5/internal/protocol"
)

// Band is an owner-issued PRIVATE channel: a "frequency code" that makes a node
// reachable only to whoever knows the code, while staying hidden from the public
// /discover + /market views. It is the discovery analogue of a Grant (a grant is
// a private ACCESS key; a band is private DISCOVERY visibility). See BANDS-DESIGN.
//
// The user-facing code is a cosmetic dotted-decimal frequency plus a secret tail,
// e.g. "147.520 MHz · 8F3K-9M2Q". ONLY the 8-char Crockford-base32 tail is the
// secret: it is stored as sha256(canonical tail) in CodeHash and is NEVER stored
// or logged in the clear (the full code is shown ONCE at mint and is not retrievable
// again - lost => revoke + re-mint). CodeDisplay is the MASKED cosmetic display
// ("147.520 MHz · ••••-••••") for re-display on the owner's dashboard; it is NOT secret
// and is NON-RECOVERABLE (CanonicalBandTail can never extract a tail from it), so the
// band cannot be reconstructed from persisted state.
type Band struct {
	ID          string   `json:"id"`           // "band_<rand>" - the DB id (NOT the secret)
	CodeHash    string   `json:"-"`            // sha256(canonical secret tail); the code is shown once at mint
	CodeDisplay string   `json:"code_display"` // MASKED cosmetic "147.520 MHz · ••••-••••" (NOT secret; non-recoverable)
	Owner       string   `json:"owner"`        // issuing owner pubkey (store.Owner.Pubkey)
	Label       string   `json:"label"`        // optional human label ("friends", "self:hermes-box")
	NodeID      string   `json:"node_id"`      // the private node this band routes to
	Models      []string `json:"models"`       // allowed models; empty = any model the node offers
	ExpiresAt   int64    `json:"expires_at"`   // unix; 0 = never (Phase 1 is always 0; Phase 2 packs add expiry)
	Revoked     bool     `json:"revoked"`
	CreatedAt   int64    `json:"created_at"`
}

// BandPatch is the editable part of a private band. Nil means "leave unchanged"; an
// explicit empty Label clears the human label. NodeID and Label are updated atomically so
// a refused move can never leave half of the requested patch behind.
type BandPatch struct {
	NodeID *string `json:"node_id,omitempty"`
	Label  *string `json:"label,omitempty"`
}

func (b Band) applyPatch(p BandPatch) Band {
	if p.NodeID != nil {
		b.NodeID = *p.NodeID
	}
	if p.Label != nil {
		b.Label = *p.Label
	}
	return b
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

// ErrBandNodeOccupied is returned by MoveBand when the destination node already carries a
// live band. It is a distinct sentinel (not a bare false) because the remedy is specific
// and worth telling the operator: that node already has its own band, so move THAT one or
// revoke it first. Callers map it to a 409.
var ErrBandNodeOccupied = errors.New("that model already carries its own private band")

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
}

func newBandStore() *bandStore {
	return &bandStore{bands: map[string]Band{}, byHash: map[string]string{}}
}

func (m *Mem) CreateBand(b Band) error {
	m.bs.mu.Lock()
	defer m.bs.mu.Unlock()
	if b.NodeID != "" && !b.Revoked {
		for _, occupant := range m.bs.bands {
			if occupant.NodeID == b.NodeID && !occupant.Revoked {
				return ErrBandNodeOccupied
			}
		}
	}
	if b.CreatedAt == 0 {
		b.CreatedAt = time.Now().Unix()
	}
	m.bs.bands[b.ID] = b
	m.bs.byHash[b.CodeHash] = b.ID
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
	// A node may retain revoked history. Match Postgres: prefer a live row, then the
	// newest row within the same state. Scanning is intentional: a node can retain multiple
	// revoked history rows beside its one live binding.
	var best Band
	found := false
	for _, b := range m.bs.bands {
		if b.NodeID != nodeID {
			continue
		}
		if !found || (best.Revoked && !b.Revoked) || best.Revoked == b.Revoked && b.CreatedAt > best.CreatedAt {
			best, found = b, true
		}
	}
	return best, found, nil
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
	if !revoked && b.Revoked && b.NodeID != "" {
		for otherID, occupant := range m.bs.bands {
			if otherID != id && occupant.NodeID == b.NodeID && !occupant.Revoked {
				return false, ErrBandNodeOccupied
			}
		}
	}
	b.Revoked = revoked
	m.bs.bands[id] = b
	return true, nil
}

// UpdateBand applies the owner-editable label and node binding atomically. A label-only
// patch may annotate revoked history; a patch that moves a revoked band is refused because
// moving it would resurrect a burnt code at a new node.
func (m *Mem) UpdateBand(id, owner string, patch BandPatch) (Band, bool, error) {
	m.bs.mu.Lock()
	defer m.bs.mu.Unlock()
	b, ok := m.bs.bands[id]
	if !ok || b.Owner != owner {
		return Band{}, false, nil
	}
	if patch.NodeID != nil {
		if b.Revoked {
			return Band{}, false, nil
		}
		if *patch.NodeID != b.NodeID {
			for otherID, occupant := range m.bs.bands {
				if otherID != id && occupant.NodeID == *patch.NodeID && !occupant.Revoked {
					return Band{}, false, ErrBandNodeOccupied
				}
			}
		}
	}
	b = b.applyPatch(patch)
	m.bs.bands[id] = b
	return b, true, nil
}

// MoveBand rebinds a LIVE band to a different node, owner-scoped. It is the only write
// path Band.NodeID has ever had: until now NodeID was set once at CreateBand, and since a
// node id is "<station>-<model>" that meant a band was hard-bound to ONE model for life.
// Moving it is what lets an owner point their band at a different model WITHOUT rotating
// the secret - the code, its hash and its display are untouched, so everyone already tuned
// in keeps working.
//
// It reports whether the band moved. It refuses (false, nil) an unknown id, another
// owner's band, and a REVOKED band - whose code is permanently burnt, so moving it would
// resurrect a dead code at a new node. Moving onto a node that already carries a LIVE band
// returns ErrBandNodeOccupied: a node carries at most one band, and silently displacing
// the other one would take a station off air its owner never touched. Moving a band to the
// node it already sits on is an idempotent success, so a retried request is not an error.
func (m *Mem) MoveBand(id, owner, nodeID string) (bool, error) {
	_, ok, err := m.UpdateBand(id, owner, BandPatch{NodeID: &nodeID})
	return ok, err
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

// RemaskBandDisplays re-masks every persisted band's CodeDisplay into the
// NON-RECOVERABLE cosmetic form (protocol.MaskBandDisplay), so a band minted before the
// display was masked at the source can no longer reconstruct/resolve from stored state.
// The CodeHash (the resolve lookup key) and the byHash index are left UNTOUCHED, so the
// owner's one-time full code still resolves; ONLY the display changes. Returns how many
// rows it actually changed; IDEMPOTENT (an already-masked display is skipped, so a re-run
// changes 0).
func (m *Mem) RemaskBandDisplays() (int, error) {
	m.bs.mu.Lock()
	defer m.bs.mu.Unlock()
	n := 0
	for id, b := range m.bs.bands {
		masked := protocol.MaskBandDisplay(b.CodeDisplay)
		if masked == b.CodeDisplay {
			continue
		}
		b.CodeDisplay = masked
		m.bs.bands[id] = b
		n++
	}
	return n, nil
}
