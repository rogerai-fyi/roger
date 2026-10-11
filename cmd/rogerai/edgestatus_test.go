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
	require.Equal(t, "roger join http://192.168.1.10:8791", st.JoinAgainstLine())

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
	edgeScans = newScanScopes() // a scope another test left open would read as a scan asked for
	t.Cleanup(func() { edgeScans = newScanScopes() })
	h, err := newEdgeHost("gentle-mongoose-93")
	require.NoError(t, err)
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	h.statusNow = func() time.Time { return now }
	// Never armed: discovery is reported off however the environment reads, because no
	// engine exists to be scanning.
	st := h.status()
	require.Equal(t, edge.DiscoveryOff, st.Discovery)
	require.EqualValues(t, edge.DefaultInterval/time.Second, st.IntervalS)
	require.Equal(t, "Core", st.Authority)

	// Armed but not browsing: a machine outside an Edge that nobody has asked to scan
	// (scan_on_request.feature) is not scanning, and does not claim to be.
	h.mu.Lock()
	h.armed, h.every, h.lastPass, h.lastFound = true, 30*time.Second, time.Now().Add(-12*time.Second), edge.PassSummary(0, 0)
	h.mu.Unlock()
	now = now.Add(edgeStatusTTL)
	st = h.status()
	require.Equal(t, edge.DiscoveryIdle, st.Discovery)

	h.mu.Lock()
	h.browsing = true
	h.mu.Unlock()
	now = now.Add(edgeStatusTTL) // arming happens at launch, before any viewer polls
	st = h.status()
	require.Equal(t, edge.DiscoveryScanning, st.Discovery)
	require.False(t, st.ScansOnRequest, "browsing as a member, not on request")
	require.EqualValues(t, 30, st.IntervalS)
	require.Contains(t, st.DiscoveryLine(time.Now()), "last pass 12s ago, nothing answered")
}

func TestEdgeHostStatusIsCachedUnderAPollingViewer(t *testing.T) {
	useTempConfig(t)
	h, err := newEdgeHost("gentle-mongoose-93")
	require.NoError(t, err)
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	h.statusNow = func() time.Time { return now }
	first := h.status()
	// A change on disk within the TTL is not seen: the console polls once a second and
	// the status opens the authority's root each real read.
	runEdgeCLI(t, "edge", "authority", "local", "shed")
	require.Equal(t, first, h.status(), "within the TTL the cached status is served")
	now = now.Add(edgeStatusTTL)
	require.True(t, h.status().AuthorityHere, "past the TTL the status is really read again")
}

// TestEdgeHostStatusReadsAnOpenScanBeforeTheBrowseStarts pins scan_on_request.feature:100 at
// the host: the frame [3] draws the moment it opens its scan must say it scans while the screen
// is open, even though the browse itself starts on the host's next wake. A status cached before
// the scope opened is never served after it.
func TestEdgeHostStatusReadsAnOpenScanBeforeTheBrowseStarts(t *testing.T) {
	useTempConfig(t)
	edgeScans = newScanScopes()
	t.Cleanup(func() { edgeScans = newScanScopes() })
	h, err := newEdgeHost("gentle-mongoose-93")
	require.NoError(t, err)
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	h.statusNow = func() time.Time { return now }
	h.mu.Lock()
	h.armed, h.every = true, 30*time.Second
	h.mu.Unlock()
	require.Equal(t, edge.DiscoveryIdle, h.status().Discovery, "nobody asked: not scanning")

	end := edgeScans.Begin(scanEdge) // [3] opens; the host has not woken yet (browsing is false)
	st := h.status()                 // same instant: within the TTL of the idle reading
	require.Equal(t, edge.DiscoveryScanning, st.Discovery, "an open scope is never reported as idle")
	require.True(t, st.ScansOnRequest)
	require.Contains(t, st.DiscoveryLine(now), "while this screen is open")

	end()
	require.Equal(t, edge.DiscoveryIdle, h.status().Discovery, "the scope closed: the idle reading returns")
}
