package tui

// SELECTED-CELL MARQUEE - read the whole name without widening the column.
//
// A table cell that cannot hold its text is elided ("deepseek/deepseek-v…"). That is fine
// for the twenty rows you are not looking at and useless for the one you are, so the
// SELECTED cell - and only that one - slides its text through the same column until the
// whole string has been read, then starts over.
//
// Three rules make this safe to bolt onto tables whose exact rendered text dozens of
// tests already assert:
//
//   FRAME ZERO IS TODAY'S RENDER, BYTE FOR BYTE. Offset 0 does not re-derive the elided
//   cell, it DELEGATES to the very builder (pad / truncVisible) the table used before
//   this file existed. The identity is true by construction, so a test that pins a padded
//   row at a fixed width keeps passing untouched.
//
//   THE COLUMN NEVER GROWS. Only the window moves. Every offset yields the same number of
//   grapheme clusters the static cell did - the same unit pad() counts in - so the
//   marquee is exactly as width-safe as the cell it replaces, never worse.
//
//   IT ONLY MOVES WHEN IT HAS TO. Text that fits is returned static at every offset; a
//   list where most rows fit does not jitter, and a screen with nothing overflowing lets
//   the carrier beat freeze (which is what keeps the terminal's native mouse selection
//   alive between repaints).
//
// The window steps in GRAPHEME CLUSTERS, never bytes and never bare runes, so an emoji
// sequence or a combining mark is never sliced in half at any offset.

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/rivo/uniseg"
)

// MARQUEE RHYTHM, in carrier ticks. The app has exactly one beat and it is 160ms (tick()),
// so these are the timings in real seconds:
//
//	hold  8 ticks ~ 1.3s   The eye has to land on the START of the name before it moves.
//	                       Any shorter and the row is already sliding by the time you look
//	                       at it, which is precisely what makes most marquees unreadable.
//	step  2 ticks ~ 320ms  One column per step, ~3 columns/second. Deliberately calm: this
//	                       app freezes its animation when idle on purpose, and a name that
//	                       whips past is no more readable than one that is cut off. A
//	                       20-column overrun reads out in about six seconds.
//	end  12 ticks ~ 1.9s   The tail is the half you came for, so it rests longer than the
//	                       head does before the snap back to the start.
const (
	marqueeHoldFrames = 8
	marqueeStepFrames = 2
	marqueeEndFrames  = 12
)

// marqueeCycle is one full hold-scroll-hold cycle in ticks. A span of 0 has no cycle;
// 1 is returned instead of 0 so callers can take a modulus without guarding.
func marqueeCycle(span int) int {
	if span <= 0 {
		return 1
	}
	return marqueeHoldFrames + span*marqueeStepFrames + marqueeEndFrames
}

// marqueePhase maps ticks-since-selected to a column offset in [0, span], looping: hold at
// 0, then one column every marqueeStepFrames, then hold at span, then back to 0. A
// non-positive elapsed (a fresh model, or a frame counter that has not moved yet) is 0.
func marqueePhase(elapsed, span int) int {
	if span <= 0 || elapsed <= 0 {
		return 0
	}
	t := elapsed % marqueeCycle(span)
	if t < marqueeHoldFrames {
		return 0
	}
	if col := (t-marqueeHoldFrames)/marqueeStepFrames + 1; col <= span {
		return col
	}
	return span
}

// marqueeTravel is how many columns a cell has to travel before its whole text has been
// read: the number of grapheme clusters the text overruns w by, or 0 when it fits.
func marqueeTravel(s string, w int) int {
	if w <= 0 {
		return 0
	}
	if n := uniseg.GraphemeClusterCount(s); n > w {
		return n - w
	}
	return 0
}

// marqueeClusters splits s into grapheme clusters - the unit the window steps in, so no
// offset can ever land inside a multi-rune character.
func marqueeClusters(s string) []string {
	out := make([]string, 0, len(s))
	g := uniseg.NewGraphemes(s)
	for g.Next() {
		out = append(out, g.Str())
	}
	return out
}

