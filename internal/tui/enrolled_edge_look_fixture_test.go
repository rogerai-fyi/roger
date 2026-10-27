package tui

// The fixture for features/edge/enrolled_edge_look.feature: the Background's Edge (the local
// authority "shed", the member "garage" reached through a relay, two devices nearby, one invited),
// built through the real hooks and rendered by the TUI's own View().

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

// enrolledLook is one Edge to draw, and the knobs the scenarios turn.
type enrolledLook struct {
	now      time.Time
	self     edge.SelfStatus
	fleet    *edge.Fleet
	sess     *edge.Sessions
	cands    []store.EdgeNode
	house    []edge.Instance
	selfInst string
	reads    int // EdgeCandidates calls: how often the Edge was read
	ids      map[string]string
	alerts   map[string][]string // firing rules by node id; nil: no alerting wired
	adopting []store.EdgeNode    // adopted, not checked in yet; each holds a live code
}

const (
	lookGarageID = "n_gggggggggggggggggggggggggggggggggggggggggggggggg"
	lookPhoneID  = "n_8eda6e1a92c9000000000000000000000000000000000000"
	lookDeskID   = "n_ab12cd34ef56111111111111111111111111111111111111"
)

func newEnrolledLook(t *testing.T) *enrolledLook {
	t.Helper()
	f := &enrolledLook{now: time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC), ids: map[string]string{}}
	f.fleet = edge.NewFleet(store.NewMem(), "acct-1")
	f.fleet.SetClock(func() time.Time { return f.now })
	f.sess = edge.NewSessions("acct-1")
	f.sess.SetClock(func() time.Time { return f.now })
	f.self = edge.SelfStatus{Root: edge.RootLocal, Authority: "shed", AuthorityLocal: true, AuthorityHere: true,
		AuthorityAddr: "http://192.168.1.5:8791", Enrolled: true, Name: "shed", Discovery: edge.DiscoveryScanning,
		IntervalS: 30, LastPass: f.now.Unix(), Nearby: 3, NearbyInEdge: 1}
	f.enroll(t, store.EdgeNode{ID: lookGarageID, Name: "garage", Kind: "host", Presence: "VERIFIED", LastSeen: f.now.Unix() - 20})
	f.cands = []store.EdgeNode{
		{ID: lookPhoneID, Name: "gentle-ibex-14", Kind: "mobile", Presence: "CANDIDATE", LastSeen: f.now.Add(-12 * time.Second).Unix()},
		{ID: lookDeskID, Name: "deskside", Kind: "host", Pin: strings.Repeat("ab", 32), Presence: "CANDIDATE", LastSeen: f.now.Add(-20 * time.Minute).Unix()},
	}
	return f
}

func (f *enrolledLook) enroll(t *testing.T, n store.EdgeNode) {
	t.Helper()
	_, err := f.fleet.Enroll(n)
	require.NoError(t, err)
	f.ids[n.Name] = n.ID
}

// model builds the TUI on [3] EDGE at w x h (h 0: no window size received), the phone invited.
func (f *enrolledLook) model(t *testing.T, w, h int) model {
	t.Helper()
	hooks := Hooks{Station: "gm-93", EdgeFleet: f.fleet, EdgeSelf: f.self.Name, EdgeSessions: f.sess,
		EdgeCandidates: func() []store.EdgeNode { f.reads++; return f.cands },
		EdgeStatus:     func() edge.SelfStatus { return f.self },
		EdgeInviteFor: func(n store.EdgeNode) (string, time.Time, error) {
			return "roger://join?kind=edge&for=" + n.ID, f.now.Add(15 * time.Minute), nil
		},
		EdgeAdopt:      func(id, name string) error { return nil },
		EdgeName:       func(id, name string) error { return nil },
		EdgeRenameSelf: func(string) error { return nil },
	}
	if f.alerts != nil {
		hooks.EdgeAlerts = func() map[string][]string { return f.alerts }
	}
	if f.adopting != nil {
		hooks.EdgeAdopting = func() []store.EdgeNode { return f.adopting }
		hooks.EdgeAdoptCode = func(string) (string, time.Time, bool) { return "7KQ2M-9XH4P", f.now.Add(10 * time.Minute), true }
	}
	if f.house != nil {
		hooks.EdgeHousehold = func() []edge.Instance { return f.house }
		hooks.EdgeSelfInstance = func() string { return f.selfInst }
	}
	var tm tea.Model = NewWithHooks("http://b", "t", nil, hooks)
	if h > 0 {
		tm, _ = tm.Update(tea.WindowSizeMsg{Width: w, Height: h})
	}
	m := tm.(model)
	if h <= 0 {
		m.width = w
	}
	m.edgeClock = func() time.Time { return f.now }
	m.enterEdge()
	m.edge.nearby.invited = map[string]bool{lookPhoneID: true}
	return m
}

// lookFrame renders a model's whole frame with no colour (NO_COLOR).
func lookFrame(m model) string {
	lipgloss.SetColorProfile(termenv.Ascii)
	return m.View()
}

// edgeView is the [3] body with no measured height (every row), the way the tests read it.
func (m model) edgeView(w int) string { return m.edgeViewH(w, 0) }

func lookColorBody(m model, w int) string {
	lipgloss.SetColorProfile(termenv.TrueColor)
	lipgloss.SetHasDarkBackground(true)
	defer lipgloss.SetColorProfile(termenv.Ascii)
	return m.edgeView(w)
}
