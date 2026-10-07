package tui

import (
	"strings"

	"rogerai.fm/roger/v6/internal/client"
)

// BINDING THE BOOTH'S RULES TO ROUTING (MODEL-VARIANTS-DESIGN-2026-08-22 step 5, and the
// routing-expression contract features/routing/ROUTING-EXPRESSION-CONTRACT.md §1/§5/§9).
//
// Bands are grouped by (model, quant), so a row on the dial IS a set of weights; the
// operator's [3] CONFIG rules (an accepted quant set, a pref) and the U key (hide curated
// supply) are things every request from the booth must carry, because an agent turn or a
// guest on the live proxy runs while nobody is watching a dial. They travel as the consumer
// ROUTING OBJECT in the request body, and the BROKER filters on them - which is what makes
// them rules rather than views, and what binds a station that registered after the last
// dial scan (an exclude list derived from m.bands could never name it).
//
// ABSENCE IS ASYMMETRIC HERE, ON PURPOSE. The tuned ROW means "these exact weights", so it
// travels as its one label and a station that stated no quant is not admitted: unknown
// weights are not the chosen ones. The standing RULE (Limit.acceptsQuant) means "any of these
// I would accept", and an unstated quant passes it, so the rule travels with "unknown"
// appended. Two questions, two answers; TestAbsenceIsReadDifferentlyByRowAndRule pins both.

// quantList is the provider.quantizations a request on `model` carries: the tuned row's
// label alone when a row is tuned, else the standing rule plus "unknown", else nothing.
// A tuned row OUTSIDE the standing rule is the caller's to refuse (quantRuleRefusal).
func (m model) quantList(model, rowQuant string) []string {
	if rowQuant != "" {
		return []string{rowQuant}
	}
	return client.RuleQuantizations(m.limits.resolve(model).Quants)
}

// quantRuleRefusal is the reason a turn on `model` at the tuned quant `rowQuant` must not be
// sent: the row contradicts the operator's standing rule. "" means the turn may go.
func (m model) quantRuleRefusal(model, rowQuant string) string {
	lim := m.limits.resolve(model)
	if rowQuant == "" || len(lim.Quants) == 0 || lim.acceptsQuant(rowQuant) {
		return ""
	}
	return rowQuant + " is outside your quant rule (" + strings.Join(lim.Quants, ", ") +
		") - edit it in [3] CONFIG or pick another row"
}

// routing is the consumer routing object every in-booth path sends for `model`: the
// standing pref, the confidential toggle, hidden curated supply, and the quant choice.
// It carries every key of the band's [3] CONFIG rule and the dial filters that bind: F as
// `:free`, C as roger.confidential, U as roger.self_hosted_only.
//
// The band's limits (the out, in and per-request price caps, the min-tps and context floors,
// the first-token ceiling) compose with the tuned profile's the STRICTER way
// (the lower cap, the higher floor), as the broker composes a header with a body. Every other
// key the profile states (trust, region, require, params, quantizations) replaces
// the band's: both are the owner's own choices, and the profile was picked on the confirm.
func (m model) routing(model, rowQuant string) client.Routing {
	lim := m.limits.resolve(model)
	rt := (client.Routing{
		Pref:           lim.Pref,
		Confidential:   m.confidentialOnly || m.fConf,
		SelfHostedOnly: m.fNoCurated || lim.SelfHosted,
		Quantizations:  m.quantList(model, rowQuant),
		MaxReq:         lim.MaxCost,
		Require:        lim.Require,
		ParamsB:        lim.ParamsB,
		MinCtx:         lim.MinCtx,
		MaxTTFT:        lim.MaxTTFTMs,
		TrustMin:       lim.TrustMin,
		Region:         lim.Region,
		FreeOnly:       m.fFree,
		HeaderMode:     m.headerRouting,
	}).Overlay(m.profileFor(model)) // the profile tuned under, for the connected band
	rt.MaxOut, rt.MaxIn = stricterCap(lim.MaxOut, rt.MaxOut), stricterCap(lim.MaxIn, rt.MaxIn)
	rt.MaxReq = stricterCap(lim.MaxCost, rt.MaxReq)
	// The context floor and first-token ceiling are limits too: the higher floor, the lower ceiling.
	rt.MinCtx = max(lim.MinCtx, rt.MinCtx)
	if lim.MaxTTFTMs > 0 && (rt.MaxTTFT == 0 || lim.MaxTTFTMs < rt.MaxTTFT) {
		rt.MaxTTFT = lim.MaxTTFTMs
	}
	// The dial row the operator connected to is the quant this turn asks for: a profile's list
	// never silently replaces it (the confirm refuses a row the profile excludes).
	if rowQuant != "" {
		rt.Quantizations = []string{rowQuant}
	}
	rt.MinTPS = max(lim.MinTPS, rt.MinTPS)
	return rt
}

// profileQuantRefusal is the reason a row at `rowQuant` must not be accepted under a profile
// whose provider.quantizations excludes it ("" when it may).
func profileQuantRefusal(body map[string]any, rowQuant string) string {
	p, _ := body["provider"].(map[string]any)
	list, _ := p["quantizations"].([]any)
	if rowQuant == "" || len(list) == 0 {
		return ""
	}
	var names []string
	for _, q := range list {
		s, _ := q.(string)
		if strings.EqualFold(s, rowQuant) {
			return ""
		}
		names = append(names, s)
	}
	return rowQuant + " is outside the profile's quant list (" + strings.Join(names, ", ") + ") - pick another row or profile"
}

// stricterCap is the lower of two price caps, where 0 means "no cap of my own".
func stricterCap(a, b float64) float64 {
	if a <= 0 || (b > 0 && b < a) {
		return b
	}
	return a
}

// tunedQuant is the quant of the row the operator is connected to for `model`, or "" when
// the connection is on another model or stated no quant.
func (m model) tunedQuant(model string) string {
	if m.connected == nil || m.connected.Model != model {
		return ""
	}
	return m.connected.Quant
}
