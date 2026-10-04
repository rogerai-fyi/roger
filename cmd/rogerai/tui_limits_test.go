package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestTUILimitsRoundTripKeepsEveryKey is the regression pin for the [3] CONFIG save path: the
// host mapped only max_in/max_out/min_tps/quants, so any edit in the TUI silently erased a
// pref (and every other routing key) set from the CLI, and the default's quant rule and pref
// were dropped on load.
func TestTUILimitsRoundTripKeepsEveryKey(t *testing.T) {
	useTempConfig(t)
	full := Limit{MaxIn: 0.3, MaxOut: 3, MinTPS: 9, Quants: []string{"Q8_0"}, Pref: "fast", Require: []string{"tools"},
		ParamsB: []float64{7, 70}, MinCtx: 32768, MaxTTFTMs: 1500, TrustMin: "verified", SelfHosted: true,
		Region: []string{"eu"}, MaxCost: 0.02}
	c := loadConfig()
	c.Limits.Default = full
	c.Limits.Models = map[string]Limit{"m": full}
	require.NoError(t, saveConfig(c))

	store := tuiLimits(loadConfig())
	require.Equal(t, toTUILimit(full), store.Default, "load keeps the default's every key")
	require.Equal(t, toTUILimit(full), store.Snapshot()["m"])

	store.Save(store.Snapshot(), store.Default) // what any [3] CONFIG edit does
	got := loadConfig()
	require.Equal(t, full, got.Limits.Default)
	require.Equal(t, full, got.Limits.Models["m"])
}
