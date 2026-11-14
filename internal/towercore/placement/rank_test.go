package placement

// Contract: features/tower/placement_ranking.feature.

import (
	"math/rand"
	"testing"

	"github.com/stretchr/testify/require"
)

func rng() *rand.Rand { return rand.New(rand.NewSource(1)) }

func TestAHouseStationGoesToAHouseTowerWhateverTheOthers(t *testing.T) {
	c := []Candidate{
		{TowerID: "nora", Tier: TierA, Class: SameNetwork, Load: 0},
		{TowerID: "house", House: true, Tier: TierB, Class: Far, Load: 0.9},
	}
	got, reason, ok := Choose(c, true, rng())
	require.True(t, ok)
	require.Equal(t, "house", got.TowerID)
	require.Equal(t, ReasonBest, reason)
}

func TestAHouseStationFailsOverOnlyToAllowedCommunityTowers(t *testing.T) {
	c := []Candidate{
		{TowerID: "nora", Tier: TierA, Class: Far, HouseAllowed: true},
		{TowerID: "new", Tier: TierA, Class: SameNetwork, HouseAllowed: false},
	}
	got, reason, ok := Choose(c, true, rng())
	require.True(t, ok)
	require.Equal(t, "nora", got.TowerID)
	require.Equal(t, ReasonHouseFailover, reason)

	_, _, ok = Choose([]Candidate{{TowerID: "new", Tier: TierA}}, true, rng())
	require.False(t, ok, "a house Station with nowhere allowed is not placed")
}

func TestACommunityStationTiesGoToTheCommunity(t *testing.T) {
	c := []Candidate{
		{TowerID: "house", House: true, Tier: TierA, Class: SameContinent, Load: 0},
		{TowerID: "op", Tier: TierA, Class: SameContinent, Load: 0.8},
	}
	for i := 0; i < 50; i++ {
		got, reason, ok := Choose(c, false, rand.New(rand.NewSource(int64(i))))
		require.True(t, ok)
		require.Equal(t, "op", got.TowerID, "a house Tower never wins a tie, even far less loaded")
		require.Equal(t, ReasonBest, reason)
	}
}

func TestAHouseTowerWinsACommunityStationOnlyOnAMeasuredSignal(t *testing.T) {
	near := []Candidate{
		{TowerID: "house", House: true, Tier: TierA, Class: SameContinent},
		{TowerID: "nora", Tier: TierA, Class: Far},
	}
	got, reason, _ := Choose(near, false, rng())
	require.Equal(t, "house", got.TowerID)
	require.Equal(t, ReasonNearerHouse, reason)

	healthier := []Candidate{
		{TowerID: "house", House: true, Tier: TierA, Class: Far},
		{TowerID: "nora", Tier: TierB, Class: SameContinent},
	}
	got, reason, _ = Choose(healthier, false, rng())
	require.Equal(t, "house", got.TowerID)
	require.Equal(t, ReasonNearerHouse, reason)

	got, reason, _ = Choose([]Candidate{{TowerID: "house", House: true, Tier: TierA, Class: Far}}, false, rng())
	require.Equal(t, "house", got.TowerID)
	require.Equal(t, ReasonNoCommunity, reason)
}

func TestHealthThenLocality(t *testing.T) {
	got, _, _ := Choose([]Candidate{
		{TowerID: "near-b", Tier: TierB, Class: SameNetwork},
		{TowerID: "far-a", Tier: TierA, Class: Far},
	}, false, rng())
	require.Equal(t, "far-a", got.TowerID, "a trusted far Tower beats an unproven near one")

	for _, order := range [][]Class{{SameNetwork, SameContinent}, {SameContinent, Unknown}, {Unknown, Far}} {
		got, _, _ := Choose([]Candidate{
			{TowerID: "worse", Tier: TierA, Class: order[1]},
			{TowerID: "better", Tier: TierA, Class: order[0]},
		}, false, rng())
		require.Equal(t, "better", got.TowerID, "%v", order)
	}
}

func TestOneCandidateInTheBestClassIsChosenWithoutADraw(t *testing.T) {
	got, _, ok := Choose([]Candidate{{TowerID: "only", Tier: TierA, Class: Far, Load: 0.99}}, false, nil)
	require.True(t, ok)
	require.Equal(t, "only", got.TowerID)
}

func TestNoCandidatesPlacesNothing(t *testing.T) {
	_, _, ok := Choose(nil, false, rng())
	require.False(t, ok)
}

func TestTheTwoChoiceDrawKeepsLoadEven(t *testing.T) {
	// Five Towers in one class; place 1000 Stations, each raising its Tower's load.
	counts := make([]int, 5)
	r := rand.New(rand.NewSource(7))
	for n := 0; n < 1000; n++ {
		c := make([]Candidate, 5)
		for i := range c {
			c[i] = Candidate{TowerID: string(rune('a' + i)), Tier: TierA, Class: SameContinent, Load: float64(counts[i]) / 1000}
		}
		got, _, ok := Choose(c, false, r)
		require.True(t, ok)
		counts[got.TowerID[0]-'a']++
	}
	min, max := counts[0], counts[0]
	for _, v := range counts {
		if v < min {
			min = v
		}
		if v > max {
			max = v
		}
	}
	require.LessOrEqual(t, max-min, 3, "two choices keep the spread tiny: %v", counts)
}

func TestEqualLoadIsBrokenAtRandomNotById(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		got, _, _ := Choose([]Candidate{
			{TowerID: "aaa", Tier: TierA, Class: Unknown, Load: 0.5},
			{TowerID: "zzz", Tier: TierA, Class: Unknown, Load: 0.5},
		}, false, rand.New(rand.NewSource(int64(i))))
		seen[got.TowerID] = true
	}
	require.True(t, seen["aaa"] && seen["zzz"], "both win sometimes")
}

func TestStrictlyBetter(t *testing.T) {
	cur := Candidate{Tier: TierA, Class: SameContinent}
	require.True(t, StrictlyBetter(Candidate{Tier: TierA, Class: SameNetwork}, cur, false))
	require.False(t, StrictlyBetter(Candidate{Tier: TierA, Class: SameContinent, Load: 0}, Candidate{Tier: TierA, Class: SameContinent, Load: 0.9}, false), "load alone is not better")
	require.True(t, StrictlyBetter(Candidate{Tier: TierA, Class: Far}, Candidate{Tier: TierB, Class: SameNetwork}, false))
	require.False(t, StrictlyBetter(Candidate{Tier: TierB, Class: SameNetwork}, Candidate{Tier: TierA, Class: Far}, false))
	// Group: a house Station on a community Tower, a house Tower is better.
	require.True(t, StrictlyBetter(Candidate{House: true, Tier: TierB, Class: Far}, Candidate{Tier: TierA, Class: SameNetwork}, true))
	// A community Station on a house Tower: a community Tower of the same tier and class is better.
	require.True(t, StrictlyBetter(Candidate{Tier: TierA, Class: SameContinent}, Candidate{House: true, Tier: TierA, Class: SameContinent}, false))
	// ...but a house Tower of the same tier and class is not better than a community one.
	require.False(t, StrictlyBetter(Candidate{House: true, Tier: TierA, Class: SameContinent}, Candidate{Tier: TierA, Class: SameContinent}, false))
}
