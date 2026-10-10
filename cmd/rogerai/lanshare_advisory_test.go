package main

// `roger --lan share` serves this LAN for free: the "below the suggested minimum ... public traffic"
// advisory is about being picked by the market, so it has nothing to say there (found 2026-10-10).

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/detect"
)

func TestLANShareSaysNothingAboutPublicTraffic(t *testing.T) {
	s := &lanShareBDD{t: t}
	s.reset()
	t.Cleanup(s.after)
	stubPreflight(t, weakBox())
	t.Setenv("ROGER_LAN", "1")
	require.NoError(t, s.machineServes("qwen3-8b"))
	require.NoError(t, s.addTower("tower-a", true))
	s.runFor = 800 * time.Millisecond
	require.NoError(t, s.runShare("roger share"))
	require.NotContains(t, s.out, "suggested minimum", "the market advisory has no place in LAN sharing:\n%s", s.out)
	require.True(t, strings.Contains(s.out, "LAN") || strings.Contains(s.out, "lan"), "it is a LAN share:\n%s", s.out)
}

func TestMarketShareStillCarriesTheAdvisory(t *testing.T) {
	require.Contains(t, shareAdvisoryFor(false, detect.Assess(weakBox())), "suggested minimum")
	require.Empty(t, shareAdvisoryFor(true, detect.Assess(weakBox())))
}