// marqueeCellWindow is the off-th w-column window over s, ending in tail while text remains
// to the right. ok is false at offset 0 and for text that fits - the caller then renders
// its own static form, which is what keeps frame zero byte-identical to today.
//
// NOT the same thing as marquee()/marqueeWindow() over in the SHARE banner + Ping World
// ticker: those LOOP a line of prose around a gap, rune by rune, and their frame zero is the
// head of the string. A table cell cannot do that - its frame zero has to be the elided cell
// the table already draws, it must wear an ellipsis while text remains, and it must stop dead
// at the tail rather than wrap through a gap. Different contract, so a separate function.
//
// tail is the caller's OWN truncation marker ("…" for pad, "" for truncVisible's hard
// cut), so the moving form wears the same clothes as the still one.
func marqueeCellWindow(s string, w, off int, tail string) (string, bool) {
	span := marqueeTravel(s, w)
	if off <= 0 || span <= 0 {
		return "", false
	}
	if off > span {
		off = span // the end of the road; never past it
	}
	g := marqueeClusters(s)
	if off == span {
		// The tail of the text, in full: there is nothing further right to promise, so
		// the marker comes off and the last column carries a real character.
		return truncVisible(strings.Join(g[len(g)-w:], ""), w), true
	}
	keep := max(0, w-uniseg.GraphemeClusterCount(tail))
	// CLAMP TO DISPLAY COLUMNS, not clusters. w clusters of CJK is 2w columns wide;
	// pad() counts runes and would overflow there too, but a moving cell that is wider
	// than the still one it replaces is a regression this feature must not introduce, so
	// the window is trimmed to the real column budget on its way out.
	return truncVisible(strings.Join(g[off:off+keep], "")+tail, w), true
}

// padMarquee is pad() with a marquee: the same elided-and-padded cell at offset 0, sliding
// one column per offset after that. The pad flavour keeps pad's "…" marker.
func padMarquee(s string, w, off int) string {
	if win, ok := marqueeCellWindow(s, w, off, "…"); ok {
		// The column clamp can leave a cell a column short where a wide rune straddled
		// the edge; pad's cells are always full, so this one is too.
		if d := w - lipgloss.Width(win); d > 0 {
			win += strings.Repeat(" ", d)
		}
		return win
	}
	return pad(s, w)
}

// cutMarquee is truncVisible() with a marquee: a HARD cut, no marker, for the cells whose
// static form has none (the model half of a quanted "name Q4_K_M" identity cell, where the
// quant is pinned on the right and only the name has room to move).
func cutMarquee(s string, w, off int) string {
	if win, ok := marqueeCellWindow(s, w, off, ""); ok {
		return win
	}
	return truncVisible(s, w)
}

// --- the model side: which cell scrolls, and how far it has got -------------------------

// marqueeSel returns the SOURCE text and the column width of the ONE cell that may scroll
// on the screen the operator is looking at, and whether such a cell exists at all.
//
// This is where "not visible" and "not focused" are decided, in one place: only the two
// long-name tables answer, only while THEY hold the cursor. The filter line (modeCommand
// draws the band table but owns the keyboard) and the SHARE station rename both take the
// focus away from the list, and both fall through to false.
func (m model) marqueeSel() (string, int, bool) {
	switch {
	case m.mode == modeBrowse:
		bd, ok := m.selectedBand()
		if !ok {
			return "", 0, false
		}
		w := m.bandNameW()
		if m.narrow() {
			// The narrow grid drops the quant entirely and renders the bare model; a
			// CONNECTED row also leads with the lit ◉, which costs the name two columns.
			// Measure what the row draws, or the scroll stops short of the tail (or runs
			// on a cell that never moves).
			if m.connectedModel() == bd.model {
				w -= 2
			}
			return bd.model, w, true
		}
		text, cw, _ := bandNameParts(bd, w)
		return text, cw, true
	case m.mode == modeShare && !m.renaming:
		if m.shareCursor < 0 || m.shareCursor >= len(m.shareRows) {
			return "", 0, false
		}
		return shareModelCell(m.shareRows[m.shareCursor]), m.shareNameW(m.effWidth()), true
	}
	return "", 0, false
}

