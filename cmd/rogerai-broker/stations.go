package main

// stations.go serves the operator's own station list: one account-bound roll-up of
// everything they run, so an operator does not have to stitch /earnings, /strikes and
// the on-air registry together per node.
//
// Scope discipline (features/operator/stations_dashboard.feature): the response is
// derived from the AUTHENTICATED owner's node bindings only, and deliberately carries
// no consumer identity, prompt or completion text, bridge token, or private band code.

import (
	"net/http"
	"sort"
	"time"

	"rogerai.fm/roger/v5/internal/protocol"
	"rogerai.fm/roger/v5/internal/store"
)

// stationOffer is the public shape of one model a station serves. It is a deliberate
// subset of protocol.ModelOffer: pricing and capability, never the bridge token.
type stationOffer struct {
	Model    string   `json:"model"`
	Modality string   `json:"modality,omitempty"`
	PriceIn  float64  `json:"price_in"`
	PriceOut float64  `json:"price_out"`
	Ctx      int      `json:"ctx,omitempty"`
	Caps     []string `json:"capabilities,omitempty"`
}

// stationView is one station as its owner sees it.
type stationView struct {
	NodeID       string         `json:"node_id"`
	OnAir        bool           `json:"on_air"`
	RegisteredAt int64          `json:"registered_at,omitempty"`
	LastSeen     int64          `json:"last_seen,omitempty"`
	Region       string         `json:"region,omitempty"`
	HW           string         `json:"hw,omitempty"`
	Confidential bool           `json:"confidential"`
	Private      bool           `json:"private"`
	Offers       []stationOffer `json:"offers"`
	Earnings     float64        `json:"earnings"`
	// EarningsUnavailable is set when the ledger could not be read. A money dashboard
	// must never render a fabricated 0 that an operator would read as "I earned nothing".
	EarningsUnavailable bool `json:"earnings_unavailable,omitempty"`
	// RecentServed is a WINDOW, not a lifetime total: it counts the most recent entries
	// only, so it saturates. Named for what it is rather than implying a total.
	RecentServed int               `json:"recent_served"`
	Chain        store.ChainStatus `json:"chain"`
	ChainState   string            `json:"chain_state"` // unknown | continuous | breaks-recorded
}

// stations handles GET /stations: the authenticated owner's own station roll-up.
func (b *broker) stations(w http.ResponseWriter, r *http.Request) {
	if corsCredsPreflight(w, r) {
		return
	}
	if !allow(w, r, http.MethodGet) {
		return
	}
	corsCreds(w, r)

	login, o, ok := b.payoutOwner(r, nil)
	if !ok {
		jsonErr(w, http.StatusUnauthorized, "not logged in - run `roger login` to view your stations")
		return
	}
	if o.GitHubID == 0 {
		jsonErr(w, http.StatusForbidden, "no operator account for this login")
		return
	}

	// The owner's OWN nodes, by account binding. This is the whole authorization
	// story: node ids are public, so the list must never be assembled from a
	// request-supplied id.
	ids, err := b.db.NodesOfAccount(o.Pubkey)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "could not read your stations")
		return
	}
	sort.Strings(ids)

	// One durable read for the whole list rather than one per station: registered-at
	// and the confidential grant survive a broker restart, the in-memory registry does
	// not.
	recs := map[string]store.NodeRecord{}
	if all, err := b.db.AllNodes(); err == nil {
		for _, rec := range all {
			recs[rec.NodeID] = rec
		}
	}

	out := make([]stationView, 0, len(ids))
	for _, id := range ids {
		out = append(out, b.stationView(id, recs[id]))
	}

	strikes, _ := b.db.StrikesByOwner(o.Pubkey, 50)
	if strikes == nil {
		strikes = []store.Strike{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"github_login": login,
		"stations":     out,
		"strikes":      strikes,
	})
}

// stationView assembles one station's public-to-its-owner view.
func (b *broker) stationView(id string, rec store.NodeRecord) stationView {
	v := stationView{NodeID: id, Offers: []stationOffer{}}

	b.mu.Lock()
	reg, registered := b.nodes[id]
	lastSeen := b.lastSeen[id]
	b.mu.Unlock()

	if !lastSeen.IsZero() {
		v.LastSeen = lastSeen.Unix()
		v.OnAir = time.Since(lastSeen) < nodeTTL
	}
	if registered {
		v.Region, v.HW, v.Private = reg.Region, reg.HW, reg.Private
		v.Offers = publicOffers(reg.Offers)
	}
	// The durable record carries registered-at and the confidential grant, which the
	// in-memory registry loses across a restart.
	if rec.NodeID != "" {
		v.RegisteredAt = rec.RegisteredAt
		v.Confidential = rec.Confidential
		if !registered {
			v.Offers = publicOffers(rec.Reg.Offers)
			v.Region, v.HW, v.Private = rec.Reg.Region, rec.Reg.HW, rec.Reg.Private
		}
	}

	if earned, err := b.db.EarningsOf(id); err == nil {
		v.Earnings = round6(earned)
	} else {
		v.EarningsUnavailable = true
	}
	if recent, err := b.db.RecentByNode(id, recentWindow); err == nil {
		v.RecentServed = len(recent)
	}

	if st, err := b.db.ChainStatus(id); err == nil {
		v.Chain = st
		v.ChainState = chainState(st)
	} else {
		v.ChainState = "unknown"
	}
	return v
}

// chainState labels the chain for display. A station the broker has never recorded a
// receipt from is "unknown", NOT broken - it has simply not served yet.
func chainState(st store.ChainStatus) string {
	switch {
	case st.CheckedAt == 0 && st.Head == "":
		return "unknown"
	case st.Breaks > 0:
		return "breaks-recorded"
	default:
		return "continuous"
	}
}

// publicOffers strips every offer field an owner does not need and a response must
// never carry - above all the bridge token.
func publicOffers(offers []protocol.ModelOffer) []stationOffer {
	out := make([]stationOffer, 0, len(offers))
	for _, o := range offers {
		out = append(out, stationOffer{
			Model:    o.Model,
			Modality: o.Modality,
			PriceIn:  o.PriceIn,
			PriceOut: o.PriceOut,
			Ctx:      o.Ctx,
			Caps:     o.Capabilities,
		})
	}
	return out
}

// recentWindow bounds the per-station activity read. It is a window, not a total.
const recentWindow = 100
