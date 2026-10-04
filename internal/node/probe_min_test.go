package node

// probe_min_test.go: the saved per-model minimum probe interval (config.json share_prices
// probe_min, seeded through Config.ProbeMin) rides every share the controller starts,
// including auto-started ones, and a model with none declared registers without it.

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/agent"
)

func TestControllerCarriesSavedProbeMin(t *testing.T) {
	c := newCtrl(t, Config{ProbeMin: map[string]time.Duration{"free-1": 6 * time.Hour}})
	real := startAgent
	t.Cleanup(func() { startAgent = real })
	got := map[string]time.Duration{}
	startAgent = func(cfg agent.Config) (*agent.Session, error) {
		got[cfg.Model] = cfg.ProbeMin
		return real(cfg)
	}

	for _, m := range []string{"free-1", "free-2"} {
		res := c.ToggleOnAir(m)
		require.NoError(t, res.Err)
	}
	t.Cleanup(func() { c.StopAll() })
	require.Equal(t, 6*time.Hour, got["free-1"])
	require.Zero(t, got["free-2"], "a model with no saved probe_min registers without one")
}
