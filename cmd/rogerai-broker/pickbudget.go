package main

import (
	"os"
	"strconv"
	"strings"
	"sync/atomic"
)

// Bounded work per request (contract §14.B, #12): a routing object must not be able to buy
// unbounded broker work. Every pickFor call a relay makes draws on one per-request budget;
// past it pickFor finds nothing and the relay answers 503 routing_budget_exceeded before any
// hold or dispatch. The count is per request and lives only as long as the request, so it is
// not shared state.

// pickBudgetLimit is ROGERAI_PICK_BUDGET (default 64), read per call like the other knobs.
func pickBudgetLimit() int {
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv("ROGERAI_PICK_BUDGET"))); err == nil && v > 0 {
		return v
	}
	return 64
}

// pickBudget counts one request's pickFor calls. Atomic because the failover paths may pick
// from more than one goroutine of the same request.
type pickBudget struct {
	limit    int64
	picks    atomic.Int64
	exceeded atomic.Bool
}

func newPickBudget() *pickBudget { return &pickBudget{limit: int64(pickBudgetLimit())} }

// take spends one pick; false once the budget is gone (and from then on, exceeded).
func (p *pickBudget) take() bool {
	if p == nil {
		return true
	}
	if p.picks.Add(1) > p.limit {
		p.exceeded.Store(true)
		return false
	}
	return true
}

func (p *pickBudget) over() bool { return p != nil && p.exceeded.Load() }

// count is the pickFor calls made so far (the routing log's picks= field), capped at the
// calls that actually ran.
func (p *pickBudget) count() int64 {
	if p == nil {
		return 0
	}
	return min(p.picks.Load(), p.limit)
}

// capFilter is the per-request spend cap (provider.max_price.request) evaluated INSIDE the
// pick, so a station whose input cost alone meets the cap is never picked and then dropped
// (the old walk re-picked once per dropped station). It mirrors capDrops in the relay, which
// stays as the authoritative check after the pick: the billed price is the grant's fixed
// price when the request rides a grant, $0 on the caller's own station, else the offer's
// active price.
type capFilter struct {
	usd      float64 // the cap; 0 = none
	prompt   int     // the measured prompt (never zeroed by the ctx re-pick)
	grant    bool    // a grant fixes the billed price ...
	grantIn  float64 // ... at these prices
	grantOut float64
	own      map[string]bool // the caller's own stations: self-use is $0
}

func (c capFilter) drops(nodeID string, in, out float64, afree bool) bool {
	if c.usd <= 0 || c.own[nodeID] {
		return false
	}
	if c.grant {
		in, out, afree = c.grantIn, c.grantOut, c.grantIn == 0 && c.grantOut == 0
	}
	if afree {
		return false
	}
	_, drop := capBuys(c.usd, c.prompt, in, out)
	return drop
}
