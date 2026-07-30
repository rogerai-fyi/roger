package tui

// smartselect.go - the application-owned selection behind SMART MOUSE MODE
// (features/tui/conversation_hierarchy_and_selection.feature, "Smart mouse mode
// copies an application-owned selection on release").
//
// Smart selection is the DEFAULT (mouse capture on): the wheel scrolls and a
// left-drag over the CHANNEL/AGENT transcript becomes an application-owned
// selection. ctrl+o / /mouse restores native terminal selection. The covered cells
// highlight during the drag, and on release exactly the visible text is copied.
// "Exactly the visible text" means: decorative gutters (▏ ◂ ▌), role labels
// (YOU › / ROGER ›), the 2-space indent, and ANSI styling are excluded; a
// soft-wrap break rejoins without a newline (restoring the source whitespace the
// wrapper consumed); each transcript entry boundary is an explicit newline; wide
// characters, emoji, and combining marks are never split (a selection touching
// any cell of a grapheme takes the whole grapheme).
//
// Copy feedback is HONEST: the success toast appears only when a clipboard
// mechanism succeeded; when neither the local tool nor an OSC 52 path is
// available the status says so and the selection stays highlighted (recoverable)
// instead of silently pretending.

import (
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
)

// stSelHi paints the selected cells reverse-video: shape survives NO_COLOR (the
// text is untouched), color profiles get the standard selection look.
var stSelHi = lipgloss.NewStyle().Reverse(true)

// smartSelState is the drag life cycle: active while the button is down, held
// when a finished selection is kept visible after a failed copy. Coordinates are
// SCREEN cells (the anchor is the press, the head follows the pointer).
type smartSelState struct {
	active bool
	held   bool
	ax, ay int
	hx, hy int
}

// smartCopyResultMsg reports what the clipboard mechanisms actually did.
type smartCopyResultMsg struct {
	ok    bool
	chars int
}

// smartClipTool / smartOSC52 are the two copy mechanisms, package vars so tests
// can observe writes and force failure deterministically.
var smartClipTool = copyToClipboard

var smartOSC52 = func(s string) bool {
	fi, err := os.Stdout.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return false // no terminal, no OSC 52 path
	}
	fmt.Print(osc52(s))
	return true
}

// smartCopyCmd copies text off the render path and reports honestly. The local
// tool runs first (its success is observable); OSC 52 counts as a path only when
// stdout is a terminal that can consume the escape.
func smartCopyCmd(text string) tea.Cmd {
	return func() tea.Msg {
		ok := smartClipTool(text)
		if smartOSC52(text) {
			ok = true
		}
		return smartCopyResultMsg{ok: ok, chars: utf8.RuneCountInString(text)}
	}
}

// mouseStatusLine is the ONE status string for the ctrl+o / /mouse ownership
// toggle (four call sites share it): who owns the mouse and how to switch back.
func mouseStatusLine(off bool) string {
	if off {
		return stLive.Render("native select ON · drag to copy · ctrl+o for smart select + wheel scroll")
	}
	return stDim.Render("smart select ON · drag copies · wheel scrolls · ctrl+o for native select")
}

// selRow is one physical transcript row as the selection sees it: the selectable
// text (chrome excluded), the screen column where that text starts, whether the
// row ends its entry (hard = a real newline follows), and the source whitespace
// the wrapper consumed before a continuation row (restored on soft joins).
type selRow struct {
	text     string
	startCol int
	hard     bool
	gap      string
}

// selectionRows mirrors transcriptContent's geometry exactly (same ansi.Wrap,
// same 2-space indent) and maps every physical row to its selectable text.
func selectionRows(entries []string, width int) []selRow {
	wrapAt := width - 2
	rows := make([]selRow, 0, len(entries))
	for _, e := range entries {
		wrapped := e
		if wrapAt > 0 {
			wrapped = ansi.Wrap(e, wrapAt, "")
		}
		parts := strings.Split(wrapped, "\n")
		source := ansi.Strip(e)
		pos := 0
		for i, ln := range parts {
			plain := ansi.Strip(ln)
			gap := ""
			if idx := strings.Index(source[pos:], plain); plain != "" && idx >= 0 {
				if i > 0 {
					gap = source[pos : pos+idx]
				}
				pos += idx + len(plain)
			}
			text, chrome := plain, 0
			if i == 0 {
				text, chrome = stripRowChrome(plain)
			}
			rows = append(rows, selRow{text: text, startCol: 2 + chrome, hard: i == len(parts)-1, gap: gap})
		}
	}
	return rows
}

