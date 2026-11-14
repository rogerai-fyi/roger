package placement

import (
	"math/rand"
	"sort"
)

// Tier is a Tower's health tier: A unless its last two health checks both failed or its
// reputation verdict is not clean. Lower is better.
type Tier int

const (
	TierA Tier = iota
	TierB
)

// Candidate is one eligible Tower as placement sees it. The caller has already filtered out
// every Tower that cannot take this Station (placement_ranking.feature, "A Tower that cannot
// take this Station is not a candidate"); HouseAllowed says whether a community Tower's trust
// stage lets it hold a house Station, with room under its cap.
type Candidate struct {
	TowerID      string
	House        bool
	HouseAllowed bool
	Tier         Tier
	Class        Class
	Load         float64 // live attached Stations / Station capacity
}

// Reason is why a placement landed where it did, as recorded on station_placed.
type Reason string

const (
	ReasonBest          Reason = "best"
	ReasonHouseFailover Reason = "house failover"
	ReasonNearerHouse   Reason = "nearer house tower"
	ReasonNoCommunity   Reason = "no community tower"
)

// rankKey orders candidates: health tier, then locality class.
func rankKey(c Candidate) [2]int { return [2]int{int(c.Tier), int(c.Class)} }

func less(a, b [2]int) bool { return a[0] < b[0] || (a[0] == b[0] && a[1] < b[1]) }

// best returns the candidates in the best tier and class.
func best(cs []Candidate) []Candidate {
	if len(cs) == 0 {
		return nil
	}
	sorted := append([]Candidate(nil), cs...)
	sort.SliceStable(sorted, func(i, j int) bool { return less(rankKey(sorted[i]), rankKey(sorted[j])) })
	top := rankKey(sorted[0])
	n := 1
	for n < len(sorted) && rankKey(sorted[n]) == top {
		n++
	}
	return sorted[:n]
}

// draw is the two-choice draw on load: two distinct candidates at random, the less loaded wins,
// a tie is a coin. One candidate needs no draw.
func draw(cs []Candidate, rng *rand.Rand) Candidate {
	if len(cs) == 1 || rng == nil {
		return cs[0]
	}
	i := rng.Intn(len(cs))
	j := rng.Intn(len(cs) - 1)
	if j >= i {
		j++
	}
	a, b := cs[i], cs[j]
	switch {
	case a.Load < b.Load:
		return a
	case b.Load < a.Load:
		return b
	case rng.Intn(2) == 0:
		return a
	}
	return b
}

func split(cs []Candidate, house bool) []Candidate {
	var out []Candidate
	for _, c := range cs {
		if c.House == house {
			out = append(out, c)
		}
	}
	return out
}

// Choose places a Station among the eligible candidates.
//
// A house Station goes to the best house Tower; with none, to the best community Tower whose
// stage allows house Stations ("house failover"); with none of those, nowhere. A community
// Station is ranked over every Tower, and a house Tower wins it only when it is in a strictly
// better tier or class than every community Tower: within the best tier and class the draw is
// over community Towers alone whenever any is there.
func Choose(cs []Candidate, houseStation bool, rng *rand.Rand) (Candidate, Reason, bool) {
	if houseStation {
		if top := best(split(cs, true)); len(top) > 0 {
			return draw(top, rng), ReasonBest, true
		}
		var allowed []Candidate
		for _, c := range cs {
			if !c.House && c.HouseAllowed {
				allowed = append(allowed, c)
			}
		}
		if top := best(allowed); len(top) > 0 {
			return draw(top, rng), ReasonHouseFailover, true
		}
		return Candidate{}, "", false
	}
	top := best(cs)
	if len(top) == 0 {
		return Candidate{}, "", false
	}
	if community := split(top, false); len(community) > 0 {
		return draw(community, rng), ReasonBest, true
	}
	reason := ReasonNearerHouse
	if len(split(cs, false)) == 0 {
		reason = ReasonNoCommunity
	}
	return draw(top, rng), reason, true
}

// StrictlyBetter reports whether candidate is a strictly better home than current for a Station
// (station_mobility.feature, "What counts as strictly better"): the right group for the Station,
// then a better tier, then a better class. Load alone never is.
func StrictlyBetter(candidate, current Candidate, houseStation bool) bool {
	if houseStation && candidate.House != current.House {
		return candidate.House
	}
	ck, cur := rankKey(candidate), rankKey(current)
	if less(ck, cur) {
		return true
	}
	// A community Station on a house Tower: a community Tower in the same tier and class is
	// better, because ties go to the community.
	return !houseStation && ck == cur && current.House && !candidate.House
}
