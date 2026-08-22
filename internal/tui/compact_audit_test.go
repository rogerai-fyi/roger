package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// THE MINIMIZE-MODE AUDIT (founder 2026-08-21: "lets make sure we audit minimize mode to
// make sure we are correctly representing all the ui/ux needs").
//
// COMPACT is the windowshade: the dense calm view an operator drops into when they want the
// radio out of the way. It is where a screen is MOST likely to be wrong, for two reasons -
// it is the least-looked-at mode, and it is usually paired with a SHORT terminal, which is
// the condition that turns a too-tall frame into a scrolled alt-buffer (the stacked-logo bug).
//
// So this audit is mechanical rather than aesthetic. It walks every mode at several
// geometries and asserts the three things that are true of a correct screen regardless of
// what it is for: it fits the width, it fits the height, and it still tells the operator
// how to leave.

// auditModes is every mode a compact operator can actually land on, with the state each
// needs to render something real. A mode rendering its empty state is still a valid audit
// subject - an empty screen that overflows is just as broken as a full one.
func auditModes(t *testing.T, w, h int) map[mode]model {
	t.Helper()
	base := func() model {
		m := privateTab(t)
		m.compact = true
		m.width, m.height = w, h
		m.tuneTab = tabOpenMarket
		m.bands = []band{
			{model: "grok-4.6", online: true, stations: 2, cheapest: &offer{Model: "grok-4.6", Ctx: 32768, PriceOut: 1.5}},
			{model: "foundation", online: true, stations: 1, cheapest: &offer{Model: "foundation", Ctx: 8192}},
		}
		m.scanned = true
		m.limits = &LimitStore{Models: map[string]Limit{"grok-4.6": {MaxOut: 1.25}}}
		return m
	}
	out := map[mode]model{}
	add := func(md mode, f func(m *model)) {
		m := base()
		m.mode = md
		if f != nil {
			f(&m)
		}
		out[md] = m
	}
	add(modeBrowse, nil)
	add(modeBrowse, func(m *model) { m.tuneTab = tabPrivate })
	add(modeChat, func(m *model) {
		m.connected = &offer{NodeID: "eager-puma-54-grok-4-6", Model: "grok-4.6", Online: true}
		m.transcript = []string{"hi", "hello"}
	})
	add(modeHelp, nil)
	add(modeLimits, func(m *model) { mm := m; mm.enterLimits(); mm.compact = true })
	add(modeShare, nil)
	add(modePrivate, nil)
	add(modeBandManage, func(m *model) {
		m.bandManageID, m.bandManageDisp, m.bandManageNode = "band_here", "145.225 MHz", "eager-puma-54-grok-4-6"
	})
	add(modeBandRevokeConfirm, func(m *model) { m.bandManageID, m.bandManageDisp = "band_here", "145.225 MHz" })
	add(modeBandRotateConfirm, func(m *model) { m.bandManageID, m.bandManageDisp = "band_here", "145.225 MHz" })
	add(modeBandMove, func(m *model) { m.bandManageID, m.bandManageDisp = "band_here", "145.225 MHz" })
	add(modeBandConfig, func(m *model) { m.cfgModel = "grok-4.6" })
	add(modeBandLabel, func(m *model) { m.cfgModel, m.bandManageDisp = "grok-4.6", "145.225 MHz" })
	add(modeBandDetail, func(m *model) { m.detailBand = m.bands[0] })
	add(modeFreqEntry, nil)
	add(modeLogin, nil)
	add(modeQuitConfirm, nil)
	add(modeShareSetup, nil)
	return out
}

func modeName(md mode) string {
	for n, v := range map[string]mode{
		"browse": modeBrowse, "chat": modeChat, "help": modeHelp, "limits": modeLimits,
		"share": modeShare, "private": modePrivate, "bandManage": modeBandManage,
		"bandRevoke": modeBandRevokeConfirm, "bandRotate": modeBandRotateConfirm,
		"bandMove": modeBandMove, "bandConfig": modeBandConfig, "bandLabel": modeBandLabel,
		"bandDetail": modeBandDetail, "freqEntry": modeFreqEntry, "login": modeLogin,
		"quitConfirm": modeQuitConfirm, "shareSetup": modeShareSetup,
	} {
		if v == md {
			return n
		}
	}
	return "mode?"
}

// AUDIT 1: NOTHING OVERFLOWS THE WIDTH. A line wider than the terminal wraps, and a wrapped
// line in a dense view is what makes compact look broken rather than tight.
func TestCompactNeverOverflowsWidth(t *testing.T) {
	for _, w := range []int{40, 60, 80, 100} {
		for md, m := range auditModes(t, w, 24) {
			frame := m.View()
			for i, ln := range strings.Split(frame, "\n") {
				if got := lipgloss.Width(ln); got > w {
					t.Errorf("compact %s at w=%d: line %d is %d wide: %q",
						modeName(md), w, i, got, stripANSI(ln))
				}
			}
		}
	}
}

