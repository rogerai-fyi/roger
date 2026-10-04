package main

// Tower rows inside the relay's attempt plan (contract §6, slice 1 part C).
//
// A routable Tower row is one more candidate for a request: it is evaluated under the same
// filters as a direct station (edgeEligibleM), ranked with the direct candidates by the same
// order / sort / tier rules, and tried in its turn by the same loop that tries a direct
// station - the ONE hold of the request follows a bridged attempt exactly as it follows a
// direct one (edgeHold.key: the pending-hold row is rekeyed to the attempt id the Tower's
// settlement later captures). What used to be a one-shot side door (relayViaEdge on the
// fan-out coin, a blanket decline for anything it could not evaluate) is now a column of
// the plan.

import (
	"fmt"
	"log"
	"math"
	"math/rand"
	"net/http"
	"sort"
	"time"

	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/towercore/dispatch"
	"rogerai.fm/roger/v6/internal/towercore/fleet"
)

// edgeCand is a Tower routable row as the plan sees it.
type edgeCand struct {
	target dispatch.Target
	row    fleet.Station
	tier   string     // "A" or "B": the pool the placement drew it from
	metric edgeMetric // what a strict sort ranks it by
	wallet string     // the account wallet the bridge bills (edgeCallerFor)
	pubHex string     // the verified consumer pubkey (the receipt's pseudonym)
}

// edgePlanCands places up to max Tower rows for model under the consumer's constraints,
// each placement excluding the Towers already placed (the tower-to-tower order).
func (b *broker) edgePlanCands(model string, rng *rand.Rand, c edgeConstraints, max int, wallet, pubHex string) []edgeCand {
	ts := b.tower
	if ts == nil || ts.dispatch == nil || max <= 0 {
		return nil
	}
	exclude := map[string]bool{}
	var out []edgeCand
	for len(out) < max {
		target, row, tier, metric, ok := b.edgePlacement(model, rng, exclude, c)
		if !ok {
			break
		}
		out = append(out, edgeCand{target: target, row: row, tier: tier, metric: metric, wallet: wallet, pubHex: pubHex})
		exclude[row.TowerID] = true
	}
	return out
}

// edgeCoinForTest, when set, decides the fan-out coin instead of the request seed. It is nil
// in production; a BDD step sets it to make "the coin always says edge" a real statement
// rather than a hope over a batch.
var edgeCoinForTest func() bool

// edgePlanCand resolves a placed Tower row into a plan candidate: the body the Tower's hub
// receives (the dispatched model, carriers already stripped), its hold ceiling at the row's
// price over the joined node's declared window (the same holdCostFor a direct pair uses), and
// the per-request cap lowering max_tokens to what the cap buys at that price.
func (b *broker) edgePlanCand(model string, e edgeCand, body []byte, capReq float64, promptTokens int, now time.Time) attemptCand {
	b.mu.Lock()
	reg := b.nodes[e.row.NodeID]
	b.mu.Unlock()
	joined, _ := edgeJoinedOffer(reg, model)
	offer := protocol.ModelOffer{Model: model, PriceIn: e.metric.in, PriceOut: e.metric.out, Ctx: joined.Ctx, CtxEstimated: joined.CtxEstimated}
	pricing := pricingPlan{payer: e.wallet}
	ec := e
	c := attemptCand{node: protocol.NodeRegistration{NodeID: e.row.TowerID}, offer: offer, pricing: pricing, model: model, body: body, edge: &ec}
	// The larger of the window estimate and the grant's own ceiling (founder ruling
	// 2026-10-02): the Tower's settlement clamps to the hold, so a hold below the ceiling could
	// underpay the operator. The consumer's own caps still bound it (capReq below; a pair the
	// wallet cannot cover is trimmed from the plan like any other).
	c.maxCost = math.Max(holdCostFor(pricing, offer, body, now), edgeGrantCeiling(e.row.PriceIn, e.row.PriceOut))
	if capReq > 0 && c.maxCost > capReq {
		buys, _ := capBuys(capReq, promptTokens, offer.PriceIn, offer.PriceOut)
		c.body = capBody(body, buys)
		c.maxCost = capReq
	}
	return c
}

