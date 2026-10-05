package main

import (
	"fmt"
	"net/http"
	"time"

	"rogerai.fm/roger/v6/internal/store"
)

// Per-account MONTHLY SPEND CAP enforcement (a budget limit, modeled on Groq's "set a
// max you'll pay per month, notify + stop at the limit"). The cap is a $ ceiling on
// captured spend per CALENDAR month, stored per GitHub-linked wallet (internal/store).
// Enforcement is GLOBAL: it sits at the credit-hold gate in relay (tunnel.go), the one
// path every paid consume route (public use, --freq, grant, the [0] agent harness, in-
// channel chat) funnels through. Self-use / free ($0) never reaches it.

// capState is the month-to-date snapshot used for both enforcement and the
// near/at-cap notices surfaced in the response headers + /balance body.
type capState struct {
	cap     float64 // the account's monthly cap ($); 0 = unlimited
	spend   float64 // captured month-to-date spend ($)
	pct     float64 // spend/cap (0 when unlimited)
	near    bool    // crossed the 80% notify threshold (and not yet at the cap)
	atLimit bool    // at/over the cap (spend would be blocked)
}

// monthlyCapState reads a wallet's cap + month-to-date spend and derives the notify
// flags. An unlimited cap (0) reports near=false/atLimit=false.
func (b *broker) monthlyCapState(holder string, now time.Time) capState {
	cap, _ := b.db.MonthlyCapOf(holder)
	spend := b.monthSpend(holder, now)
	return capStateFrom(cap, spend)
}

// capStateFrom derives the cap snapshot from ALREADY-READ cap + spend values (no query),
// so a caller that already has both can build the headers/notices without re-querying.
// An unlimited cap (0) reports near=false/atLimit=false. This is the W2a refactor: it
// lets monthlyCapCheck reuse the spend/cap it already read instead of re-summing them.
func capStateFrom(cap, spend float64) capState {
	s := capState{cap: cap, spend: spend}
	if cap > 0 {
		s.pct = spend / cap
		s.atLimit = spend >= cap
		s.near = !s.atLimit && spend >= cap*store.CapNearThreshold
	}
	return s
}

// monthlyCapCheck enforces the cap for one paid relay request. It returns a non-zero
// HTTP status + message when the request must be REJECTED (the request's worst-case
// cost would push month-to-date spend past the cap), 0 to allow. On allow it also sets
// the near/at-cap notice headers so a client can warn "you've used $X of your $Y
// monthly limit" without a second round-trip. Caller only invokes this on a paid
// (maxCost>0) request, so free/self spend is never blocked.
func (b *broker) monthlyCapCheck(w http.ResponseWriter, holder string, maxCost float64, now time.Time) (int, string) {
	cap, _ := b.db.MonthlyCapOf(holder)
	if cap <= 0 {
		return 0, "" // unlimited (opt-in feature; default off)
	}
	spend := b.monthSpend(holder, now)
	// Reject when even this request's worst-case (the hold amount) would exceed the cap.
	// Using the upper-bound cost mirrors the hold: we never authorize spend we couldn't
	// also have to capture. A request that exactly fits is allowed.
	// This read is a PRE-CHECK only (the counter is a cache, never the authority): it refuses
	// early when captured spend alone already leaves no room; the decision that authorizes is
	// the capped hold (holdUnderCap), which also counts open holds. The relay does not call
	// it for a floor-only (free) hold.
	if spend+maxCost > cap {
		// The counter is a cache that can over-read: confirm against the ledger before
		// refusing, and reseed the counter with the truth.
		if truth, err := b.db.MonthSpendOf(holder, now); err == nil && truth < spend {
			spend = truth
			if b.shared != nil {
				_ = b.shared.counterSet(capSpendKey(holder, now), truth, capCounterTTL)
			}
		}
	}
	if spend+maxCost > cap {
		// Surface the at-limit headers on the rejection too, so a client shows the same
		// "$X of $Y" line whether it was warned or hard-stopped.
		return b.capRefusal(w, holder, spend, 0, cap, now)
	}
	// Allowed: emit the near/at notice headers from the cap + spend we ALREADY read
	// (W2a) - monthlyCapState would re-query both, doubling the work; capStateFrom
	// reuses the values, so the hot paid path runs exactly ONE cap read + ONE spend read.
	cs := capStateFrom(cap, spend)
	setCapHeaders(w, cs)
	// Flag-gated transactional notice on crossing the 80% near-threshold (async,
	// de-duped per holder/month). No-op when RESEND_API_KEY is unset or no email.
	if cs.near {
		b.emailCapNotice(holder, "80", spend, cap, now)
	}
	return 0, ""
}

