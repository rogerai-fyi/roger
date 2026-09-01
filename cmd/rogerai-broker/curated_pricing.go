package main

// curated_pricing.go - THE ONE PLACE the curated money rules live.
//
// Founder ruling 2026-09-01: curated operators are paid exactly what their upstream
// charges, plus half the routing fee (the 50/50 ruling); the consumer pays list + markup, and the
// broker's fee for anonymization, routing across the broker/tower network, and picking
// the best-measured connection among same-model stations.
//
// The arithmetic these helpers exist to make impossible to get wrong twice: at
// posted = list x M, the STANDARD settlement (cost x 0.90 at the current fee) would hand
// a curated operator 0.90 x M = 0.99 x list against a 1.00 x list upstream bill -
// underwater on every token, invisibly, forever (and worse at any higher fee). Curated settlement is therefore its own rule, defined
// beside its markup so the two can never drift apart.

// defaultFeeRate is the platform's take on a HUMAN station's settled cost (the operator
// keeps 1-defaultFeeRate). One number with curatedMarkup below: the founder's 2026-09-01
// ruling is ONE 10% fee across both planes ("10% approved ... 90/5/5"), sized against the
// researched routing-fee market (aggregators cluster at ~5%, 0% exists; 30% was ~6x
// market). Overridable per deployment via --fee / ROGERAI_FEE; pinned by
// features/money/fee_splits.feature "An unconfigured broker takes exactly the
// ten-percent default".
const defaultFeeRate = 0.10

// curatedMarkup is the multiplier from a curated station's DECLARED upstream list price
// to its POSTED price. Broker-owned: changing it here re-derives every curated posted
// price at the next registration refresh, and re-scales every settlement split with it.
const curatedMarkup = 1.10

// curatedPosted derives the consumer-facing price from a declared upstream list price.
// Zero stays zero: a free upstream is posted free (the markup is a fee on money moved,
// and no money moves).
func curatedPosted(list float64) float64 { return list * curatedMarkup }

// curatedFeeShare is the fraction of the ROUTING FEE POOL (cost minus the reimbursed
// list) a curated operator keeps on top of their reimbursement - the founder's 50/50
// ruling (2026-09-01, "do the 50/50 split of the fee pool for curators"): the incentive
// that makes a stranger bring their provider contracts to the dial. Deliberately a share
// OF THE POOL and never a percent of the posted price: pool-share >= 0 keeps the
// operator >= list at ANY markup, while a percent-of-posted (a 95% split, say) drowns
// them again the moment the markup constant moves (0.95 x 1.04 < 1.00).
const curatedFeeShare = 0.50

// curatedOwnerShare is the curated settlement: reimburse the operator's upstream bill
// (cost/M recovers exactly the declared list's share, whatever the token counts were),
// then add their half of the routing fee the posted markup collected. The broker retains
// the other half. At the 1.10 markup: a $1.10 request credits $1.05 and retains $0.05.
func curatedOwnerShare(cost float64) float64 {
	list := cost / curatedMarkup
	return list + curatedFeeShare*(cost-list)
}

// nodeCurated reports whether the named node registered as a curated station.
func (b *broker) nodeCurated(node string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	reg, ok := b.nodes[node]
	return ok && reg.Curated
}
