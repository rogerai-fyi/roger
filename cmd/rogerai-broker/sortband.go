package main

// sortband.go - the fairness rules of contract §14.4-§14.6 (features/routing/fairness_and_abuse.feature):
// a strict sort picks within a band of the best, weighted by spare capacity; the Tower coin is
// weighted by eligible capacity on each side; home stations rank ahead of curated ones unless
// the consumer opted in to curated.

import "math/rand"

// sortBandOf is the band width (a fraction) for key: ROGERAI_SORT_BAND_PRICE (default 5%) for
// price, ROGERAI_SORT_BAND_SPEED (default 10%) for throughput and latency.
func sortBandOf(key sortKey) float64 {
	pct := 0.0
	switch key {
	case sortPrice:
		pct = envFloat("ROGERAI_SORT_BAND_PRICE", 5)
	case sortThroughput, sortLatency:
		pct = envFloat("ROGERAI_SORT_BAND_SPEED", 10)
	}
	if pct < 0 {
		pct = 0
	}
	return pct / 100
}

// inSortBand reports whether c sits within band of best on key. An unmeasured station is never
// in a measured best's band (unmeasured stays last).
func inSortBand(key sortKey, best, c edgeMetric, band float64) bool {
	switch key {
	case sortPrice:
		return c.cost <= best.cost*(1+band)
	case sortThroughput:
		if best.tps <= 0 {
			return c.tps <= 0
		}
		return c.tps > 0 && c.tps >= best.tps*(1-band)
	case sortLatency:
		if best.latency <= 0 {
			return c.latency <= 0
		}
		return c.latency > 0 && c.latency <= best.latency*(1+band)
	}
	return false
}

// spareSlots is a station's spare capacity: its effective capacity (measured concurrency when
// measured, else the declared hardware class - capacityOf) less what is in flight.
func spareSlots(capacity, inflight int) float64 {
	if s := capacity - inflight; s > 0 {
		return float64(s)
	}
	return 0
}

// spareWeightedPick draws an index with probability proportional to weights from the request's own
// PRNG (never by node id). All-zero weights draw uniformly; a nil rng takes the heaviest.
func spareWeightedPick(weights []float64, rng *rand.Rand) int {
	total := 0.0
	for _, w := range weights {
		total += w
	}
	if rng == nil {
		best := 0
		for i, w := range weights {
			if w > weights[best] {
				best = i
			}
		}
		return best
	}
	if total <= 0 {
		return rng.Intn(len(weights))
	}
	x := rng.Float64() * total
	for i, w := range weights {
		if x < w {
			return i
		}
		x -= w
	}
	return len(weights) - 1
}

// edgeCoin is the Tower fan-out coin (§14.5): true (the Tower head goes first) with probability
// towerCap / (directCap + towerCap), decided by the request seed. Unknown capacity on either side
// counts as one slot.
func edgeCoin(requestID string, directCap, towerCap int) bool {
	if directCap < 1 {
		directCap = 1
	}
	if towerCap < 1 {
		towerCap = 1
	}
	return seededRand(requestID).Float64()*float64(directCap+towerCap) < float64(towerCap)
}

// edgeSideCapacity is the eligible capacity on each side of the coin: the direct pool's nodes
// and the placed Tower rows, each station counted individually and ONCE - a node reachable both
// directly and behind a placed row counts on the Tower side.
func edgeSideCapacity(direct map[string]int, rows []attemptCand, rowCap func(node string) int) (d, t int) {
	behind := map[string]bool{}
	for _, c := range rows {
		if c.edge != nil {
			behind[c.edge.row.NodeID] = true
			t += rowCap(c.edge.row.NodeID)
		}
	}
	for node, capacity := range direct {
		if !behind[node] {
			d += capacity
		}
	}
	return d, t
}

// edgeRowCapacity is the effective capacity of the station behind a Tower row.
func (b *broker) edgeRowCapacity(node string) int {
	b.metricsMu.Lock()
	defer b.metricsMu.Unlock()
	return edgeCapacityOf(b.concurrentTPS[node])
}

// namesCurated reports whether any of ids is a curated station on air (an opt-in to curated).
func (b *broker) namesCurated(ids []string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, id := range ids {
		if reg, ok := b.nodes[id]; ok && reg.Curated {
			return true
		}
	}
	return false
}
