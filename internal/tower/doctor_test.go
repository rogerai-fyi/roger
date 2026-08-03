package tower

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// doctor is the operator's pre-flight check. Its most important job is to state, in
// terms the operator can act on, whether this Tower can reach the public network at all
// - the Phase 1 gate is "packet capture proves no RogerAI egress", and an operator needs
// to be able to see that before they trust it.

func TestDoctorReportsStandaloneAsFullyLocal(t *testing.T) {
	c, err := ParseConfig([]byte(minimalStandalone))
	require.NoError(t, err)
	rep := Doctor(c)

	require.True(t, rep.OK, "a default standalone config is healthy: %v", rep.Problems)
	require.Equal(t, ModeStandalone, rep.Mode)
	require.False(t, rep.ReachesPublicNetwork, "standalone must report no public reachability")
	require.Empty(t, rep.PublicAuthority)
	require.True(t, rep.AllListenersLoopback)
}

func TestDoctorReportsJoinedReachability(t *testing.T) {
	c, err := ParseConfig([]byte(minimalJoined))
	require.NoError(t, err)
	rep := Doctor(c)

	require.True(t, rep.OK, "a valid joined config is healthy: %v", rep.Problems)
	require.True(t, rep.ReachesPublicNetwork)
	require.Equal(t, "https://broker.rogerai.fm", rep.PublicAuthority)
}

// A non-loopback bind is legitimate (LAN and cluster serving are supported) but must be
// reported, so an operator never exposes a Tower without being told.
func TestDoctorFlagsANonLoopbackBindAsDeliberate(t *testing.T) {
	y := minimalStandalone + "stationListener:\n  address: 0.0.0.0:7070\n"
	c, err := ParseConfig([]byte(y))
	require.NoError(t, err)
	rep := Doctor(c)

	require.False(t, rep.AllListenersLoopback)
	require.True(t, rep.OK, "explicit LAN serving is supported, not an error")
	require.NotEmpty(t, rep.Notes, "a non-loopback bind must be called out to the operator")
	joined := strings.Join(rep.Notes, " ")
	require.Contains(t, joined, "0.0.0.0:7070")
}

func TestDoctorRendersHumanReadably(t *testing.T) {
	c, err := ParseConfig([]byte(minimalStandalone))
	require.NoError(t, err)
	out := Doctor(c).String()

	require.Contains(t, out, "mode: standalone")
	require.Contains(t, out, "public network")
	require.Contains(t, strings.ToLower(out), "loopback")
}
