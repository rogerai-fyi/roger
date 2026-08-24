// Package origin is the Tower traffic-origin tally: how many attempts were routed to each
// Tower from each country, for the admin detail view's "where does this Tower's demand come
// from" block.
//
// It is COARSE and PRIVACY-PRESERVING BY CONSTRUCTION. The only origin it records is the
// 2-letter ISO country Cloudflare already hands Core on the inbound request (CF-IPCountry) -
// never an IP, never the consumer account, wallet, or pubkey. An attempt with no country
// header (a dev path, a non-CF hop) is counted as "unknown" rather than dropped or guessed.
//
// The privacy is STRUCTURAL, not merely a matter of what the view chooses to render: no
// stored record ever carries an attempt id BESIDE a country. Idempotency (a retried
// attempt-open counts once) is tracked by the attempt id ALONE, with no country or Tower
// beside it; the country is stored under a surrogate id with NO attempt id. So there is
// nothing in the store to join a consumer - reachable through the attempt id the billing
// ledger keys - to where their request came from. The view answers "how much from where"
// and the schema itself cannot answer "who". The mem store keeps the same separation.
//
// The durable store is shared across instances, so the tally is fleet-wide - the same union
// the routing fabric and the other Tower ledgers read.
package origin

import (
	"sort"
	"strings"
	"sync"
	"time"
)

// Unknown is the country bucket for an attempt that arrived with no country header.
const Unknown = "unknown"

// Tally is one country's attempt count for a Tower over a window.
type Tally struct {
	Country  string
	Attempts int
}

// Store records and reads the per-Tower, per-country attempt tally.
type Store interface {
	// Record counts one attempt routed to a Tower from a country. Idempotent on attemptID: a
	// retried open counts once. An empty country is stored as Unknown. An empty towerID or
	// attemptID is a no-op (nothing to attribute).
	Record(towerID, attemptID, country string, at time.Time) error
	// ByTower returns the per-country tally for a Tower over a window (a zero `since` is
	// all-time), sorted by country.
	ByTower(towerID string, since time.Time) ([]Tally, error)
}

// normCountry folds a raw header value to the stored bucket: upper-case ISO code, or Unknown
// when absent. Cloudflare uses "XX" / "T1" for unresolved and Tor; those are kept verbatim
// (they are still "where", coarsely) rather than merged into Unknown, which means "no header".
func normCountry(c string) string {
	c = strings.ToUpper(strings.TrimSpace(c))
	if c == "" {
		return Unknown
	}
	return c
}

func sortTallies(t []Tally) {
	sort.Slice(t, func(i, j int) bool { return t[i].Country < t[j].Country })
}

type memStore struct {
	mu   sync.Mutex
	seen map[string]struct{} // attempt id -> counted (idempotency)
	rows map[string][]memRow // tower id -> rows
}

type memRow struct {
	country string
	at      time.Time
}

// NewMemStore builds the in-process origin tally.
func NewMemStore() Store {
	return &memStore{seen: map[string]struct{}{}, rows: map[string][]memRow{}}
}

func (m *memStore) Record(towerID, attemptID, country string, at time.Time) error {
	if towerID == "" || attemptID == "" {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, done := m.seen[attemptID]; done {
		return nil
	}
	m.seen[attemptID] = struct{}{}
	m.rows[towerID] = append(m.rows[towerID], memRow{country: normCountry(country), at: at})
	return nil
}

func (m *memStore) ByTower(towerID string, since time.Time) ([]Tally, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	all := since.IsZero()
	byCountry := map[string]int{}
	for _, r := range m.rows[towerID] {
		if all || !r.at.Before(since) {
			byCountry[r.country]++
		}
	}
	out := make([]Tally, 0, len(byCountry))
	for c, n := range byCountry {
		out = append(out, Tally{Country: c, Attempts: n})
	}
	sortTallies(out)
	return out, nil
}