// setCapHeaders writes the monthly-budget notice headers. They are always safe to send
// (no secrets) and let the CLI/TUI print "you've used $X of your $Y monthly limit"
// inline. Omitted entirely when the cap is unlimited (no budget to report).
func setCapHeaders(w http.ResponseWriter, s capState) {
	if s.cap <= 0 {
		return
	}
	h := w.Header()
	h.Set("X-RogerAI-Monthly-Cap", ftoa(round6(s.cap)))
	h.Set("X-RogerAI-Monthly-Spend", ftoa(round6(s.spend)))
	h.Set("X-RogerAI-Monthly-Pct", fmt.Sprintf("%.0f", s.pct*100))
	switch {
	case s.atLimit:
		h.Set("X-RogerAI-Monthly-Notice", fmt.Sprintf("monthly limit reached - $%.2f of $%.2f this month", round6(s.spend), round6(s.cap)))
	case s.near:
		h.Set("X-RogerAI-Monthly-Notice", fmt.Sprintf("you've used $%.2f of your $%.2f monthly limit (%.0f%%)", round6(s.spend), round6(s.cap), s.pct*100))
	}
}

// freeFloorHold is the hold a $0 offer still places (estimateMaxCost's floor): a request
// whose hold is only this floor costs nothing and is never refused by the monthly cap
// (founder ruling 2026-10-04: free stations bypass the cap).
const freeFloorHold = 1e-6

// capHoldGapForTest, when set, runs between the cap pre-check and the capped hold. Tests
// use it to interleave another instance's whole request there; nil in production.
var capHoldGapForTest func()

// holdUnderCap places a TRACKED hold of amount for holder under the monthly cap, decided in
// the hold transaction (store.HoldForCapped: captured spend + open holds + amount <= cap
// under the wallet row lock), so concurrent requests on any instance can never overshoot.
// A floor-only hold is exempt. On a cap refusal it writes nothing itself and returns the
// same 402 status and message as monthlyCapCheck, having set the at-limit headers,
// X-RogerAI-Cost: 0 and the 100% notice; the caller answers with them.
func (b *broker) holdUnderCap(w http.ResponseWriter, holder, requestID string, amount float64, now time.Time) (held bool, status int, msg string, err error) {
	cap := 0.0
	if amount > freeFloorHold {
		cap, _ = b.db.MonthlyCapOf(holder)
	}
	if capHoldGapForTest != nil {
		capHoldGapForTest()
	}
	res, err := b.db.HoldForCapped(holder, requestID, amount, cap, now)
	if err != nil {
		return false, 0, "", err
	}
	if res.OverCap {
		status, msg = b.capRefusal(w, holder, res.Spend, res.Pending, cap, now)
		return false, status, msg, nil
	}
	return res.OK, 0, "", nil
}

// capRefusal sets the at-limit headers, X-RogerAI-Cost: 0 and the 100% notice, and returns
// the approved 402 for a request the monthly cap refuses. pending is the wallet's open holds
// the decision counted: when captured spend alone is below the cap the refusal says the rest
// is held by requests still in progress (X-RogerAI-Monthly-Pending), and the once-a-month
// 100% notice is kept for spend that has actually reached the cap.
func (b *broker) capRefusal(w http.ResponseWriter, holder string, spend, pending, cap float64, now time.Time) (int, string) {
	setCapHeaders(w, capState{cap: cap, spend: spend, pct: spend / cap, atLimit: true})
	w.Header().Set("X-RogerAI-Cost", "0")
	if spend < cap && pending > 0 {
		w.Header().Set("X-RogerAI-Monthly-Pending", ftoa(round6(pending)))
		return http.StatusPaymentRequired, fmt.Sprintf(
			"monthly spend limit reached: $%.2f spent and $%.2f held by requests still in progress, of $%.2f this month - retry when they finish, raise it with `roger limit --monthly` (or [3] CONFIG), or wait until next month",
			round6(spend), round6(pending), round6(cap))
	}
	b.emailCapNotice(holder, "100", spend, cap, now)
	return http.StatusPaymentRequired, fmt.Sprintf(
		"monthly spend limit reached: $%.2f of $%.2f this month - raise it with `roger limit --monthly` (or [3] CONFIG), or wait until next month",
		round6(spend), round6(cap))
}
