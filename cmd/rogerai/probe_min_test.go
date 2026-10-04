package main

// probe_min_test.go specs the CLI half of the operator-declared minimum probe interval:
//   - `roger share --probe-min <duration>` rides cfgRun.ProbeMin (negative is refused);
//   - config.json share_prices[model].probe_min (a Go duration string) seeds the same field
//     for headless `roger share` (an explicit flag wins) and, through tuiHooks, for TUI and
//     auto-started shares;
//   - the flag's help states the 24h cap and that verification lapses between probes.
// Real config files in an isolated dir + the existing agentStart capture seam; no mocks.

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestShareProbeMinReachesTheAgent(t *testing.T) {
	for _, tc := range []struct {
		name    string
		saved   string
		flags   []string
		want    time.Duration
		wantErr string
	}{
		{name: "undeclared", want: 0},
		{name: "the flag", flags: []string{"--probe-min", "6h"}, want: 6 * time.Hour},
		{name: "the saved share config", saved: "6h", want: 6 * time.Hour},
		{name: "an explicit flag wins over the saved value", saved: "6h", flags: []string{"--probe-min", "90m"}, want: 90 * time.Minute},
		{name: "an explicit zero turns a saved value off", saved: "6h", flags: []string{"--probe-min", "0"}, want: 0},
		{name: "a negative flag is refused", flags: []string{"--probe-min", "-1h"}, wantErr: "--probe-min"},
		{name: "an unparsable saved value is refused", saved: "six hours", wantErr: "probe_min"},
		{name: "a negative saved value is refused", saved: "-5m", wantErr: "probe_min"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			useTempConfig(t)
			got := captureShareConfig(t)
			cfg := config{Broker: "https://b", User: "u"}
			if tc.saved != "" {
				cfg.Prices = map[string]SharePrice{"claude-code": {ProbeMin: tc.saved}}
			}
			args := append([]string{"claude-code", "--upstream", "http://127.0.0.1:1234/v1"}, tc.flags...)
			if tc.wantErr != "" {
				// Refused before go-live, so there is no share worker to join (runShare's helper).
				require.ErrorContains(t, cmdShare(cfg, args), tc.wantErr)
				return
			}
			require.NoError(t, runShare(t, cfg, args))
			require.Equal(t, tc.want, got.ProbeMin)
		})
	}
}

func TestShareProbeMinConfigRoundTrip(t *testing.T) {
	useTempConfig(t)
	on := true
	require.NoError(t, saveConfig(config{Broker: "https://b", User: "u", Prices: map[string]SharePrice{
		"claude-code": {PriceOut: 3, AutoStart: &on, ProbeMin: "6h"},
		"local":       {PriceOut: 1},
	}}))
	raw, err := os.ReadFile(configPath())
	require.NoError(t, err)
	require.Equal(t, 1, strings.Count(string(raw), `"probe_min"`), "an unset probe_min must stay omitted:\n%s", raw)
	require.Equal(t, "6h", loadConfig().Prices["claude-code"].ProbeMin)
}

func TestTuiHooksSeedSavedProbeMin(t *testing.T) {
	useTempConfig(t)
	h := tuiHooks(config{Prices: map[string]SharePrice{
		"claude-code":      {ProbeMin: "6h"},
		"claude-code-opus": {ProbeMin: "12h"},
		"typo":             {ProbeMin: "banana"}, // never reaches the broker
		"negative":         {ProbeMin: "-1h"},
		"plain":            {PriceOut: 1},
	}})
	require.Equal(t, map[string]time.Duration{"claude-code": 6 * time.Hour, "claude-code-opus": 12 * time.Hour}, h.SavedProbeMin)
	require.Nil(t, tuiHooks(config{}).SavedProbeMin)
}

func TestShareProbeMinHelpDisclosesTheTrade(t *testing.T) {
	for _, want := range []string{"capped at 24h", "verification lapses between probes"} {
		require.Contains(t, probeMinUsage, want)
	}
}
