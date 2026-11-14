package tui

// arrownav.go: the 2-second arrow-key section window.
//
// ←/→ step the preset bank one section at a time (cyclePreset). Some sections take
// the arrows for themselves: AGENT is a text prompt (←/→ move the cursor, → accepts
// the suggested next action) and the LOGIN panel used to treat them as "dismiss".
// Arrowing INTO one of those used to strand the operator: the next arrow typed or
// bounced back instead of moving on, and only esc got them out.
//
// The fix is a short window. After a ←/→ section jump, further ←/→ presses keep
// switching sections for navWindow. Any other key closes the window and is handled
// by the section as usual, so typing starts immediately. Once the window lapses, the
// arrows go back to meaning whatever the section uses them for. The preset bar shows
// the countdown while the window is open.
//
// Two places keep their arrows even inside the window:
//   - [3] EDGE. Its map and action buttons are driven by ←/→ (the 2026-09-23 fix in
//     edge.go: a Right meant for a button must never land on [4]). An arrow walk
//     stops when it reaches EDGE, which also bounds a held arrow key to at most one
//     pass along the bank.
//   - AGENT with something in hand: a kept draft in the composer (← moves its cursor)
//     or a suggested next action showing (→ accepts it).

import (
	"fmt"
	"math"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// navWindow is how long ←/→ keep switching sections after a ←/→ jump.
const navWindow = 2 * time.Second

// navNow is the window's clock (a var so tests can move time forward).
var navNow = time.Now

// navTickMsg repaints the countdown and closes the window when it lapses. seq ties
// it to one opening of the window; a tick from an earlier opening is ignored.
type navTickMsg struct{ seq int }

func navTick(seq int, d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return navTickMsg{seq: seq} })
}

// navLive reports whether the arrow window is open for the current section. It is
// pinned to the section it opened on (an async jump elsewhere closes it). EDGE never
// holds it, and AGENT gives the arrows back while a turn runs, a modal is up, or the
// composer has a draft or a suggested next action.
func (m model) navLive() bool {
	if m.navUntil.IsZero() || m.mode != m.navMode || !navNow().Before(m.navUntil) {
		return false
	}
	switch m.mode {
	case modeEdge:
		return false
	case modeAgent:
		if m.agentBusy || m.agentPendingConfirm != nil || m.operatorPlate != nil ||
			m.operatorPicker || m.agentPicker || m.operatorHandoff != nil ||
			m.agentIn.Value() != "" || m.agentNextHint != "" {
			return false
		}
	}
	return true
}

// stepPreset jumps one section in dir (+1 next / -1 previous) and opens (or
// re-opens) the arrow window on the section it lands on. Landing on EDGE closes the
// window instead, because EDGE's arrows drive its own map.
func (m model) stepPreset(dir int) (tea.Model, tea.Cmd) {
	// navArrive tells the destination it is being passed through on an arrow walk, so
	// AGENT holds its first-entry auto-tune until the operator settles there.
	m.navArrive = true
	nm, cmd, _ := m.cyclePreset(dir)
	v, ok := nm.(model)
	if !ok {
		return nm, cmd
	}
	v.navArrive = false
	if v.mode == modeEdge {
		v.navUntil = time.Time{}
		return v, cmd
	}
	v = v.openNav()
	return v, tea.Batch(cmd, navTick(v.navSeq, time.Second))
}

func (m model) openNav() model {
	m.navSeq++
	m.navUntil = navNow().Add(navWindow)
	m.navMode = m.mode
	return m
}

// onNavTick keeps the countdown ticking once a second and closes the window when
// it runs out.
func (m model) onNavTick(msg navTickMsg) (tea.Model, tea.Cmd) {
	if msg.seq != m.navSeq || m.navUntil.IsZero() {
		return m, nil
	}
	rem := m.navUntil.Sub(navNow())
	if rem <= 0 {
		m.navUntil = time.Time{}
		return m, nil
	}
	next := time.Second
	if rem < next {
		next = rem
	}
	return m, navTick(m.navSeq, next)
}

// navKey routes a key while the arrow window is open: ←/→ switch sections again
// (handled=true). Any other key closes the window and is left for the section.
func (m model) navKey(k tea.KeyMsg) (model, tea.Model, tea.Cmd, bool) {
	if m.navLive() {
		switch k.String() {
		case "left":
			nm, cmd := m.stepPreset(-1)
			return m, nm, cmd, true
		case "right":
			nm, cmd := m.stepPreset(+1)
			return m, nm, cmd, true
		}
	}
	m.navUntil = time.Time{}
	return m, nil, nil, false
}

// navHint is the countdown shown while the window is open, e.g. "←/→ 2s". Empty when
// the window is closed.
func (m model) navHint() string {
	if !m.navLive() {
		return ""
	}
	secs := int(math.Ceil(m.navUntil.Sub(navNow()).Seconds()))
	return stDim.Render("←/→ ") + stKey.Render(fmt.Sprintf("%ds", secs))
}

// settleAgentTune runs AGENT's first-entry auto-tune once the operator has settled
// on AGENT: the arrow window closed there (a key, or the 2 seconds ran out) or they
// came back to it. An arrow walk that only passes through never tunes a band, and
// never uses up the session's one auto-tune either. Called after every update.
func (m model) settleAgentTune() (model, tea.Cmd) {
	if !m.agentTuneOwed || m.mode != modeAgent || m.navLive() {
		return m, nil
	}
	m.agentTuneOwed = false
	if m.agent == nil || m.autoTuning || m.proxyHolder != nil || m.agent.model != "" {
		// Something was tuned in the meantime: there is nothing left to find.
		return m, nil
	}
	return m.armAutoTune()
}

// armAutoTune starts the silent first-entry auto-tune (R1/R6): the "finding a band"
// beat goes in the transcript, and the decision runs now (a scan is in hand) or when
// /discover lands.
func (m model) armAutoTune() (model, tea.Cmd) {
	landing := m.agentLandingLines == len(m.agentLines)
	m.autoTuneBeatLen = len(m.agentLines) // the beat below is swapped for the outcome
	m.agentLines = append(m.agentLines, agentFindingBandBeat())
	if landing {
		m.agentLandingLines = len(m.agentLines)
	}
	m.autoTuning = true
	return m, autoTuneCmd(m.broker, m.scanned)
}
