package tui

// THE j PROMPT on the empty [3] EDGE (features/edge/empty_edge_simple.feature §4): one line that
// takes an invite or an authority's address, handled by the host through Hooks.EdgeJoin with the
// same rules as `roger join`. An invite joins in one step; an address asks, shows the matching
// phrase, and waits for the owner there with a countdown. Nothing typed here ever reaches the
// status line or a log: the backend gets the text, the screen shows only its answer.

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/look"
)

// edgeJoinState is the prompt's whole state. Zero value = closed.
type edgeJoinState struct {
	open bool
	buf  string
	busy bool          // Start is running
	ask  *edge.JoinAsk // an ask by address, waiting for approval
	says string        // the last outcome, one line
	gen  int           // which dispatch a landing message belongs to (a cancelled one is dropped)
}

// edgeJoinStartedMsg lands Start: a member now (nil, nil), an ask to wait on, or why not.
type edgeJoinStartedMsg struct {
	gen int
	ask *edge.JoinAsk
	err error
}

// edgeJoinDoneMsg lands an ask's Wait.
type edgeJoinDoneMsg struct {
	gen int
	err error
}

// edgeJoinTickMsg redraws the countdown once a second while an ask waits.
type edgeJoinTickMsg struct{}

func edgeJoinTick() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return edgeJoinTickMsg{} })
}

// canJoinPrompt reports whether this build wired the prompt's backend.
func (m model) canJoinPrompt() bool { return m.hooks.EdgeJoin != nil && m.hooks.EdgeJoin.Start != nil }

func (m *model) openEdgeJoin() {
	if !m.canJoinPrompt() {
		m.status = stEmber.Render(look.SetupNoBuildLine)
		return
	}
	m.edge.join = edgeJoinState{open: true, gen: m.edge.join.gen}
}

func (m *model) closeEdgeJoin() {
	if a := m.edge.join.ask; a != nil && a.Cancel != nil {
		a.Cancel()
	}
	m.edge.join = edgeJoinState{gen: m.edge.join.gen + 1}
}

// onEdgeJoinKey owns every key while the prompt is open.
func (m *model) onEdgeJoinKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	j := &m.edge.join
	if j.ask != nil || j.busy {
		// Waiting: only esc does anything, and it cancels the ask and returns to the prompt.
		if k.Type == tea.KeyEsc || k.String() == "esc" {
			if j.ask != nil && j.ask.Cancel != nil {
				j.ask.Cancel()
			}
			*j = edgeJoinState{open: true, gen: j.gen + 1}
		}
		return m, nil
	}
	switch k.Type {
	case tea.KeyEsc:
		m.closeEdgeJoin()
	case tea.KeyEnter:
		return m, m.dispatchEdgeJoin()
	case tea.KeyBackspace, tea.KeyDelete:
		if n := len(j.buf); n > 0 {
			j.buf = j.buf[:n-1]
			j.says = ""
		}
	case tea.KeyRunes, tea.KeySpace:
		j.buf += string(k.Runes)
		j.says = ""
	}
	return m, nil
}

// dispatchEdgeJoin hands the line to the backend off the UI thread.
func (m *model) dispatchEdgeJoin() tea.Cmd {
	j := &m.edge.join
	text := strings.TrimSpace(j.buf)
	if text == "" {
		j.says = look.EmptyEdgePromptAsk
		return nil
	}
	j.busy, j.says = true, ""
	j.gen++
	gen, start := j.gen, m.hooks.EdgeJoin.Start
	return func() tea.Msg {
		ask, err := start(text)
		return edgeJoinStartedMsg{gen: gen, ask: ask, err: err}
	}
}

func (m model) onEdgeJoinStarted(msg edgeJoinStartedMsg) (tea.Model, tea.Cmd) {
	j := &m.edge.join
	if !j.open || msg.gen != j.gen {
		return m, nil // the owner left or cancelled meanwhile
	}
	j.busy = false
	switch {
	case msg.err != nil:
		j.says = msg.err.Error()
		return m, nil
	case msg.ask == nil:
		// A member now: the full view replaces the empty state.
		m.edge.join = edgeJoinState{gen: j.gen}
		m.refreshEdge()
		return m, nil
	}
	j.ask, j.buf = msg.ask, ""
	gen, wait := j.gen, msg.ask.Wait
	return m, tea.Batch(edgeJoinTick(), func() tea.Msg { return edgeJoinDoneMsg{gen: gen, err: wait()} })
}

func (m model) onEdgeJoinDone(msg edgeJoinDoneMsg) (tea.Model, tea.Cmd) {
	j := &m.edge.join
	if !j.open || msg.gen != j.gen || j.ask == nil {
		return m, nil
	}
	if msg.err != nil {
		*j = edgeJoinState{open: true, gen: j.gen, says: msg.err.Error()}
		return m, nil
	}
	m.edge.join = edgeJoinState{gen: j.gen}
	m.refreshEdge()
	return m, nil
}

// onEdgeJoinTick keeps the countdown moving only while an ask waits.
func (m model) onEdgeJoinTick() (tea.Model, tea.Cmd) {
	if !m.edge.join.open || m.edge.join.ask == nil {
		return m, nil
	}
	return m, edgeJoinTick()
}

// edgeJoinLines draws the prompt (or the wait) in the empty state's look. mono folds the glyphs.
func (m model) edgeJoinLines(w int, mono bool) []string {
	j := m.edge.join
	muted, ink, paper, live := lookStyle(look.Muted), lookStyle(look.Ink), lookStyle(look.Paper), lookStyle(look.Live)
	if mono {
		muted, ink, paper, live = lookStyle(look.Muted).UnsetForeground(), ink.UnsetForeground(), paper.UnsetForeground(), live.UnsetForeground()
	}
	rail := live.Render(look.Fold(look.Rail, mono))
	var out []string
	out = append(out, "  "+rail+" "+paper.Bold(true).Render(look.EmptyEdgeJoin))
	switch {
	case j.ask != nil:
		left := j.ask.Until.Sub(m.edgeNow())
		if left < 0 {
			left = 0
		}
		out = append(out,
			"    "+muted.Render("phrase")+"  "+paper.Bold(true).Render(j.ask.Phrase),
			"    "+ink.Render(look.EmptyEdgePhraseNote),
			"    "+muted.Render(fmt.Sprintf("%s · %d:%02d left", look.EmptyEdgeAskWait, int(left/time.Minute), int(left%time.Minute/time.Second))),
			"",
			lookActionBar(look.EmptyEdgeAskActions))
	case j.busy:
		out = append(out, "    "+muted.Render("joining…"))
	default:
		field := j.buf
		if field == "" {
			field = muted.Render(look.EmptyEdgePromptAsk)
		} else {
			field = ink.Render(truncVisible(field, max(10, w-8)))
		}
		out = append(out, "    "+paper.Render("›")+" "+field+paper.Render("▁"))
		if j.says != "" {
			out = append(out, "    "+live.Render(look.Fold("✕", mono))+" "+ink.Render(j.says))
		}
		out = append(out, "", lookActionBar(look.EmptyEdgePromptActions))
	}
	return out
}
