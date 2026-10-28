package tui

// A device that starts pinging while the ENROLLED [3] view is open shows as a NEARBY row on its own,
// the way it does on the empty state. Found live on 6.10.0-test: after e (start an Edge here) the
// enrolled view stayed on "NO DEVICES YET" with a phone pinging, DISCOVERY counting it, until r.

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/edge"
	"rogerai.fm/roger/v7/internal/store"
)

func TestEnrolledViewPicksUpANewNearbyDevice(t *testing.T) {
	t.Cleanup(func() { lipgloss.SetColorProfile(termenv.Ascii) })
	lipgloss.SetColorProfile(termenv.Ascii)
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	fl := edge.NewFleet(store.NewMem(), "acct-1")
	fl.SetClock(func() time.Time { return now })
	self := edge.SelfStatus{Root: edge.RootLocal, Authority: "shed", AuthorityLocal: true, AuthorityHere: true,
		AuthorityAddr: "http://192.168.1.5:8791", Enrolled: true, Name: "shed", Discovery: edge.DiscoveryScanning,
		IntervalS: 30, LastPass: now.Unix()}
	var cands []store.EdgeNode
	h := Hooks{Station: "gm-93", EdgeFleet: fl, EdgeSelf: self.Name,
		EdgeCandidates: func() []store.EdgeNode { return cands },
		EdgeStatus:     func() edge.SelfStatus { return self },
	}
	var tm tea.Model = NewWithHooks("http://b", "t", nil, h)
	tm, _ = tm.Update(tea.WindowSizeMsg{Width: 100, Height: 44})
	tm, _ = tm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")})
	m := tm.(model)
	m.edgeClock = func() time.Time { return now }
	require.Equal(t, modeEdge, m.mode)
	require.False(t, m.edgeEmptySimple(), "an enrolled machine never sees the empty state")
	require.Contains(t, stripANSI(m.edgeView(100)), "NO DEVICES YET")
	require.True(t, m.edge.statusTicking, "the enrolled view re-reads its snapshot while it shows, as the empty state does")

	// A phone starts pinging; the host's next pass finds it.
	cands = []store.EdgeNode{{ID: "n_8eda6e1a92c9" + strings.Repeat("0", 36), Name: "gentle-ibex-14", Kind: "mobile",
		Presence: "CANDIDATE", LastSeen: now.Unix()}}
	self.LastPass = now.Add(30 * time.Second).Unix()

	tm, cmd := m.Update(edgeStatusTickMsg{})
	m = tm.(model)
	out := stripANSI(m.edgeView(100))
	require.Contains(t, out, "NEARBY")
	require.Contains(t, out, "gentle-ibex-14")
	require.NotContains(t, out, "NO DEVICES YET")
	require.NotNil(t, cmd, "the re-read keeps going while [3] is open")

	// Leaving [3] stops it.
	m.leaveEdge()
	tm, cmd = m.Update(edgeStatusTickMsg{})
	require.Nil(t, cmd)
	require.False(t, tm.(model).edge.statusTicking)
}

// The highlight is on a device, not on a slot. A re-read can drop the highlighted device (i on a
// phone grants its claim, so the host stops listing it as a candidate) or add one above it; either
// way a and i must never land on a device the owner did not pick. Found live on 6.10.0-test: i on
// the second of two phones, esc, a, and the FIRST phone's claim was granted.
func TestTheNearbyHighlightStaysOnTheDeviceItWasOn(t *testing.T) {
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	dev := func(id, name string) store.EdgeNode {
		return store.EdgeNode{ID: "n_" + id + strings.Repeat("0", 36), Name: name, Kind: "mobile", Presence: "CANDIDATE", LastSeen: now.Unix()}
	}
	a, b, c := dev("aaaaaaaaaaaa", "quiet-heron-22"), dev("bbbbbbbbbbbb", "calm-otter-7"), dev("cccccccccccc", "brave-lynx-3")
	open := func(t *testing.T, cands *[]store.EdgeNode, self *edge.SelfStatus) model {
		fl := edge.NewFleet(store.NewMem(), "acct-1")
		fl.SetClock(func() time.Time { return now })
		*self = edge.SelfStatus{Root: edge.RootLocal, Authority: "shed", AuthorityLocal: true, AuthorityHere: true,
			AuthorityAddr: "http://192.168.1.5:8791", Enrolled: true, Name: "shed", Discovery: edge.DiscoveryScanning,
			IntervalS: 30, LastPass: now.Unix()}
		h := Hooks{Station: "gm-93", EdgeFleet: fl, EdgeSelf: self.Name,
			EdgeCandidates: func() []store.EdgeNode { return *cands },
			EdgeStatus:     func() edge.SelfStatus { return *self },
		}
		var tm tea.Model = NewWithHooks("http://b", "t", nil, h)
		tm, _ = tm.Update(tea.WindowSizeMsg{Width: 100, Height: 44})
		tm, _ = tm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")})
		m := tm.(model)
		m.edgeClock = func() time.Time { return now }
		require.True(t, m.edgeMapActive())
		return m
	}
	pass := func(t *testing.T, m model, self *edge.SelfStatus) model {
		self.LastPass += 30
		tm, _ := m.Update(edgeStatusTickMsg{})
		return tm.(model)
	}
	pick := func(t *testing.T, m model, id string) model {
		for range 12 {
			if m.edgeNearbySelectedID() == id {
				return m
			}
			tm, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
			m = tm.(model)
		}
		require.Equal(t, id, m.edgeNearbySelectedID(), "could not move the highlight onto the device")
		return m
	}

	t.Run("the picked device leaves: nothing nearby is highlighted", func(t *testing.T) {
		cands, self := []store.EdgeNode{a, b}, edge.SelfStatus{}
		m := pick(t, open(t, &cands, &self), b.ID)
		cands = []store.EdgeNode{a}
		m = pass(t, m, &self)
		require.Empty(t, m.edgeNearbySelectedID(), "the highlight slid onto %s", m.edgeNearbySelectedID())
	})
	t.Run("a device appears above the picked one: the highlight stays", func(t *testing.T) {
		cands, self := []store.EdgeNode{a, b}, edge.SelfStatus{}
		m := pick(t, open(t, &cands, &self), b.ID)
		cands = []store.EdgeNode{c, a, b}
		m = pass(t, m, &self)
		require.Equal(t, b.ID, m.edgeNearbySelectedID())
	})
	t.Run("the owner never moved: a device appearing first does not take the highlight", func(t *testing.T) {
		cands, self := []store.EdgeNode{a}, edge.SelfStatus{}
		m := open(t, &cands, &self)
		require.Equal(t, a.ID, m.edgeNearbySelectedID(), "a device pinging is highlighted on arrival")
		cands = []store.EdgeNode{c, a}
		m = pass(t, m, &self)
		require.Equal(t, a.ID, m.edgeNearbySelectedID())
	})
	t.Run("the first device to ping still takes the highlight", func(t *testing.T) {
		var cands []store.EdgeNode
		self := edge.SelfStatus{}
		m := open(t, &cands, &self)
		cands = []store.EdgeNode{a}
		m = pass(t, m, &self)
		require.Equal(t, a.ID, m.edgeNearbySelectedID())
	})
}
