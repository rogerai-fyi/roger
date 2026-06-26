package main

import (
	"fmt"
	"net/http"
	"time"

	"github.com/rogerai-fyi/roger/internal/store"
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
	if spend+maxCost > cap {
		// Surface the at-limit headers on the rejection too, so a client shows the same
		// "$X of $Y" line whether it was warned or hard-stopped.
		setCapHeaders(w, capState{cap: cap, spend: spend, pct: spend / cap, atLimit: true})
		// Flag-gated transactional notice (async, de-duped per holder/month). No-op
		// when RESEND_API_KEY is unset or no email on file.
		b.emailCapNotice(holder, "100", spend, cap, now)
		return http.StatusPaymentRequired, fmt.Sprintf(
			"monthly spend limit reached: $%.2f of $%.2f this month - raise it with `roger limit --monthly` (or [3] CONFIG), or wait until next month",
			round6(spend), round6(cap))
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