// selMarqueeTravel is how far the currently SELECTED cell has to travel (0 = it fits, or
// there is nothing selected to scroll). Named apart from the package-level marqueeTravel
// so a reader never has to work out which of the two a bare call meant.
func (m model) selMarqueeTravel() int {
	text, w, ok := m.marqueeSel()
	if !ok {
		return 0
	}
	return marqueeTravel(text, w)
}

// marqueeStill reports whether the scroll is held at frame zero regardless of what is
// selected. Two reasons, and both are contracts older than this feature:
//
//   - THE WINDOWSHADE is the app's reduced-motion mode; everything freezes in it.
//   - NATIVE SELECTION owning the mouse (ctrl+o / "/mouse") is a promise, spelled out in
//     conversation_hierarchy_and_selection.feature and chat_prompt_wrapping.feature, that
//     idle ticks will not repaint - because a repaint wipes the highlight the operator is
//     dragging. A marquee is a repaint every other tick, so it stands down until they
//     hand the mouse back. Smart mouse mode (the default) owns its own selection and is
//     unaffected, so the feature is on for almost everyone almost always.
//
// NO_COLOR is deliberately NOT in this list. It strips color; it does not remove the need
// to read a name, and for an elided cell the marquee IS the reading. It is information,
// not decoration, so it survives where the beacon and the signal shimmer do not.
func (m model) marqueeStill() bool { return m.compact || m.mouseOff }

// marqueeRunning reports whether a marquee is actually in motion right now - the carrier
// beat's reason to keep ticking. It reads the key syncMarquee already computed rather than
// re-deriving the selection, so the per-tick cost is a string compare.
func (m model) marqueeRunning() bool { return !m.marqueeStill() && m.marqKey != "" }

// marqueeOff is the column offset the selected cell is currently showing.
func (m model) marqueeOff() int {
	text, w, ok := m.marqueeSel()
	if !ok {
		return 0
	}
	span := marqueeTravel(text, w)
	if m.marqueeStill() || span <= 0 {
		return 0
	}
	return marqueePhase(m.frame-m.marqFrame, span)
}

// marqueeKey identifies the scrolling cell. Everything that should restart the scroll is
// in it - the screen, the column width, and the text itself - so moving the selection,
// resizing the terminal, or leaving the screen all re-anchor to frame zero for free. It is
// empty when nothing should scroll, which is also the "no marquee" state.
func (m model) marqueeKey() string {
	text, w, ok := m.marqueeSel()
	if !ok || marqueeTravel(text, w) <= 0 {
		return ""
	}
	return strconv.Itoa(int(m.mode)) + "\x00" + strconv.Itoa(w) + "\x00" + text
}

// syncMarquee re-anchors the scroll whenever the cell under the cursor changes. Called
// once, centrally, from Update - so no key handler has to remember to reset it.
func (m *model) syncMarquee() {
	if k := m.marqueeKey(); k != m.marqKey {
		m.marqKey, m.marqFrame = k, m.frame
	}
}

// animating reports whether anything on screen is actually moving this frame - the gate
// that decides whether the carrier beat advances or FREEZES. A frozen frame is what lets
// the terminal's own mouse selection survive a repaint, so the set is kept deliberately
// small; a marquee joins it only while a selected cell genuinely overflows.
//
// dialSettling is passed in because only the tick handler knows whether the tuning-dial
// pointer is still gliding toward its detent.
func (m model) animating(dialSettling bool) bool {
	// A TRANSIENT toast keeps the clock alive only until its dismiss window closes;
	// without that bound the persistent browse ambient summary would pin the clock on
	// forever and native selection would never survive a repaint.
	toastPending := m.status != "" && m.statusFrame > 0 && m.frame-m.statusFrame < toastFrames &&
		(m.mode == modeBrowse || m.mode == modeCommand || m.mode == modeChat || m.mode == modeAgent)
	return m.relaying || m.agentBusy || m.shareLoading ||
		m.mode == modeConnecting || m.mode == modePingWorld || toastPending || dialSettling ||
		m.marqueeRunning()
}