// directMetric is what a strict sort ranks a direct candidate by, and whether it sits in
// Tier A - the same bar pickFor draws (tierAHealthy).
func (b *broker) directMetric(c attemptCand, now time.Time) (edgeMetric, bool) {
	ain, aout, _, _ := c.offer.ActivePrice(now)
	b.mu.Lock()
	tq := b.trust[c.node.NodeID]
	b.mu.Unlock()
	b.metricsMu.Lock()
	tps := b.tps[c.node.NodeID]
	sr, sseen := b.success[c.node.NodeID]
	b.metricsMu.Unlock()
	prompt := approxPromptTokens(c.body)
	cost := estRequestCost(prompt, expectedOutput(statedOutputTokens(c.body), prompt, c.offer.Ctx), ain, aout)
	return edgeMetric{in: ain, out: aout, cost: cost, tps: tps, ttft: tq.ttftMs}, tierAHealthy(tq.probeFails, sr, sseen)
}

// edgeMetricLess is the strict single-metric ordering of contract §5 over two candidates
// (price: out then in ascending; throughput: tps descending, unmeasured last; latency: ttft
// ascending, unmeasured last). decided is false on a tie, which the caller breaks.
func edgeMetricLess(key sortKey, a, b edgeMetric) (before, decided bool) {
	switch key {
	case sortPrice:
		if a.cost != b.cost {
			return a.cost < b.cost, true // estimated request cost (§14.7); a free offer costs 0
		}
		if a.out != b.out {
			return a.out < b.out, true
		}
		if a.in != b.in {
			return a.in < b.in, true
		}
	case sortThroughput:
		if am, bm := a.tps > 0, b.tps > 0; am != bm {
			return am, true
		}
		if a.tps != b.tps {
			return a.tps > b.tps, true
		}
	case sortLatency:
		if am, bm := a.ttft > 0, b.ttft > 0; am != bm {
			return am, true
		}
		if a.ttft != b.ttft {
			return a.ttft < b.ttft, true
		}
	}
	return false, false
}

// edgeMerge is how the Tower candidates of a request join the direct plan, per model.
type edgeMerge struct {
	order       []string                 // provider.order: the listed portion ranks first, in list order
	sort        sortKey                  // provider.sort: one metric across both fabrics, direct first on a tie
	onePerModel bool                     // allow_fallbacks:false: the head of each model only
	headModel   string                   // the model the request's first pick is for (the coin is flipped there)
	coinEdge    func() bool              // the fan-out coin, counted once when consulted; true = the Tower head goes first
	towers      map[string][]attemptCand // per model, in placement order
}

// mergeEdgePlan returns the plan with the Tower candidates inserted. Direct candidates keep
// their relative order; within one model a Tower row ranks by the consumer's order, else by
// the strict sort, else by the tier gate (a Tier-B row only behind a Tier-A direct station,
// and ahead of a Tier-B one), and when the two heads tie on tier the fan-out coin decides -
// only for the head model, and only once per request.
func (b *broker) mergeEdgePlan(plan []attemptCand, models []string, m edgeMerge, now time.Time) []attemptCand {
	orderIdx := make(map[string]int, len(m.order))
	for i, id := range m.order {
		if _, dup := orderIdx[id]; !dup {
			orderIdx[id] = i
		}
	}
	rank := func(c attemptCand) (int, bool) {
		id := c.node.NodeID
		if c.edge != nil {
			id = c.edge.row.TowerID
		}
		i, ok := orderIdx[id]
		return i, ok
	}
	var out []attemptCand
	for _, model := range models {
		var block []attemptCand
		for _, c := range plan {
			if c.model == model {
				block = append(block, c)
			}
		}
		tws := m.towers[model]
		switch {
		case len(tws) == 0:
			// nothing to merge
		case len(block) == 0:
			block = tws
		case len(m.order) > 0:
			// Listed candidates first in list order (a Tower id ranks exactly where it was named),
			// then the unlisted ones: direct by score, Towers behind them.
			merged := append(append([]attemptCand(nil), block...), tws...)
			key := func(c attemptCand) int {
				if i, ok := rank(c); ok {
					return i
				}
				if c.edge != nil {
					return len(m.order) + 1
				}
				return len(m.order)
			}
			sort.SliceStable(merged, func(i, j int) bool { return key(merged[i]) < key(merged[j]) })
			block = merged
		case m.sort != sortNone:
			// Snapshot every candidate's metric once (directMetric takes two locks), then sort
			// the indices: no lock is taken inside the comparator.
			merged := append(append([]attemptCand(nil), block...), tws...)
			metrics := make([]edgeMetric, len(merged))
			for i, c := range merged {
				if c.edge != nil {
					metrics[i] = c.edge.metric
				} else {
					metrics[i], _ = b.directMetric(c, now)
				}
			}
			idx := make([]int, len(merged))
			for i := range idx {
				idx[i] = i
			}
			sort.SliceStable(idx, func(i, j int) bool {
				before, decided := edgeMetricLess(m.sort, metrics[idx[i]], metrics[idx[j]])
				return decided && before
			})
			block = make([]attemptCand, len(merged))
			for i, k := range idx {
				block[i] = merged[k]
			}
		default:
			_, directA := b.directMetric(block[0], now)
			towerA := tws[0].edge.tier == "A"
			towerFirst := false
			switch {
			case towerA && !directA:
				towerFirst = true
			case !towerA && directA:
				towerFirst = false
			case model == m.headModel && m.coinEdge != nil:
				towerFirst = m.coinEdge()
			}
			if towerFirst {
				block = append(append([]attemptCand(nil), tws...), block...)
			} else {
				block = append(block, tws...)
			}
		}
		if m.onePerModel && len(block) > 1 {
			block = block[:1]
		}
		out = append(out, block...)
	}
	return out
}

