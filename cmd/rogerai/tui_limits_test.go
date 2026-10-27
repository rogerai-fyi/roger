package main

import (
	"testing"

	"rogerai.fm/roger/v6/internal/tui"

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

	// A [3] CONFIG edit that changes every key carries every key back to config.json.
	edited := Limit{MaxIn: 0.4, MaxOut: 4, MinTPS: 11, Quants: []string{"Q4_K_M"}, Pref: "cheap", Require: []string{"vision"},
		ParamsB: []float64{8, 32}, MinCtx: 16384, MaxTTFTMs: 900, TrustMin: "tier-a", SelfHosted: false,
		Region: []string{"us"}, MaxCost: 0.05}
	store.Set("m", toTUILimit(edited))
	got := loadConfig()
	require.Equal(t, edited, got.Limits.Models["m"], "every key of the edited row reaches config.json")
	require.Equal(t, full, got.Limits.Default, "the untouched default is not rewritten")
}

// TestBoothSaveKeepsAConcurrentEditToTheSameRow: a roger set-limit on the SAME row (or the
// default) while the booth is open is kept; the booth writes back only the fields it changed,
// and routes on the merged rule.
func TestBoothSaveKeepsAConcurrentEditToTheSameRow(t *testing.T) {
	useTempConfig(t)
	c := loadConfig()
	c.Limits.Default = Limit{MaxOut: 5, MinTPS: 10}
	c.Limits.Models = map[string]Limit{"m": {MaxOut: 5, MinTPS: 10}}
	require.NoError(t, saveConfig(c))
	store := tuiLimits(loadConfig())

	other := loadConfig() // another roger process: set-limit on the same row and the default
	other.Limits.Models["m"] = Limit{MaxOut: 5, MinTPS: 20}
	other.Limits.Default = Limit{MaxOut: 5, MinTPS: 30}
	require.NoError(t, saveConfig(other))

	store.Update("m", func(cur tui.Limit) tui.Limit { cur.MaxOut = 3; return cur }) // the booth edits MaxOut only
	got := loadConfig()
	require.Equal(t, Limit{MaxOut: 3, MinTPS: 20}, got.Limits.Models["m"], "the booth's field and the CLI's field both stand")
	require.Equal(t, Limit{MaxOut: 5, MinTPS: 30}, got.Limits.Default, "the default the booth never touched keeps the CLI's edit")
	require.Equal(t, 20.0, store.Snapshot()["m"].MinTPS, "the booth routes on the merged rule")
}