// stripRowChrome removes the decorative prefixes a transcript row may carry - the
// user band bar (▌ ) with its YOU › label, the answer gutters (▏ / ◂ ) - and
// reports how many screen columns they occupied. A row that is ONLY a role label
// (the ◂ ROGER › head) selects as nothing.
func stripRowChrome(plain string) (string, int) {
	s, cols := plain, 0
	for _, p := range []string{"▌ ", "YOU › ", "▏ ", "◂ "} {
		if strings.HasPrefix(s, p) {
			s = strings.TrimPrefix(s, p)
			cols += ansi.StringWidth(p)
		}
	}
	if strings.HasPrefix(s, "ROGER ›") {
		return "", 0
	}
	if cols == 0 {
		return plain, 0
	}
	return s, cols
}

// cutCells returns the graphemes of text covering any cell in [from, to]
// (text-relative columns, inclusive). A wide character or emoji touched on
// either cell is taken whole; combining marks travel with their base cluster.
func cutCells(text string, from, to int) string {
	if text == "" || to < from || to < 0 {
		return ""
	}
	var b strings.Builder
	col := 0
	g := uniseg.NewGraphemes(text)
	for g.Next() {
		cl := g.Str()
		w := ansi.StringWidth(cl)
		if w == 0 {
			// A stray zero-width cluster rides with whatever came before it.
			if b.Len() > 0 {
				b.WriteString(cl)
			}
			continue
		}
		if col > to {
			break
		}
		if col+w-1 >= from {
			b.WriteString(cl)
		}
		col += w
	}
	return b.String()
}

// selectionText extracts the copied text for a selection from (sr,sc) to
// (er,ec) - content row + screen column, either order. Terminal-linear
// semantics: the first row from its start column, middle rows whole, the last
// row to its end column. Soft-wrapped rows rejoin through their source gap,
// entry boundaries become newlines, and blank/label edge rows contribute
// nothing, so a drag that begins or ends in padding never fabricates content.
func selectionText(rows []selRow, sr, sc, er, ec int) string {
	if len(rows) == 0 {
		return ""
	}
	if er < sr || (er == sr && ec < sc) {
		sr, sc, er, ec = er, ec, sr, sc
	}
	if er < 0 || sr >= len(rows) {
		return ""
	}
	if sr < 0 {
		sr, sc = 0, 0
	}
	if er >= len(rows) {
		er, ec = len(rows)-1, 1<<30
	}
	var b strings.Builder
	sep := ""
	for i := sr; i <= er; i++ {
		row := rows[i]
		from, to := 0, 1<<30
		if i == sr {
			from = sc - row.startCol
		}
		if i == er {
			to = ec - row.startCol
		}
		if from < 0 {
			from = 0
		}
		if piece := cutCells(row.text, from, to); piece != "" {
			if b.Len() > 0 {
				b.WriteString(sep)
			}
			b.WriteString(piece)
			sep = ""
		}
		// Accumulate the separator this row leaves behind: a newline after an
		// entry end, the restored source gap after a soft wrap.
		if row.hard {
			sep += "\n"
		} else if i+1 < len(rows) {
			sep += rows[i+1].gap
		}
	}
	out := b.String()
	if strings.TrimSpace(out) == "" {
		return ""
	}
	return out
}

// transcriptTop is the SCREEN row where the active transcript viewport's first
// row paints, mirroring View()'s composition (header block, then the per-mode
// rows above the viewport). -1 when the mode has no selectable transcript.
func (m model) transcriptTop() int {
	w := m.effWidth()
	var top int
	if m.compact {
		top = lineRows(m.compactHeader(w))
	} else {
		top = lineRows(m.presetBar(w)) + 1 + lineRows(m.header(w))
	}
	switch m.mode {
	case modeChat:
		return top + 1 // the TUNE-IN heading row
	case modeAgent:
		if m.operatorHandoff != nil {
			return -1
		}
		top++ // the dial-deck heading row
		if !m.compact {
			top += lineRows(strings.TrimSuffix(m.deskStripLine(w), "\n"))
		}
		mdl := ""
		if m.agent != nil {
			mdl = m.agent.model
		}
		if mdl != "" {
			top += len(agentCornerPing(m.agentTurnState, anim(m.frame), m.narrow(), m.agentMascotCompact(), m.agentBusy))
		}
		return top
	}
	return -1
}

// transcriptRegion is the active transcript viewport's screen extent.
func (m model) transcriptRegion() (top, height int) {
	top = m.transcriptTop()
	if top < 0 {
		return -1, 0
	}
	switch m.mode {
	case modeChat:
		return top, m.chatVP.Height
	case modeAgent:
		return top, m.agentVP.Height
	}
	return -1, 0
}