// AUDIT 2: NOTHING OVERFLOWS THE HEIGHT. A frame taller than the terminal scrolls the ALT
// BUFFER, stranding the previous frame's top above it - which is what produced the stacked
// ROGER logos the founder reported. Compact is exactly where this bites, because an
// operator minimizes to fit a small window.
func TestCompactNeverOverflowsHeight(t *testing.T) {
	for _, h := range []int{20, 24, 30} {
		for md, m := range auditModes(t, 100, h) {
			frame := m.View()
			if rows := len(strings.Split(strings.TrimRight(frame, "\n"), "\n")); rows > h {
				t.Errorf("compact %s at h=%d: the frame is %d rows - it will scroll the alt buffer",
					modeName(md), h, rows)
			}
		}
	}
}

// AUDIT 3: EVERY SCREEN SAYS HOW TO LEAVE. Compact drops decoration by design, but the way
// out is not decoration - a dense screen with no exit is a trap, and the operator who
// minimized is the one least likely to remember the key.
func TestCompactAlwaysNamesTheWayOut(t *testing.T) {
	for md, m := range auditModes(t, 100, 24) {
		if md == modeBrowse {
			continue // the dial IS the top level: there is nothing to leave to
		}
		frame := stripANSI(m.View())
		low := strings.ToLower(frame)
		if !strings.Contains(low, "esc") && !strings.Contains(low, "q ") && !strings.Contains(low, "back") {
			t.Errorf("compact %s names no way out:\n%s", modeName(md), frame)
		}
	}
}

// AUDIT 4: THE HEIGHT BOUND HOLDS AT SCALE. Audit 2 walks a fixture with a handful of
// rows; the failure mode that actually bit the founder was a LONG list on a short
// terminal. BASE STATION printed every session and every band unconditionally, so this
// sweeps both lists past any plausible count against any plausible window.
func TestBaseStationFitsAnyTerminal(t *testing.T) {
	for _, h := range []int{20, 24, 30, 40, 60} {
		for _, n := range []int{0, 1, 5, 40, 200} {
			m := privateTab(t)
			m.compact = true
			m.mode = modePrivate
			m.width, m.height = 100, h
			m.rcSessions = nil
			m.rcBands = nil
			for i := 0; i < n; i++ {
				m.rcSessions = append(m.rcSessions, RemoteSessionRow{ID: "s", Name: "desk", Online: true})
				m.rcBands = append(m.rcBands, BandRow{ID: "b", Display: "145.225 MHz", Status: "active", NodeID: "n"})
			}
			rows := len(strings.Split(strings.TrimRight(m.View(), "\n"), "\n"))
			if rows > h {
				t.Errorf("BASE STATION h=%d with %d sessions + %d bands: %d rows - it will scroll the alt buffer",
					h, n, n, rows)
			}
		}
	}
}

// A truncated list must SAY it was truncated. A list that simply stops reads as a
// complete one, and an operator who cannot see their band assumes they do not have it.
func TestBaseStationAnnouncesWhatItHid(t *testing.T) {
	m := privateTab(t)
	m.compact = true
	m.mode = modePrivate
	m.width, m.height = 100, 22
	for i := 0; i < 40; i++ {
		m.rcBands = append(m.rcBands, BandRow{ID: "b", Display: "145.225 MHz", Status: "active", NodeID: "n"})
	}
	out := stripANSI(m.View())
	if !strings.Contains(out, "more") {
		t.Errorf("a truncated BASE STATION must say how many it hid:\n%s", out)
	}
}

// The cursor must stay VISIBLE inside the window. A bounded list that always renders from
// the top hides the row the operator is on the moment they move past the fold - they press
// down, nothing appears to change, and the screen looks frozen.
func TestBaseStationKeepsTheCursorInView(t *testing.T) {
	m := privateTab(t)
	m.compact = true
	m.mode = modePrivate
	m.width, m.height = 100, 24
	m.rcBands = nil
	for i := 0; i < 30; i++ {
		m.rcBands = append(m.rcBands, BandRow{
			ID: "band_" + string(rune('a'+i%26)), Display: "145.225 MHz",
			Status: "active", NodeID: "node-" + string(rune('a'+i%26)),
		})
	}
	m.rcCursor = 25 // well past any window that starts at the top
	if got := stripANSI(m.View()); !strings.Contains(got, "▸") {
		t.Errorf("the cursor row is outside the rendered window:\n%s", got)
	}
}
