package store

import (
	"github.com/rogerai-fyi/roger/internal/protocol"
)

// OfferOverride is an owner-authored price + time-of-use schedule the OWNER set from
// the web Console for one (node, model). It is the EFFECTIVE PUBLISHED price: at
// register time the broker SEEDS the matching node offer from it (so the owner's
// web-set price survives node re-registration AND a broker restart), and ActivePrice
// reads it at serve time. It only ever records a FUTURE/published price - it never
// touches a past UsageReceipt or any ledger row (those are immutable, settled at the
// price quoted at the moment they were served).
type OfferOverride struct {
	Owner     string                 `json:"owner"`    // owner pubkey - the scope key (an override never shadows another account's node)
	NodeID    string                 `json:"node_id"`  // the served node
	Model     string                 `json:"model"`    // the model on that node
	PriceIn   float64                `json:"price_in"` // base/fallback published input price (credits/1M tokens)
	PriceOut  float64                `json:"price_out"`
	Schedule  []protocol.PriceWindow `json:"schedule,omitempty"` // time-of-use windows (first match wins; Free zeroes the price)
	UpdatedAt int64                  `json:"updated_at"`
}

// overrideKey is the (node,model) map key (a NUL separator can't appear in either id).
func overrideKey(node, model string) string { return node + "\x00" + model }

func (m *Mem) SetOfferOverride(ov OfferOverride) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.overrides == nil {
		m.overrides = map[string]OfferOverride{}
	}
	m.overrides[overrideKey(ov.NodeID, ov.Model)] = ov
	return nil
}

func (m *Mem) OfferOverride(node, model string) (OfferOverride, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ov, ok := m.overrides[overrideKey(node, model)]
	return ov, ok, nil
}

func (m *Mem) OverridesByOwner(owner string) ([]OfferOverride, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []OfferOverride
	for _, ov := range m.overrides {
		if ov.Owner == owner {
			out = append(out, ov)
		}
	}
	return out, nil
}

func (m *Mem) ClearOfferOverride(owner, node, model string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := overrideKey(node, model)
	ov, ok := m.overrides[k]
	if !ok || ov.Owner != owner { // owner-scoped: never clear another account's override
		return false, nil
	}
	delete(m.overrides, k)
	return true, nil
}
