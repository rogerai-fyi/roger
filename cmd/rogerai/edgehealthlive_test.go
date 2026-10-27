package main

// `roger edge health --live` opens the Limoni health screen over the same fleet read the table
// uses; the TUI's [3] "Health" key runs it as a child process, the way a guest operator is run.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/fleetview"
)

func TestEdgeHealthLiveOpensTheScreenOverTheRealFleetRead(t *testing.T) {
	useTempConfig(t)
	edgeWriteAuth(t, "owner")
	var got fleetview.HealthSnapshot
	ran := false
	prev := edgeRunHealthScreen
	edgeRunHealthScreen = func(ctx context.Context, src fleetview.HealthSource) error {
		ran = true
		snap, err := src()
		require.NoError(t, err)
		got = snap
		return nil
	}
	t.Cleanup(func() { edgeRunHealthScreen = prev })
	out, code := edgeRun(t, "edge", "health", "--live")
	require.Equal(t, 0, code, out)
	require.True(t, ran)
	require.NotNil(t, got.Rules, "the screen gets the rules in force (the defaults, at least)")
	require.NotEmpty(t, got.Rules)
}
