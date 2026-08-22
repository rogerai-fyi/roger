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
