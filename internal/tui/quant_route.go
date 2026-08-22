package tui

// BINDING A QUANT CHOICE TO ROUTING (MODEL-VARIANTS-DESIGN-2026-08-22, step 5).
//
// Bands are grouped by (model, quant), so a row on the dial IS a set of weights. But the
// BROKER groups by model alone - it knows nothing about quants and, by the founder's
// ruling, is not being taught. So tuning a Q4_K_M row would still let the broker route the
// turn to the bf16 station of the same model, and the dial's promise would be decoration.
//
// The fix uses a primitive that already exists: X-Roger-Exclude-Nodes. Name the stations
// running a DIFFERENT quant of the same model and the broker will not pick them.
//
// EXCLUDE, NOT PIN. X-Roger-Node would collapse the choice to a single station, so the
// first failure is a dead turn with no failover. Excluding the wrong quants leaves every
// station of the RIGHT quant available, which is what a band was always supposed to be.
//
// It is a CLIENT-side constraint by design (founder: "i think just the client is fine"),
// which is why no routing change was needed on the broker at all.

// quantExcludes returns the node ids to skip when tuning bd: every station serving the
// same model at a DIFFERENT quant.
//
// It returns nothing when the band has no stated quant. That case is not "match the
// blank": a band with no quant is an ABSENCE of information, and excluding every station
// that did state one would turn "I do not know what this is" into "I insist on not
// knowing" - narrowing the operator's routing on the strength of missing metadata.
func (m model) quantExcludes(bd band) []string {
	if bd.quant == "" {
		return nil
	}
	var out []string
	for _, other := range m.bands {
		if other.model != bd.model || other.quant == bd.quant {
			continue
		}
		for _, o := range other.all {
			if o.NodeID != "" {
				out = append(out, o.NodeID)
			}
		}
	}
	return out
}

// prefExcludes returns the node ids to skip for `model` under the operator's STANDING
// preference (Limit.Quants) - the [3] CONFIG rule rather than the dial's view.
//
// This is what makes the preference a rule at all. The dial filter cannot protect a turn
// nobody is watching: the agent picks a model and runs, `roger use` proxies for a bot, and
// neither consults what the browse list happened to be showing. Both go through the same
// routing options, so naming the disallowed stations here is what binds them.
func (m model) prefExcludes(model string) []string {
	lim := m.limits.resolve(model)
	if len(lim.Quants) == 0 {
		return nil
	}
	var out []string
	for _, b := range m.bands {
		if b.model != model || lim.acceptsQuant(b.quant) {
			continue
		}
		for _, o := range b.all {
			if o.NodeID != "" {
				out = append(out, o.NodeID)
			}
		}
	}
	return out
}

// routeExcludes is every station this caller will not accept for `model`: the tuned row's
// quant constraint AND the standing preference, together.
//
// Both, not either. They answer different questions - "the row I am on" and "what I will
// ever accept" - and an operator who set a preference and then tuned a row means both
// things at once. Passing only one would silently drop the other.
func (m model) routeExcludes(bd band) []string {
	seen := map[string]bool{}
	var out []string
	for _, n := range append(m.quantExcludes(bd), m.prefExcludes(bd.model)...) {
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	return out
}