// onSmartMouse owns mouse events while smart mode (capture) is on in a
// transcript view. handled=false falls through to the existing wheel routing.
func (m model) onSmartMouse(msg tea.MouseMsg) (model, tea.Cmd, bool) {
	if m.mouseOff || (m.mode != modeChat && m.mode != modeAgent) {
		return m, nil, false
	}
	switch msg.Button {
	case tea.MouseButtonWheelUp, tea.MouseButtonWheelDown, tea.MouseButtonWheelLeft, tea.MouseButtonWheelRight:
		// The wheel keeps scrolling; a drag in progress cannot survive the
		// content moving under it, so it cancels cleanly.
		m.smartSel = smartSelState{}
		return m, nil, false
	}
	switch msg.Action {
	case tea.MouseActionPress:
		if msg.Button != tea.MouseButtonLeft {
			return m, nil, false
		}
		top, h := m.transcriptRegion()
		if top < 0 || h <= 0 || msg.Y < top || msg.Y >= top+h {
			m.smartSel = smartSelState{} // a click elsewhere drops a held highlight
			return m, nil, false
		}
		m.smartSel = smartSelState{active: true, ax: msg.X, ay: msg.Y, hx: msg.X, hy: msg.Y}
		return m, nil, true
	case tea.MouseActionMotion:
		if !m.smartSel.active {
			return m, nil, false
		}
		m.smartSel.hx, m.smartSel.hy = msg.X, msg.Y
		return m, nil, true
	case tea.MouseActionRelease:
		if !m.smartSel.active {
			return m, nil, false
		}
		sel := m.smartSel
		m.smartSel = smartSelState{}
		if sel.hx == sel.ax && sel.hy == sel.ay {
			return m, nil, true // a click is not a drag - no clipboard write
		}
		text := m.smartSelectionCopyText(sel)
		if text == "" {
			return m, nil, true // padding-only: no write, no toast
		}
		// Keep the selection visible until the copy result lands - it is the
		// recoverable artifact if every clipboard mechanism fails.
		m.smartSel = smartSelState{held: true, ax: sel.ax, ay: sel.ay, hx: sel.hx, hy: sel.hy}
		return m, smartCopyCmd(text), true
	}
	return m, nil, false
}

// smartSelectionCopyText maps the drag's screen cells through the viewport
// scroll offset into content rows and extracts the selection.
func (m model) smartSelectionCopyText(sel smartSelState) string {
	top, h := m.transcriptRegion()
	if top < 0 || h <= 0 {
		return ""
	}
	var yOff int
	var entries []string
	switch m.mode {
	case modeChat:
		yOff, entries = m.chatVP.YOffset, m.transcript
	case modeAgent:
		yOff, entries = m.agentVP.YOffset, m.displayAgentLines()
	}
	rows := selectionRows(entries, m.effWidth())
	clampY := func(y int) int { return min(max(y, top), top+h-1) }
	sr := clampY(sel.ay) - top + yOff
	er := clampY(sel.hy) - top + yOff
	return selectionText(rows, sr, sel.ax, er, sel.hx)
}

// onSmartCopyResult lands the honest clipboard outcome: success clears the
// highlight behind a counted toast; failure names both missing mechanisms and
// keeps the selection visibly recoverable.
func (m model) onSmartCopyResult(msg smartCopyResultMsg) model {
	if msg.ok {
		m.smartSel = smartSelState{}
		m.status = stLive.Render("✓ ") + stKey.Render(fmt.Sprintf("Copied %d characters to clipboard", msg.chars))
		return m
	}
	m.smartSel.held = true
	m.status = stEmber.Render("copy failed - no clipboard tool (wl-copy/xclip/xsel) and no OSC 52 path succeeded · selection kept · ctrl+o for native drag-copy")
	return m
}

// overlaySelection paints the current selection reverse-video onto the rendered
// frame, bounded to the transcript region. Pure restyling: the frame's text is
// untouched, so NO_COLOR and narrow layouts lose only the shimmer, never rows.
func (m model) overlaySelection(frame string) string {
	sel := m.smartSel
	if !sel.active && !sel.held {
		return frame
	}
	top, h := m.transcriptRegion()
	if top < 0 || h <= 0 {
		return frame
	}
	ax, ay, hx, hy := sel.ax, sel.ay, sel.hx, sel.hy
	if hy < ay || (hy == ay && hx < ax) {
		ax, ay, hx, hy = hx, hy, ax, ay
	}
	rows := strings.Split(frame, "\n")
	for y := max(ay, top); y <= hy && y < top+h && y < len(rows); y++ {
		from, to := 0, 1<<30
		if y == ay {
			from = ax
		}
		if y == hy {
			to = hx
		}
		rows[y] = highlightSpan(rows[y], from, to)
	}
	return strings.Join(rows, "\n")
}

// highlightSpan restyles columns [from, to] of one rendered row reverse-video,
// ANSI-aware and lossless (strip(result) == strip(row)).
func highlightSpan(row string, from, to int) string {
	w := ansi.StringWidth(row)
	if w == 0 || to < from || from >= w {
		return row
	}
	to = min(to, w-1)
	pre := ansi.Cut(row, 0, from)
	mid := ansi.Cut(row, from, to+1)
	post := ""
	if to+1 < w {
		post = ansi.Cut(row, to+1, w)
	}
	return pre + stSelHi.Render(ansi.Strip(mid)) + post
}
