package main

// curated_pricing.go - THE ONE PLACE the curated money rules live.
//
// Founder ruling 2026-09-01: curated operators are paid exactly what their upstream
// charges (pass-through); the consumer pays that plus a routing markup, which is the
// broker's fee for anonymization, routing across the broker/tower network, and picking
// the best-measured connection among same-model stations.
//
// The arithmetic these helpers exist to make impossible to get wrong twice: at
// posted = list x M, the STANDARD settlement (cost x 0.70) would hand a curated operator
// 0.70 x M = 0.91 x list against a 1.00 x list upstream bill - nine percent underwater on
// every token, invisibly, forever. Curated settlement is therefore its own rule, defined
// beside its markup so the two can never drift apart.

// curatedMarkup is the multiplier from a curated station's DECLARED upstream list price
// to its POSTED price. Broker-owned: changing it here re-derives every curated posted
// price at the next registration refresh, and re-scales every settlement split with it.
const curatedMarkup = 1.30

// curatedPosted derives the consumer-facing price from a declared upstream list price.
// Zero stays zero: a free upstream is posted free (the markup is a fee on money moved,
// and no money moves).
func curatedPosted(list float64) float64 { return list * curatedMarkup }

// curatedOwnerShare is the pass-through: the portion of a curated request's cost that
// reimburses the operator's upstream bill. cost/M returns exactly the declared list
// price's share, whatever the token counts were, so the operator is made whole and
// nothing more - reimbursement, not income. The broker retains cost - cost/M: the
// routing fee the posted markup collected.
func curatedOwnerShare(cost float64) float64 { return cost / curatedMarkup }

// nodeCurated reports whether the named node registered as a curated station.
func (b *broker) nodeCurated(node string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	reg, ok := b.nodes[node]
	return ok && reg.Curated
}
