package tui

// arrownav.go - the 2-second arrow-key section window.
//
// ←/→ step the preset bank one section at a time (cyclePreset). Some sections take
// the arrows for themselves: AGENT is a text prompt (←/→ move the cursor, → accepts
// the suggested next action) and the LOGIN panel used to treat them as "dismiss".
// Arrowing INTO one of those used to strand the operator: the next arrow typed or
// bounced back instead of moving on, and only esc got them out.
//
// The fix is a short window. After any ←/→ section jump, further ←/→ presses keep
// switching sections for navWindow, wherever they land. Any other key closes the
// window and is handled by the section as usual, so typing starts immediately.
// Once the window lapses, the arrows go back to meaning whatever the section uses
// them for. The preset bar shows the countdown while the window is open.

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
// pinned to the section it opened on (an async jump elsewhere closes it), and a
// running AGENT turn or a modal that takes over the AGENT keeps its keys.
func (m model) navLive() bool {
	if m.navUntil.IsZero() || m.mode != m.navMode || !navNow().Before(m.navUntil) {
		return false
	}
	if m.mode == modeAgent && (m.agentBusy || m.agentPendingConfirm != nil || m.operatorPlate != nil ||
		m.operatorPicker || m.agentPicker || m.operatorHandoff != nil) {
		return false
	}
	return true
}

// stepPreset jumps one section in dir (+1 next / -1 previous) and opens (or
// re-opens) the arrow window on the section it lands on.
func (m model) stepPreset(dir int) (tea.Model, tea.Cmd) {
	nm, cmd, _ := m.cyclePreset(dir)
	switch v := nm.(type) {
	case model:
		v = v.openNav()
		return v, tea.Batch(cmd, navTick(v.navSeq, time.Second))
	case *model:
		*v = v.openNav()
		return v, tea.Batch(cmd, navTick(v.navSeq, time.Second))
	}
	return nm, cmd
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

// navHint is the countdown shown in the preset bar while the window is open, e.g.
// "←/→ 2s". Empty when the window is closed.
func (m model) navHint() string {
	if !m.navLive() {
		return ""
	}
	secs := int(math.Ceil(m.navUntil.Sub(navNow()).Seconds()))
	return stDim.Render("←/→ ") + stKey.Render(fmt.Sprintf("%ds", secs))
}
