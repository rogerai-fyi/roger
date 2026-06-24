package main

import (
	"fmt"

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
// or any scheduled window) exceeds the public hard ceiling. The copy steers a genuine
// "I want to be unreachable to the public" case to --private rather than an absurd
// price. Returns "" when every price is within bounds.
func registerPriceCeiling(offers []protocol.ModelOffer) string {
	outCap, inCap := maxPriceOutCeiling(), maxPriceInCeiling()
	check := func(in, out float64) string {
		if out > outCap {
			return fmt.Sprintf("output price $%.2f/1M exceeds the $%.2f/1M public ceiling - lower it, or use `--private` to share on a hidden frequency band instead", out, outCap)
		}
		if in > inCap {
			return fmt.Sprintf("input price $%.2f/1M exceeds the $%.2f/1M public ceiling - lower it, or use `--private` to share on a hidden frequency band instead", in, inCap)
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
