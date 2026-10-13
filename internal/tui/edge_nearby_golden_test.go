package tui

// Golden frames for the NEARBY rows (features/edge/empty_edge_simple.feature §7) at the three widths
// the screen meets: the empty state with two devices pinging (one invited, one stale), and the
// enrolled map with the same rows under the members. UPDATE_GOLDEN=1 rewrites them.

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/store"
)

func nearbyGoldenModel(t *testing.T, w int, enrolled bool) model {
	t.Helper()
	lipgloss.SetColorProfile(termenv.Ascii)
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	fl := edge.NewFleet(store.NewMem(), "acct-1")
	fl.SetClock(func() time.Time { return now })
	self := edge.SelfStatus{Authority: "Core", Discovery: edge.DiscoveryScanning, ScansOnRequest: true, IntervalS: 30,
		LastPass: now.Unix(), Nearby: 2}
	if enrolled {
		self = edge.SelfStatus{Root: edge.RootLocal, Authority: "shed", AuthorityLocal: true, AuthorityHere: true,
			AuthorityAddr: "http://192.168.1.5:8791", Enrolled: true, Name: "shed", Discovery: edge.DiscoveryScanning, IntervalS: 30,
			LastPass: now.Unix(), Nearby: 3, NearbyInEdge: 1}
		_, err := fl.Enroll(store.EdgeNode{ID: "n_" + strings.Repeat("g", 48), Name: "garage", Kind: "host", Presence: "VERIFIED", LastSeen: now.Unix() - 20})
		require.NoError(t, err)
	}
	cands := []store.EdgeNode{
		{ID: "n_8eda6e1a92c9" + strings.Repeat("0", 36), Name: "gentle-ibex-14", Kind: "mobile", Presence: "CANDIDATE", LastSeen: now.Add(-12 * time.Second).Unix()},
		{ID: "n_ab12cd34ef56" + strings.Repeat("1", 36), Name: "deskside", Kind: "host", Pin: strings.Repeat("ab", 32), Presence: "CANDIDATE", LastSeen: now.Add(-20 * time.Minute).Unix()},
	}
	h := Hooks{Station: "gm-93", EdgeFleet: fl, EdgeSelf: self.Name,
		EdgeCandidates: func() []store.EdgeNode { return cands },
		EdgeStatus:     func() edge.SelfStatus { return self },
		EdgeInviteFor: func(n store.EdgeNode) (string, time.Time, error) {
			return "roger://join?kind=edge&for=" + n.ID, now.Add(15 * time.Minute), nil
		},
		EdgeAdopt: func(id, name string) error { return nil },
	}
	var tm tea.Model = NewWithHooks("http://b", "t", nil, h)
	tm, _ = tm.Update(tea.WindowSizeMsg{Width: w, Height: 44})
	m := tm.(model)
	m.edgeClock = func() time.Time { return now }
	m.enterEdge()
	m.edge.nearby.invited = map[string]bool{cands[0].ID: true}
	return m
}

func TestNearbyRowsGolden(t *testing.T) {
	t.Cleanup(func() { lipgloss.SetColorProfile(termenv.Ascii) })
	for _, w := range []int{100, 80, 60} {
		for _, enrolled := range []bool{false, true} {
			m := nearbyGoldenModel(t, w, enrolled)
			out := stripANSI(m.edgeView(w))
			for _, ln := range strings.Split(out, "\n") {
				require.LessOrEqualf(t, lipgloss.Width(ln), w, "at %d columns a line overflows: %q", w, ln)
			}
			require.Contains(t, out, "NEARBY")
			require.Contains(t, out, "i invite · a approve")
			require.NotContains(t, out, "roger edge adopt")
			// The short fingerprints tell two same-named devices apart, so no width clips them.
			require.Contains(t, out, "8eda6e1a92c9")
			require.Contains(t, out, "abababababab")
			name := "nearby_empty"
			if enrolled {
				name = "nearby_enrolled"
			}
			require.NoError(t, goldenFile(t, fmt.Sprintf("%s_%d.golden", name, w), out))
		}
	}
}
