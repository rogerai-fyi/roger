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
//
// ABSENCE IS ASYMMETRIC HERE, ON PURPOSE. The tuned row means "these exact weights", so a
// station that did not state its quant is excluded: unknown weights are not the chosen
// ones. The standing rule (Limit.acceptsQuant) means "any of these I would accept", and
// an unstated quant passes it: a rule should not silently blacklist every station that
// omitted a label. Two questions, two answers; TestAbsenceIsReadDifferentlyByRowAndRule
// pins both so neither drifts to match the other by accident.
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
// This is what makes the preference a rule rather than a view. The dial filter cannot
// protect a turn nobody is watching: the agent picks a model and runs, and it never
// consults what the browse list happened to be showing. Every routing path INSIDE the
// booth - the agent, the live proxy, and the in-channel chat - goes through these options,
// so naming the disallowed stations here is what binds them.
//
// KNOWN GAPS. Exclusions are derived from m.bands, i.e. the last /discover scan: a
// station that registered after that scan, or an agent turn fired before the first one,
// is not excluded. That is inherent to resolving the rule client-side.
//
// And the standalone CLI (`roger use` outside the TUI) does NOT yet apply this.
// client.ProxyOptions carries ExcludeNodes, but the CLI path builds its options from the
// persisted limit's price/tps fields only and never resolves the quant rule to station
// ids - that needs a discover scan the CLI does not currently make. Stated here rather
// than implied away, because a rule that silently does not bind is worse than one the
// operator knows the edge of.
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

// chatExcludes is routeExcludes for the band the operator is actually CONNECTED to.
//
// The in-channel chat has no quote of its own, and m.q holds whatever row was last priced
// - which can be a different band the operator esc'd out of. Resolving from m.connected
// keeps the exclusions about the conversation actually happening.
func (m model) chatExcludes() []string {
	if m.connected == nil {
		return nil
	}
	// Bands are grouped by (model, quant), so the model alone names SEVERAL rows and the
	// first one is not necessarily the one this conversation is on. Match the connected
	// offer's quant too - the test that pinned this connected to the Q4 row and got the
	// BF16 row's exclusions back when it matched by model alone.
	for _, b := range m.bands {
		if b.model == m.connected.Model && b.quant == m.connected.Quant {
			return m.routeExcludes(b)
		}
	}
	// No band row for it (a direct or private connection): the standing preference is
	// still a rule, so apply that half rather than nothing.
	return m.prefExcludes(m.connected.Model)
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
