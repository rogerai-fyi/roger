package client

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestPickAlternativeMatchesTheBareModelAndKeepsFree: `roger use m:free` (the session model
// carries the variant) still re-picks among /discover's bare "m" offers, and :free keeps its
// meaning - only an offer that costs the caller nothing qualifies. Other variants are sorts,
// not filters.
func TestPickAlternativeMatchesTheBareModelAndKeepsFree(t *testing.T) {
	offers := []Offer{
		{NodeID: "paid", Model: "m", PriceIn: 0.5, PriceOut: 1, Online: true, TPS: 300},
		{NodeID: "zero", Model: "m", Online: true, TPS: 100},
		{NodeID: "promo", Model: "m", PriceIn: 0.5, PriceOut: 1, FreeNow: true, Online: true, TPS: 50},
	}
	got, ok := pickAlternative(offers, Criteria{Model: "m:free"}, map[string]bool{"zero": true})
	require.True(t, ok, "a free alternative exists")
	require.Equal(t, "promo", got, ":free re-picks only an offer that is free now")
	_, ok = pickAlternative(offers, Criteria{Model: "m:free"}, map[string]bool{"zero": true, "promo": true})
	require.False(t, ok, ":free never falls over to a paid offer")
	got, ok = pickAlternative(offers, Criteria{Model: "m:nitro"}, nil)
	require.True(t, ok, "a sort variant re-picks among every offer of the bare model")
	require.NotEmpty(t, got)
}
