package edge

// Unit spec for SelfStatus wording: the three facts an empty Edge states about THIS
// machine (features/edge/empty_edge.feature). The sentences are asserted here ONCE, and
// the TUI, the console and the CLI print these strings rather than composing their own.

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSelfStatusMachineLine(t *testing.T) {
	cases := []struct {
		name string
		st   SelfStatus
		want []string
		not  []string
	}{
		{"fresh", SelfStatus{}, []string{"not enrolled", "roger edge enroll <name>"}, nil},
		{"enrolled", SelfStatus{Enrolled: true, Name: "workshop"}, []string{"workshop", "enrolled"}, []string{"roger edge enroll"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.st.MachineLine()
			for _, w := range c.want {
				require.Contains(t, got, w)
			}
			for _, n := range c.not {
				require.NotContains(t, got, n)
			}
		})
	}
}

func TestSelfStatusAuthorityLines(t *testing.T) {
	cases := []struct {
		name string
		st   SelfStatus
		want []string
		not  []string
	}{
		{"core, no login", SelfStatus{Authority: "Core"},
			[]string{"Core", "needs the network, to reach Core", "roger login", "roger edge authority local <name>"}, nil},
		{"core, logged in, not enrolled", SelfStatus{Authority: "Core", LoggedIn: true},
			[]string{"Core", "roger edge authority local <name>"}, []string{"roger login"}},
		{"core, enrolled", SelfStatus{Authority: "Core", LoggedIn: true, Enrolled: true},
			[]string{"Core"}, []string{"roger login", "authority local"}},
		{"local here", SelfStatus{Authority: "shed", AuthorityLocal: true, AuthorityHere: true, Allowed: 2},
			[]string{"shed", "this machine IS the authority", "no network beyond this LAN", "2 machines may enroll", "roger edge authority allow <user key>"}, []string{"roger login"}},
		{"local here, one allowed", SelfStatus{Authority: "shed", AuthorityLocal: true, AuthorityHere: true, Allowed: 1},
			[]string{"1 machine may enroll"}, nil},
		{"local elsewhere", SelfStatus{Authority: "shed", AuthorityLocal: true},
			[]string{"shed", "no network beyond this LAN"}, []string{"IS the authority", "allow <user key>"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := strings.Join(c.st.AuthorityLines(), "\n")
			for _, w := range c.want {
				require.Contains(t, got, w)
			}
			for _, n := range c.not {
				require.NotContains(t, got, n)
			}
		})
	}
}

func TestSelfStatusDiscoveryLine(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		st   SelfStatus
		want []string
		not  []string
	}{
		{"off", SelfStatus{Discovery: DiscoveryOff}, []string{"off", "ROGERAI_EDGE_DISCOVERY"}, []string{"scanning"}},
		{"unavailable", SelfStatus{Discovery: DiscoveryUnavailable}, []string{"unavailable"}, []string{"scanning"}},
		{"idle: no engine in this process", SelfStatus{Discovery: DiscoveryIdle, IntervalS: 30},
			[]string{"not scanning in this process", "roger edge scan", "every 30s"}, []string{"scanning this network"}},
		{"scanning, no pass yet", SelfStatus{Discovery: DiscoveryScanning, IntervalS: 30},
			[]string{"scanning this network every 30s", "no pass has completed yet"}, []string{"ago"}},
		{"scanning, nothing answered", SelfStatus{Discovery: DiscoveryScanning, IntervalS: 30, LastPass: now.Add(-12 * time.Second).Unix(), Found: PassSummary(0, 0)},
			[]string{"last pass 12s ago", "nothing answered"}, nil},
		{"scanning, found", SelfStatus{Discovery: DiscoveryScanning, IntervalS: 30, LastPass: now.Add(-5 * time.Second).Unix(), Found: PassSummary(1, 2)},
			[]string{"last pass 5s ago", "1 node verified", "2 candidates seen"}, []string{"nothing answered"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.st.DiscoveryLine(now)
			for _, w := range c.want {
				require.Contains(t, got, w)
			}
			for _, n := range c.not {
				require.NotContains(t, got, n)
			}
		})
	}
}

func TestSelfStatusAddNode(t *testing.T) {
	fresh := SelfStatus{Authority: "Core"}
	lines := strings.Join(fresh.AddNodeLines(), "\n")
	require.Contains(t, lines, "run RogerAI on another machine on this network")
	require.Contains(t, lines, "CANDIDATE")
	require.Contains(t, lines, "a adopts it")
	require.Contains(t, lines, "roger edge enroll <name> --authority <address>")

	here := SelfStatus{Authority: "shed", AuthorityLocal: true, AuthorityHere: true, AuthorityAddr: "http://192.168.1.10:8791"}
	require.Equal(t, "roger edge enroll <name> --authority http://192.168.1.10:8791", here.EnrollAgainstLine())
	require.Contains(t, strings.Join(here.AddNodeLines(), "\n"), "--authority http://192.168.1.10:8791")
}

func TestPassSummary(t *testing.T) {
	require.Equal(t, "nothing answered", PassSummary(0, 0))
	require.Equal(t, "1 candidate seen", PassSummary(0, 1))
	require.Equal(t, "2 nodes verified", PassSummary(2, 0))
	require.Equal(t, "1 node verified, 2 candidates seen", PassSummary(1, 2))
}
