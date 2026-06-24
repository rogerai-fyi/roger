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
