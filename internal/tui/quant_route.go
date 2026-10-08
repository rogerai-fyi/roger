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
	return "the tuned " + rowQuant + " row is outside your quant rule for " + model +
		" (" + strings.Join(lim.Quants, ", ") + ") - tune an accepted row or change the rule in [3] CONFIG"
}

// routing is the consumer routing object every in-booth path sends for `model`: the
// standing pref, the confidential toggle, hidden curated supply, and the quant choice.
func (m model) routing(model, rowQuant string) client.Routing {
	return client.Routing{
		Pref:           m.limits.resolve(model).Pref,
		Confidential:   m.confidentialOnly,
		SelfHostedOnly: m.fNoCurated,
		Quantizations:  m.quantList(model, rowQuant),
		HeaderMode:     m.headerRouting,
	}
}

// tunedQuant is the quant of the row the operator is connected to for `model`, or "" when
// the connection is on another model or stated no quant.
func (m model) tunedQuant(model string) string {
	if m.connected == nil || m.connected.Model != model {
		return ""
	}
	return m.connected.Quant
}
