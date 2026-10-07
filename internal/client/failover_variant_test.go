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

// TestRepickHonorsCtxTTFTAndExclusions: a failover hint never names a station the broker
// would refuse for the window, the first-token ceiling, or an ignored station, whether the
// owner or the caller stated it. Unmeasured values pass, as on the broker.
func TestRepickHonorsCtxTTFTAndExclusions(t *testing.T) {
	offers := []Offer{
		{NodeID: "small", Model: "m", Online: true, TPS: 300, Ctx: 8192},
		{NodeID: "slow", Model: "m", Online: true, TPS: 300, Ctx: 65536, TTFTMs: 4000},
		{NodeID: "banned", Model: "m", Online: true, TPS: 300, Ctx: 65536, TTFTMs: 200},
		{NodeID: "ok", Model: "m", Online: true, TPS: 50, Ctx: 65536, TTFTMs: 300},
	}
	c := Criteria{Model: "m"}
	ownerRoutingCriteria(ProxyOptions{MinCtx: 32768, ExcludeNodes: []string{"banned"}}, &c)
	callerRoutingCriteria([]byte(`{"roger":{"max_ttft_ms":1500},"provider":{"ignore":["nope"]}}`), &c)
	got, ok := pickAlternative(offers, c, nil)
	require.True(t, ok)
	require.Equal(t, "ok", got)
}

// TestRepickCtxFloorNeedsADeclaredWindow: as on the broker, a context floor admits only a
// declared window at least that large; an estimated or unknown one is not a match.
func TestRepickCtxFloorNeedsADeclaredWindow(t *testing.T) {
	offers := []Offer{
		{NodeID: "estimated", Model: "m", Online: true, TPS: 300, Ctx: 131072, CtxEstimated: true},
		{NodeID: "unknown", Model: "m", Online: true, TPS: 300},
		{NodeID: "declared", Model: "m", Online: true, TPS: 10, Ctx: 65536},
	}
	got, ok := pickAlternative(offers, Criteria{Model: "m", MinCtx: 32768}, nil)
	require.True(t, ok)
	require.Equal(t, "declared", got)
}

// TestFreeOnlySessionRepicksAmongFreeStations: when the session binds every model free (the
// owner's free tune, F, or a profile's band:free), the failover re-pick looks only at free
// stations even if the guest named the bare model.
func TestFreeOnlySessionRepicksAmongFreeStations(t *testing.T) {
	require.Equal(t, "m:free", repickModel(ProxyOptions{FreeOnly: true}, "m"))
	require.Equal(t, "m:free", repickModel(ProxyOptions{Model: "m:free"}, "m"))
	require.Equal(t, "m:free:nitro", repickModel(ProxyOptions{FreeOnly: true}, "m:free:nitro"))
	require.Equal(t, "m", repickModel(ProxyOptions{}, "m"))
}

// TestRepickHonorsTheCallersPriceAndSpeed: a caller's own price caps and min_tps tighten the
// re-pick like the owner's, so it never hints a station the broker will skip for them.
func TestRepickHonorsTheCallersPriceAndSpeed(t *testing.T) {
	offers := []Offer{
		{NodeID: "pricey", Model: "m", Online: true, TPS: 300, PriceOut: 5},
		{NodeID: "slow", Model: "m", Online: true, TPS: 5, PriceOut: 1},
		{NodeID: "ok", Model: "m", Online: true, TPS: 50, PriceOut: 1},
	}
	c := Criteria{Model: "m"}
	callerRoutingCriteria([]byte(`{"provider":{"max_price":{"completion":2}},"roger":{"min_tps":20}}`), &c)
	got, ok := pickAlternative(offers, c, nil)
	require.True(t, ok)
	require.Equal(t, "ok", got)
}
