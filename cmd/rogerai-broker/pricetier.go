package main

import (
	"sort"
)

// Price-tier classification — a NEUTRAL, buyer-facing "$ … $$$$" signal computed once
// here and carried on every offer (offerView.PriceTier) so the TUI, web models page,
// and companion all render the SAME interpretation. Pricing stays operator-set; this
// only INTERPRETS the market. Full contract + scenarios: features/pricing/price_tier.feature.

// minMarketDepth is the fewest ONLINE bands a model needs before the internal-median
// fallback will classify a price; below it the median is too noisy to be honest.
const minMarketDepth = 3

// tierEps absorbs float-division noise at the inclusive low-side boundaries: the spec
// enumerates e.g. 0.070/0.10 -> $ (a deal), but that division is 0.7000000000000001 in
// float64, so a bare `r <= 0.70` would wrongly drop the band to $$. Comparing against
// `threshold + tierEps` keeps the boundary inclusive as specified, in BOTH scales.
const tierEps = 1e-9

// tierExternal grades a band against a same-model COMMERCIAL reference (discount depth):
// $ = a deep discount, $$$$ ≈ paying the commercial price. Inclusive on the low side.
func tierExternal(r float64) int {
	switch {
	case r <= 0.25+tierEps:
		return 1
	case r <= 0.50+tierEps:
		return 2
	case r <= 0.90+tierEps:
		return 3
	default:
		return 4
	}
}

// tierInternal grades a band against the live per-model median (position among peers).
// Inclusive on the low side.
func tierInternal(r float64) int {
	switch {
	case r <= 0.70+tierEps:
		return 1
	case r <= 1.15+tierEps:
		return 2
	case r <= 2.00+tierEps:
		return 3
	default:
		return 4
	}
}

// medianOut returns the median OUT-price (mirrors client.MarketMedianOut: odd -> middle,
// even -> mean of the two middle). It copies + sorts, so the caller's slice is untouched.
func medianOut(prices []float64) (float64, bool) {
	n := len(prices)
	if n == 0 {
		return 0, false
	}
	s := append([]float64(nil), prices...)
	sort.Float64s(s)
	if n%2 == 1 {
		return s[n/2], true
	}
	return (s[n/2-1] + s[n/2]) / 2, true
}

// priceTier classifies a band's active OUT-price into 0..4.
//
//	priceOut <= 0                       -> 0  (FREE; rendered as the FREE badge)
//	refOut  > 0                         -> EXTERNAL scale (vs the same model elsewhere)
//	>= minMarketDepth peers, median > 0 -> INTERNAL scale (vs live peers)
//	otherwise                           -> 0  (UNKNOWN; too thin to classify honestly)
//
// onlinePeersOut is the model's ONLINE out-prices (the band itself included); it is used
// only for the internal fallback. The external reference takes precedence so a band
// cannot dodge the signal by flooding cheap peers.
func priceTier(priceOut, refOut float64, onlinePeersOut []float64) int {
	if priceOut <= 0 {
		return 0 // FREE
	}
	if refOut > 0 {
		return tierExternal(priceOut / refOut)
	}
	if len(onlinePeersOut) >= minMarketDepth {
		if med, ok := medianOut(onlinePeersOut); ok && med > 0 {
			return tierInternal(priceOut / med)
		}
	}
	return 0 // UNKNOWN
}

// The neutral tier (0..4) is RENDERED into "$ … $$$$" display glyphs by the shared
// internal/pricetier package (pricetier.Render / .Label), which the broker, TUI, and client
// all import - one canonical render, so every surface reads a band's price identically.

// assignPriceTiers fills each offer's PriceTier from the same-model external reference
// (preferred) or the live per-model median of the ONLINE, PRICED offers in the set.
// Mutates in place: offline offers are still classified (against the live median / ref);
// FREE (price 0) and thin/no-data offers get tier 0. Concurrency-safe (b.refOut locks
// b.refMu, which is independent of the b.mu the callers hold).
func (b *broker) assignPriceTiers(offers []offerView) {
	peers := map[string][]float64{}
	for _, o := range offers {
		if o.Online && o.Out > 0 {
			peers[o.Model] = append(peers[o.Model], o.Out)
		}
	}
	for i := range offers {
		ref, _ := b.refOut(offers[i].Model)
		offers[i].PriceTier = priceTier(offers[i].Out, ref, peers[offers[i].Model])
	}
}
