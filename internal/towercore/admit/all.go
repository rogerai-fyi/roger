package admit

import "sort"

// All lists every Tower on the registry, for the admin's approval queue: pending and
// quarantined first (the ones waiting on a decision), then the live states, then the
// terminal ones - newest enrollment first within each group. Deterministic order,
// because an approval UI that reshuffles under the admin's cursor invites approving the
// wrong row.
func (r *Registry) All() []Tower {
	out, err := r.store.AllTowers()
	if err != nil {
		return nil
	}
	sort.Slice(out, func(i, j int) bool {
		wi, wj := waitingRank(out[i].State), waitingRank(out[j].State)
		if wi != wj {
			return wi < wj
		}
		if !out[i].EnrolledAt.Equal(out[j].EnrolledAt) {
			return out[i].EnrolledAt.After(out[j].EnrolledAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// waitingRank orders the states by how much they want the admin's attention.
func waitingRank(s State) int {
	switch s {
	case StateQuarantine, StatePending:
		return 0
	case StateActive, StateDraining, StateSuspended:
		return 1
	}
	return 2
}
