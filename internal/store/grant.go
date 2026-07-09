package store

import (
	"sync"
	"time"
)

// Grant is an owner-issued private access key: a labeled bearer credential
// (rog-grant_<secret>) a grantee sets as their API key and uses with no login,
// no account, no wallet. The broker authenticates the grant by its secret hash,
// resolves it to the issuing owner, routes only to that owner's nodes at the
// grant's price (free = 0/0), and enforces the grant's caps. See
// docs-internal/GRANT-KEYS-DESIGN.md. The secret itself is shown ONCE at create;
// only its sha256 is ever stored.
type Grant struct {
	ID         string   `json:"id"`          // "grant_<rand>" - the DB id (NOT the secret)
	SecretHash string   `json:"-"`           // sha256(secret); the secret is shown once at create
	Owner      string   `json:"owner"`       // issuing owner pubkey (store.Owner.Pubkey)
	Label      string   `json:"label"`       // "petlings", "friend-jane", "self:hermes-box"
	Nodes      []string `json:"nodes"`       // allowed node ids; empty = ALL of this owner's nodes
	Models     []string `json:"models"`      // allowed models; empty = any model the nodes offer
	Free       bool     `json:"free"`        // true => price 0/0 (skips wallet debit entirely)
	PriceIn    float64  `json:"price_in"`    // custom/discounted $/1M in (ignored when Free)
	PriceOut   float64  `json:"price_out"`   // custom/discounted $/1M out (ignored when Free)
	RPM        float64  `json:"rpm"`         // rate-limit sustained req/min (0 = broker default)
	Burst      float64  `json:"burst"`       // rate-limit bucket depth (0 = broker default)
	DailyCap   int64    `json:"daily_cap"`   // max tokens/UTC-day (0 = unlimited)
	MonthlyCap int64    `json:"monthly_cap"` // max tokens/UTC-month (0 = unlimited)
	Self       bool     `json:"self"`        // a --self grant (owner's own boxes/agents; always $0)
	ExpiresAt  int64    `json:"expires_at"`  // unix; 0 = never
	Revoked    bool     `json:"revoked"`
	CreatedAt  int64    `json:"created_at"`
}

// GrantUsage is a per-grant rollup row used by the daily/monthly cap check and the
// dashboard. Tokens are prompt+completion summed for the UTC window.
type GrantUsage struct {
	DayTokens   int64 `json:"day_tokens"`   // tokens served in the current UTC day
	MonthTokens int64 `json:"month_tokens"` // tokens served in the current UTC month
}

// GrantPatch is the set of editable grant fields (PATCH /grants/{id}). A nil
// pointer field means "leave unchanged"; this lets an owner toggle revoked or
// adjust caps/price/scope without resending the whole grant.
type GrantPatch struct {
	Label      *string   `json:"label,omitempty"`
	Nodes      *[]string `json:"nodes,omitempty"`
	Models     *[]string `json:"models,omitempty"`
	Free       *bool     `json:"free,omitempty"`
	PriceIn    *float64  `json:"price_in,omitempty"`
	PriceOut   *float64  `json:"price_out,omitempty"`
	RPM        *float64  `json:"rpm,omitempty"`
	Burst      *float64  `json:"burst,omitempty"`
	DailyCap   *int64    `json:"daily_cap,omitempty"`
	MonthlyCap *int64    `json:"monthly_cap,omitempty"`
	ExpiresAt  *int64    `json:"expires_at,omitempty"`
	Revoked    *bool     `json:"revoked,omitempty"`
}

// Expired reports whether the grant has passed its expiry (0 = never).
func (g Grant) Expired(now time.Time) bool {
	return g.ExpiresAt != 0 && now.Unix() >= g.ExpiresAt
}

// GrantPrice returns the price the grant bills at: 0/0 for a free or self grant,
// else its custom (PriceIn, PriceOut). A negative stored price is clamped to 0 here -
// the billing chokepoint every settle path reads - so even a legacy/corrupt negative
// row can never yield a negative cost (which Finalize would turn into a minted credit).
// The HTTP create/edit paths reject a negative price outright; this is defense in depth.
func (g Grant) GrantPrice() (in, out float64) {
	if g.Free || g.Self {
		return 0, 0
	}
	in, out = g.PriceIn, g.PriceOut
	if in < 0 {
		in = 0
	}
	if out < 0 {
		out = 0
	}
	return in, out
}