// edgeDeclines is the per-request tally of Tower rows a consumer constraint declined: one
// counter per constraint per request on /admin/live (edge_bridge_declined{<constraint>}) and
// one log line per (constraint, Tower) naming the request - never a band code. The line does
// not begin with "edge bridge:", which is the mark of a bridge that RAN.
func (b *broker) flushEdgeDeclines(requestID string, declined map[string]map[string]bool) {
	for reason, towers := range declined {
		b.stats.edgeDeclineAdd(reason)
		for tower := range towers {
			log.Printf("edge decline: request=%s tower=%s constraint=%s", requestID, tower, reason)
		}
	}
}

// noteIDCollisions warns the operator once per id that names BOTH a direct node and a Tower
// (or the node behind a Tower row): each is matched in its own namespace (contract §5), and
// /admin/live says so.
func (b *broker) noteIDCollisions(ids []string, models []string) {
	ts := b.tower
	if ts == nil || ts.routable == nil || len(ids) == 0 {
		return
	}
	towerIDs := map[string]bool{}
	for _, model := range models {
		rows, err := ts.routable.Candidates(model, time.Now())
		if err != nil {
			continue
		}
		for _, row := range rows {
			towerIDs[row.TowerID] = true
			if row.NodeID != "" {
				towerIDs[row.NodeID] = true
			}
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, id := range ids {
		if _, direct := b.nodes[id]; direct && towerIDs[id] {
			b.stats.noteCollision(id)
		}
	}
}

// planEdgeAttempt runs one planned bridged attempt on the request's hold. soft is "a later
// candidate stands behind this one", which bounds a dead Tower to the short drive timeout.
func (b *broker) planEdgeAttempt(r *http.Request, c attemptCand, payer string, holdKey *string, maxCost float64, soft bool, deadline time.Time) ([]byte, dispatch.EdgeGrant, edgeOutcome) {
	e := c.edge
	return b.edgeAttempt(r, e.target, e.row, c.model, c.body, e.wallet, edgeHold{payer: payer, key: holdKey, maxCost: maxCost}, soft, deadline)
}

// voidEdgeAttempt records the $0 lineage receipt of a bridged attempt that failed, as a
// direct attempt's void is recorded: the attempt id, the relay the broker dispatched to, the
// Tower, the dispatched model, the void reason and the upstream status.
func (b *broker) voidEdgeAttempt(payer, user string, c attemptCand, g dispatch.EdgeGrant, status int) {
	if g.AttemptID == "" {
		return
	}
	rec := protocol.UsageReceipt{
		RequestID: g.AttemptID, NodeID: g.RelayName, Relay: g.TowerID, Model: c.model,
		User: b.pseudonym(user, g.RelayName), PriceIn: edgeRowPrice(g.PriceInMicros), PriceOut: edgeRowPrice(g.PriceOutMicros),
		TS: time.Now().Unix(), VoidReason: voidReasonFor(status), UpstreamStatus: status,
	}
	rec.SignBroker(b.priv)
	_, _ = b.db.Settle(payer, g.RelayName, 0, 0, rec)
}

// towerFailureBody is the error body a Tower station's failure is answered with.
func towerFailureBody(status int) []byte {
	return errorBody("", fmt.Sprintf("the station behind the tower replied %d", status))
}
