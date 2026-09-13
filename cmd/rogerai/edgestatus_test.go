package main

// Unit spec for edgeSelfStatus: the three facts are read from the SAME files the CLI
// writes (`roger edge authority local`, `roger edge authority allow`, `roger edge
// enroll`), driven through the real dispatch in an isolated config dir.

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edge"
)

func runEdgeCLI(t *testing.T, args ...string) string {
	t.Helper()
	edgeStdin = strings.NewReader("")
	out, err := captureEdgeStdout(func() error { return dispatch(loadConfig(), args) })
	require.NoError(t, err, "roger %s:\n%s", strings.Join(args, " "), out)
	return out
}

func TestEdgeSelfStatusFreshMachine(t *testing.T) {
	useTempConfig(t)
	st := edgeSelfStatus(nil, edgeDiscoveryFacts{Enabled: true, Interval: 30 * time.Second})
	require.Empty(t, st.Err)
	require.False(t, st.Enrolled)
	require.False(t, st.LoggedIn)
	require.Equal(t, "Core", st.Authority)
	require.False(t, st.AuthorityLocal)
	require.False(t, st.AuthorityHere)
	require.Equal(t, edge.DiscoveryScanning, st.Discovery)
	require.EqualValues(t, 30, st.IntervalS)
	require.Zero(t, st.LastPass)
}

func TestEdgeSelfStatusDiscoveryStates(t *testing.T) {
	useTempConfig(t)
	off := edgeSelfStatus(nil, edgeDiscoveryFacts{Enabled: false, Interval: 30 * time.Second})
	require.Equal(t, edge.DiscoveryOff, off.Discovery)

	un := edgeSelfStatus(nil, edgeDiscoveryFacts{Enabled: true, Unavailable: true, Interval: 30 * time.Second})
	require.Equal(t, edge.DiscoveryUnavailable, un.Discovery)

	at := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	seen := edgeSelfStatus(nil, edgeDiscoveryFacts{Enabled: true, Interval: 30 * time.Second, LastPass: at, Found: edge.PassSummary(0, 2)})
	require.Equal(t, at.Unix(), seen.LastPass)
	require.Equal(t, "2 candidates seen", seen.Found)

	env := edgeDiscoveryFactsFromEnv()
	require.Equal(t, edge.DefaultInterval, env.Interval)
}

func TestEdgeSelfStatusLocalAuthorityHereAndEnrolled(t *testing.T) {
	useTempConfig(t)
	t.Setenv("ROGER_BROKER", "http://127.0.0.1:1")
	runEdgeCLI(t, "edge", "authority", "local", "shed")

	st := edgeSelfStatus(nil, edgeDiscoveryFacts{Enabled: true, Interval: 30 * time.Second, AuthorityAddr: "http://192.168.1.10:8791"})
	require.Empty(t, st.Err)
	require.True(t, st.AuthorityLocal)
	require.True(t, st.AuthorityHere)
	require.Equal(t, "shed", st.Authority)
	require.Equal(t, "http://192.168.1.10:8791", st.AuthorityAddr)
	require.Equal(t, 1, st.Allowed, "designating admits this machine's own user key")
	require.Equal(t, "roger edge enroll <name> --authority http://192.168.1.10:8791", st.EnrollAgainstLine())

	runEdgeCLI(t, "edge", "authority", "allow", strings.Repeat("ab", 32))
	require.Equal(t, 2, edgeSelfStatus(nil, edgeDiscoveryFacts{Enabled: true}).Allowed)

	// Enrolling on the authority machine needs no login: the Edge is rooted here.
	runEdgeCLI(t, "edge", "enroll", "workshop")
	est, err := loadEdgeState()
	require.NoError(t, err)
	st = edgeSelfStatus(est.fleet, edgeDiscoveryFacts{Enabled: true})
	require.True(t, st.Enrolled)
	require.Equal(t, "workshop", st.Name, "the owner's name, from the fleet, not the raw node id")
	require.Contains(t, st.MachineLine(), "workshop · enrolled")
	// the whole `roger edge` listing is the enrolled-alone case: no enroll hint under THIS MACHINE
	out := runEdgeCLI(t, "edge")
	for _, ln := range strings.Split(out, "\n") {
		if strings.Contains(ln, "THIS MACHINE") {
			require.NotContains(t, ln, "roger edge enroll")
		}
	}
}

func TestEdgeSelfStatusUnreadableRecordClaimsNothing(t *testing.T) {
	useTempConfig(t)
	// A FILE where the Edge auth directory should be: every read under it fails.
	dir := edgeAuthDir()
	require.NoError(t, os.MkdirAll(filepath.Dir(dir), 0o700))
	require.NoError(t, os.WriteFile(dir, []byte("not a directory"), 0o600))
	st := edgeSelfStatus(nil, edgeDiscoveryFacts{Enabled: true})
	require.NotEmpty(t, st.Err)
	require.Empty(t, st.Authority)
	require.False(t, st.Enrolled)
	// and `roger edge` prints the status as unread rather than as Core
	out := runEdgeCLI(t, "edge")
	require.Contains(t, out, "STATUS")
	require.Contains(t, out, "could not be read")
	require.NotContains(t, out, "AUTHORITY     Core")
}

func TestEdgeAuthorityURL(t *testing.T) {
	require.Equal(t, "", edgeAuthorityURL(nil))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	got := edgeAuthorityURL(ln)
	if ip := edgeLANIP(); ip == "" {
		require.Equal(t, "", got, "no LAN route, no address to hand out")
	} else {
		require.Equal(t, "http://"+net.JoinHostPort(ip, itoa(ln.Addr().(*net.TCPAddr).Port)), got)
	}
}

func TestEdgeHostStatusReportsTheEngineNotTheKnob(t *testing.T) {
	useTempConfig(t)
	h, err := newEdgeHost("gentle-mongoose-93")
	require.NoError(t, err)
	// Never armed: discovery is reported off however the environment reads, because no
	// engine exists to be scanning.
	st := h.status()
	require.Equal(t, edge.DiscoveryOff, st.Discovery)
	require.EqualValues(t, edge.DefaultInterval/time.Second, st.IntervalS)
	require.Equal(t, "Core", st.Authority)

	h.mu.Lock()
	h.armed, h.every, h.lastPass, h.lastFound = true, 30*time.Second, time.Now().Add(-12*time.Second), edge.PassSummary(0, 0)
	h.mu.Unlock()
	st = h.status()
	require.Equal(t, edge.DiscoveryScanning, st.Discovery)
	require.EqualValues(t, 30, st.IntervalS)
	require.Contains(t, st.DiscoveryLine(time.Now()), "last pass 12s ago, nothing answered")
}
