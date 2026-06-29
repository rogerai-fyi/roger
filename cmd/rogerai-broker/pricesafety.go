package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/rogerai-fyi/roger/internal/protocol"
)

// Price-safety: hard ceilings that stop an absurd price from ever landing on the
// public market (operator side) and runaway consumer overpay (see the client). The
// operator ceiling lives broker-side because it is a marketplace invariant - it must
// hold no matter which client (CLI/TUI/web/raw) registered the node.

// maxPriceOutCeiling / maxPriceInCeiling are the per-1M-token hard caps a PUBLIC
// station may charge. Defaults: $100/1M out, $50/1M in (well above any real model's
// going rate, so only a typo or a deterrent price trips them). Env-overridable for
// the operator.
func maxPriceOutCeiling() float64 { return envFloat("ROGERAI_MAX_PRICE_OUT", 100) }
func maxPriceInCeiling() float64  { return envFloat("ROGERAI_MAX_PRICE_IN", 50) }

// consumerDefaultMaxOut is the broker-side DEFAULT consumer out-price cap (per 1M
// tokens) applied to a relay request that carries NO X-Roger-Max-Price-Out. It is the
// server-side mirror of the client's client.ConsumerDefaultMaxOut ($10/1M): the first-
// party CLI/TUI always injects the cap, but a hand-rolled API client that omits it must
// not silently bind to an exorbitant band. A consumer that DOES send a (higher) cap on
// purpose is honored as-is - this only fills the silent-default case. Env-overridable;
// <=0 disables the backstop (the operator ceiling still bounds the absolute max).
func consumerDefaultMaxOut() float64 {
	return envFloat("ROGERAI_CONSUMER_DEFAULT_MAX_PRICE_OUT", 10)
}

// effectiveRelayMaxOut returns the out-price cap the broker enforces in pick for one
// relay request: the consumer's explicit cap when set (>0), else the server-side default
// backstop (consumerDefaultMaxOut). Returns 0 only when the caller sent no cap AND the
// backstop is disabled, which means "no cap" (the operator ceiling is the sole bound).
func effectiveRelayMaxOut(reqMaxOut float64) float64 {
	if reqMaxOut > 0 {
		return reqMaxOut
	}
	return consumerDefaultMaxOut()
}

// registerPriceCeiling returns a non-empty rejection message if any offer (base price
// or any scheduled window) exceeds the public hard ceiling. The copy states the REAL
// remedy - lower the price below the ceiling - and deliberately does NOT suggest
// --private as an escape: the ceiling is GLOBAL (it binds private + confidential bands
// too; --private only hides a station from the public market, it is not a price bypass).
// Returns "" when every price is within bounds.
func registerPriceCeiling(offers []protocol.ModelOffer) string {
	outCap, inCap := maxPriceOutCeiling(), maxPriceInCeiling()
	check := func(in, out float64) string {
		if out > outCap {
			return fmt.Sprintf("output price $%.2f/1M exceeds the $%.2f/1M public ceiling - lower the price below the ceiling (it applies to every band, public or private)", out, outCap)
		}
		if in > inCap {
			return fmt.Sprintf("input price $%.2f/1M exceeds the $%.2f/1M public ceiling - lower the price below the ceiling (it applies to every band, public or private)", in, inCap)
		}
		return ""
	}
	for _, o := range offers {
		if msg := check(o.PriceIn, o.PriceOut); msg != "" {
			return msg
		}
		for _, win := range o.Schedule {
			if win.Free {
				continue
			}
			if msg := check(win.In, win.Out); msg != "" {
				return msg
			}
		}
	}
	return ""
}

// validateOfferInput checks an owner-authored (web Console) price + schedule before it
// is persisted as an override: non-negative prices, well-formed "HH:MM" window bounds,
// valid weekday indices (0=Sun..6=Sat), and non-negative per-window prices. It returns
// "" when the input is clean. The public price CEILING is enforced separately via
// registerPriceCeiling (so the same hard cap applies whether a price arrives by CLI
// registration or by a Console edit). Bad input is rejected here rather than silently
// dropped by ActivePrice's lenient parse, so the owner gets a clear error.
func validateOfferInput(priceIn, priceOut float64, schedule []protocol.PriceWindow) string {
	if priceIn < 0 || priceOut < 0 {
		return "price cannot be negative"
	}
	for _, w := range schedule {
		if !validHHMM(w.Start) || !validHHMM(w.End) {
			return fmt.Sprintf("schedule window times must be HH:MM (24h) - got start=%q end=%q", w.Start, w.End)
		}
		for _, d := range w.Days {
			if d < 0 || d > 6 {
				return fmt.Sprintf("schedule day must be 0-6 (Sun-Sat) - got %d", d)
			}
		}
		if !w.Free && (w.In < 0 || w.Out < 0) {
			return "schedule window price cannot be negative"
		}
	}
	return ""
}

// validHHMM reports whether s is a valid "HH:MM" 24h time (mirrors protocol.hhmm,
// which is unexported).
func validHHMM(s string) bool {
	p := strings.SplitN(s, ":", 2)
	if len(p) != 2 {
		return false
	}
	h, e1 := strconv.Atoi(strings.TrimSpace(p[0]))
	m, e2 := strconv.Atoi(strings.TrimSpace(p[1]))
	return e1 == nil && e2 == nil && h >= 0 && h <= 23 && m >= 0 && m <= 59
}