// applyPatch returns g with the non-nil patch fields applied.
func (g Grant) applyPatch(p GrantPatch) Grant {
	if p.Label != nil {
		g.Label = *p.Label
	}
	if p.Nodes != nil {
		g.Nodes = *p.Nodes
	}
	if p.Models != nil {
		g.Models = *p.Models
	}
	if p.Free != nil {
		g.Free = *p.Free
	}
	if p.PriceIn != nil {
		g.PriceIn = *p.PriceIn
	}
	if p.PriceOut != nil {
		g.PriceOut = *p.PriceOut
	}
	if p.RPM != nil {
		g.RPM = *p.RPM
	}
	if p.Burst != nil {
		g.Burst = *p.Burst
	}
	if p.DailyCap != nil {
		g.DailyCap = *p.DailyCap
	}
	if p.MonthlyCap != nil {
		g.MonthlyCap = *p.MonthlyCap
	}
	if p.ExpiresAt != nil {
		g.ExpiresAt = *p.ExpiresAt
	}
	if p.Revoked != nil {
		g.Revoked = *p.Revoked
	}
	return g
}

// dayKey / monthKey are the UTC-window keys for the grant usage rollup.
func dayKey(t time.Time) string   { return t.UTC().Format("2006-01-02") }
func monthKey(t time.Time) string { return t.UTC().Format("2006-01") }

// --- Mem grant storage ---------------------------------------------------
//
// A second small map set on Mem, mirroring owners/nodeAcct. Guarded by its own
// mutex so grant ops never contend with the wallet/ledger lock.

type grantStore struct {
	mu       sync.Mutex
	grants   map[string]Grant  // id -> grant
	bySecret map[string]string // secret_hash -> id
	dayUsage map[string]int64  // "id|YYYY-MM-DD" -> tokens
	monUsage map[string]int64  // "id|YYYY-MM" -> tokens
}

func newGrantStore() *grantStore {
	return &grantStore{
		grants: map[string]Grant{}, bySecret: map[string]string{},
		dayUsage: map[string]int64{}, monUsage: map[string]int64{},
	}
}

func (m *Mem) CreateGrant(g Grant) error {
	m.gs.mu.Lock()
	defer m.gs.mu.Unlock()
	if g.CreatedAt == 0 {
		g.CreatedAt = time.Now().Unix()
	}
	m.gs.grants[g.ID] = g
	m.gs.bySecret[g.SecretHash] = g.ID
	return nil
}

func (m *Mem) GrantBySecretHash(hash string) (Grant, bool, error) {
	m.gs.mu.Lock()
	defer m.gs.mu.Unlock()
	id, ok := m.gs.bySecret[hash]
	if !ok {
		return Grant{}, false, nil
	}
	g, ok := m.gs.grants[id]
	return g, ok, nil
}

func (m *Mem) GrantsByOwner(owner string) ([]Grant, error) {
	m.gs.mu.Lock()
	defer m.gs.mu.Unlock()
	var out []Grant
	for _, g := range m.gs.grants {
		if g.Owner == owner {
			out = append(out, g)
		}
	}
	return out, nil
}

func (m *Mem) SetGrantRevoked(id, owner string, revoked bool) (bool, error) {
	m.gs.mu.Lock()
	defer m.gs.mu.Unlock()
	g, ok := m.gs.grants[id]
	if !ok || g.Owner != owner { // owner-scoped: never touch another owner's grant
		return false, nil
	}
	g.Revoked = revoked
	m.gs.grants[id] = g
	return true, nil
}

func (m *Mem) UpdateGrant(id, owner string, patch GrantPatch) (Grant, bool, error) {
	m.gs.mu.Lock()
	defer m.gs.mu.Unlock()
	g, ok := m.gs.grants[id]
	if !ok || g.Owner != owner {
		return Grant{}, false, nil
	}
	g = g.applyPatch(patch)
	m.gs.grants[id] = g
	return g, true, nil
}

func (m *Mem) GrantUsageOf(id string, now time.Time) (GrantUsage, error) {
	m.gs.mu.Lock()
	defer m.gs.mu.Unlock()
	return GrantUsage{
		DayTokens:   m.gs.dayUsage[id+"|"+dayKey(now)],
		MonthTokens: m.gs.monUsage[id+"|"+monthKey(now)],
	}, nil
}

func (m *Mem) AddGrantUsage(id string, tokens int64, now time.Time) error {
	m.gs.mu.Lock()
	defer m.gs.mu.Unlock()
	m.gs.dayUsage[id+"|"+dayKey(now)] += tokens
	m.gs.monUsage[id+"|"+monthKey(now)] += tokens
	return nil
}
