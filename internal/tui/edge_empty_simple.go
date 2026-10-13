package tui

// THE SIMPLE EMPTY EDGE (features/edge/empty_edge_simple.feature): what [3] EDGE shows on a
// machine that is in no Edge yet. Inside the TUI's own chrome, in the Health/Fleet look from the
// shared internal/look package: a muted ROGER · EDGE eyebrow, YOUR EDGE · not set up yet, one
// sentence, the two ways in (e, j), the scan line with its magic eye, and the action bar. No
// lamp, no ON AIR banner, no command, no Core, no login: those come once it is set up.

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/look"
)

// edgeStatusTickEvery is how often the empty state re-reads this machine's status while it is
// showing, so a scan pass that completes with nothing to animate still reaches the scan line.
const edgeStatusTickEvery = 2 * time.Second

type edgeStatusTickMsg struct{}

func edgeStatusTick() tea.Cmd {
	return tea.Tick(edgeStatusTickEvery, func(time.Time) tea.Msg { return edgeStatusTickMsg{} })
}

// onEdgeStatusTick re-reads the snapshot and re-arms only while the empty state is on screen.
func (m model) onEdgeStatusTick() (tea.Model, tea.Cmd) {
	if m.mode != modeEdge || !m.edgeEmptySimple() {
		m.edge.statusTicking = false
		return m, nil
	}
	m.refreshEdge()
	return m, edgeStatusTick()
}

// armEdgeStatusTick starts the re-read loop when the empty state comes on screen; idempotent.
func (m *model) armEdgeStatusTick() tea.Cmd {
	if m.mode != modeEdge || m.edge.statusTicking || !m.edgeEmptySimple() {
		return nil
	}
	m.edge.statusTicking = true
	return edgeStatusTick()
}

// edgeEmptySimple reports whether the frame shows the simple empty state: the fleet could be
// read and holds nothing, and this machine is neither enrolled nor rooting an Edge. An unread
// fleet or status, or a host that did not start, is never drawn as "not set up yet". A device
// pinging nearby does not change this machine's state: it is drawn as a NEARBY row on this
// screen (§7), never by falling back to the old facts view.
func (m model) edgeEmptySimple() bool {
	st := m.edge
	if m.hooks.EdgeFleet == nil || st.err != "" || len(st.rows) > 0 {
		return false
	}
	if st.status == nil {
		return true
	}
	return st.status.Err == "" && !st.status.SetUp()
}

// edgeEmptySimpleView draws the state. Every line goes through line(), which clips to w.
func (m model) edgeEmptySimpleView(w int, line func(string)) {
	mono := lookMono()
	muted, ink, paper, live := lookStyle(look.Muted), lookStyle(look.Ink), lookStyle(look.Paper), lookStyle(look.Live)
	if mono {
		muted, ink, paper, live = muted.UnsetForeground(), ink.UnsetForeground(), paper.UnsetForeground(), live.UnsetForeground()
	}
	textW := max(30, w-4)

	line("  " + muted.Render(look.Fold(look.EmptyEdgeBrand, mono)))
	line("  " + live.Render(look.Fold(look.Rail, mono)) + " " + paper.Bold(true).Render(look.Eyebrow(look.EmptyEdgeTitle)) +
		muted.Render(" · ") + ink.Render(look.EmptyEdgeState))
	for _, ln := range wordWrap(look.EmptyEdgeSay, textW) {
		line("  " + ink.Render(ln))
	}
	line("")

	if m.edge.join.open {
		for _, ln := range m.edgeJoinLines(w, mono) {
			line(ln)
		}
		return
	}
	if m.edge.nearby.invite != nil {
		for _, ln := range m.edgeNearbyInviteLines(w, mono) {
			line(ln)
		}
		return
	}

	// The two ways in: key cap, label, and what it means (beside it when there is room,
	// under it when there is not).
	ways := []struct{ key, label, what string }{
		{"e", look.EmptyEdgeStart, look.EmptyEdgeStartIs},
		{"j", look.EmptyEdgeJoin, look.EmptyEdgeJoinIs},
	}
	labelW := 0
	for _, wy := range ways {
		labelW = max(labelW, len([]rune(wy.label)))
	}
	beside := w >= 80
	for _, wy := range ways {
		head := "  " + lookKeyCap(wy.key, mono) + "  " + ink.Render(pad(wy.label, labelW))
		if beside {
			line(head + "  " + muted.Render(wy.what))
			continue
		}
		line(head)
		line("       " + muted.Render(wy.what))
	}
	line("")

	// The scan line and its magic eye, from the frame's own status snapshot.
	var scan look.EdgeScan
	if st := m.edge.status; st != nil {
		scan = st.Scan()
	}
	first, second := scan.Lines()
	eye := live.Render(look.Fold(scan.Eye(), mono))
	if scan.Off {
		eye = muted.Render(look.Fold(scan.Eye(), mono))
	}
	for i, ln := range wordWrap(first, max(24, w-5)) {
		if i == 0 {
			line("  " + eye + " " + ink.Render(ln))
			continue
		}
		line("     " + ink.Render(ln))
	}
	line("     " + muted.Render(second))
	line("")
	// The devices pinging nearby, each a row the owner acts on (§7).
	if rows := m.edgeNearbyLines(w, mono); len(rows) > 0 {
		for _, ln := range rows {
			line(ln)
		}
		line("")
	}
	line(lookActionBar(look.EmptyEdgeActions))
}

// edgeAddNextLine is the one line a fresh authority with no other node gets under its map: how
// the next device joins (features/edge/empty_edge_simple.feature §3, "after starting").
func edgeAddNextLine(st *edge.SelfStatus) string {
	if st == nil || !st.AuthorityHere {
		return ""
	}
	return "add the next device: " + strings.Join(st.AddNodeLines(), "; ")
}
