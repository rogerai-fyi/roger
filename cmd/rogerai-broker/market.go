package main

import (
	"net/http"
	"sort"
	"time"
)

type offerView struct {
	NodeID       string  `json:"node_id"`
	Region       string  `json:"region"`
	HW           string  `json:"hw"`
	Model        string  `json:"model"`
	In           float64 `json:"price_in"`  // active (time-of-use) price right now
	Out          float64 `json:"price_out"` // active price right now
	Ctx          int     `json:"ctx"`
	Online       bool    `json:"online"`
	Confidential bool    `json:"confidential"`
	FreeNow      bool    `json:"free_now"`
	Scheduled    bool    `json:"scheduled"`
	TPS          float64 `json:"tps"` // measured output tokens/sec (0 = not yet measured)
}

// discover handles GET /discover: all model offers with live status, measured
// throughput, and active (time-of-use) price, cheapest-now first.
func (b *broker) discover(w http.ResponseWriter, r *http.Request) {
	if corsPreflight(w, r) {
		return
	}
	if !allow(w, r, http.MethodGet) {
		return
	}
	cors(w) // public market data - let the website (rogerai.fyi) fetch it
	b.mu.Lock()
	now := time.Now()
	var out []offerView
	for _, n := range b.nodes {
		online := time.Since(b.lastSeen[n.NodeID]) < 35*time.Second
		for _, o := range n.Offers {
			pin, pout, free, _ := o.ActivePrice(now)
			out = append(out, offerView{
				NodeID: n.NodeID, Region: n.Region, HW: n.HW, Model: o.Model,
				In: pin, Out: pout, Ctx: o.Ctx, Online: online,
				Confidential: b.confidential[n.NodeID], FreeNow: free, Scheduled: len(o.Schedule) > 0,
				TPS: b.tps[n.NodeID],
			})
		}
	}
	b.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].In < out[j].In })
	writeJSON(w, http.StatusOK, map[string]any{"offers": out})
}

// marketView is the per-model market summary surfaced by GET /market.
type marketView struct {
	Model       string  `json:"model"`
	Providers   int     `json:"providers"`    // online nodes offering this model
	InFlight    int     `json:"in_flight"`    // active requests across those nodes
	MinPrice    float64 `json:"min_price"`    // cheapest active input price (credits/1M)
	BestTPS     float64 `json:"best_tps"`     // fastest measured output tok/s
	SuccessRate float64 `json:"success_rate"` // mean EWMA success across providers (0..1)
	Signal      int     `json:"signal"`       // 0..100 demand/quality signal
}

// market handles GET /market: a per-model marketplace view aggregated from live
// node state - how many providers are online, current in-flight load, the cheapest
// active price, the best measured throughput, mean success rate, and a 0..100
// "signal" combining supply, quality, and reliability. Concurrency-safe.
func (b *broker) market(w http.ResponseWriter, r *http.Request) {
	if corsPreflight(w, r) {
		return
	}
	if !allow(w, r, http.MethodGet) {
		return
	}
	cors(w) // public market data - let the website (rogerai.fyi) fetch it
	type acc struct {
		providers   int
		inflight    int
		minPrice    float64
		havePrice   bool
		bestTPS     float64
		successSum  float64
		successSeen int
	}
	now := time.Now()
	agg := map[string]*acc{}

	b.mu.Lock()
	b.metricsMu.Lock()
	for _, n := range b.nodes {
		if time.Since(b.lastSeen[n.NodeID]) >= 35*time.Second {
			continue
		}
		tps := b.tps[n.NodeID]
		inflight := b.inflight[n.NodeID]
		sr, srSeen := b.success[n.NodeID]
		for _, o := range n.Offers {
			a := agg[o.Model]
			if a == nil {
				a = &acc{}
				agg[o.Model] = a
			}
			a.providers++
			a.inflight += inflight
			in, _, _, _ := o.ActivePrice(now)
			if !a.havePrice || in < a.minPrice {
				a.minPrice, a.havePrice = in, true
			}
			if tps > a.bestTPS {
				a.bestTPS = tps
			}
			if srSeen {
				a.successSum += sr
				a.successSeen++
			}
		}
	}
	b.metricsMu.Unlock()
	b.mu.Unlock()

	out := make([]marketView, 0, len(agg))
	for model, a := range agg {
		successRate := 1.0 // optimistic until we have evidence
		if a.successSeen > 0 {
			successRate = a.successSum / float64(a.successSeen)
		}
		out = append(out, marketView{
			Model: model, Providers: a.providers, InFlight: a.inflight,
			MinPrice: a.minPrice, BestTPS: a.bestTPS,
			SuccessRate: round6(successRate),
			Signal:      marketSignal(a.providers, a.inflight, a.bestTPS, successRate),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Signal > out[j].Signal })
	writeJSON(w, http.StatusOK, map[string]any{"market": out})
}

// marketSignal scores a model 0..100. Higher = a healthier channel: more online
// providers (supply), proven throughput (quality), high success (reliability),
// lightly discounted by current congestion (in-flight load per provider). This is
// deliberately simple + monotonic; it is NOT a price - it's a glanceable health bar.
func marketSignal(providers, inflight int, bestTPS, successRate float64) int {
	if providers == 0 {
		return 0
	}
	// Supply: saturates around ~5 providers.
	supply := float64(providers) / 5.0
	if supply > 1 {
		supply = 1
	}
	// Quality: measured tok/s, saturating around 300 t/s.
	quality := bestTPS / 300.0
	if quality > 1 {
		quality = 1
	}
	// Congestion penalty: load per provider; ~2+ in-flight each = fully congested.
	congestion := float64(inflight) / float64(providers) / 2.0
	if congestion > 1 {
		congestion = 1
	}
	// Weighted blend, then knock off congestion.
	score := 0.45*supply + 0.30*quality + 0.25*successRate
	score *= (1 - 0.4*congestion)
	s := int(score*100 + 0.5)
	if s < 0 {
		s = 0
	}
	if s > 100 {
		s = 100
	}
	return s
}
