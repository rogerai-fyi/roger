// Package tui is the interactive `rogerai` experience - a two-way radio for Local Models,
// and the terminal twin of the website's "Live Operating Manual". Stations
// (providers) go on air; you tune in to a channel and talk. The look is the web's:
// ~95% monochrome + ONE red beacon, the shared instrument glyphs (◉ on air, ○ off
// air, ◆ verified, ▁▂▃▄▅▆▇█ signal bars), flat hairline structure, and a single
// carrier beat driving the beacon, the ((•)) spinner, and the signal-bar shimmer.
// Built on Bubble Tea + Lipgloss.
package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"rogerai.fm/roger/v6/internal/glyphs"
)

// quitConfirmView is the on-air quit-guard: a clear "you are ON AIR - quit and go
// off air?" prompt with the SAFE default on NO (keep sharing). Shown only while at
// least one model is live (requestQuit gates entry).
// panelFit renders body inside the standard bordered panel, CLAMPED to the terminal.
//
// stPanel sizes its border to the CONTENT, so a panel built from prose was whatever width
// its longest line happened to be - 68 cells for the quit guard, regardless of the
// terminal. On a narrow or minimized window every one of those boxes ran off the screen,
// which the compact audit caught across four screens at once.
//
// Two geometry facts, learned the hard way on the [3] CONFIG edit box and stated here so
// they are stated once: Style.Width() sets the TOTAL width INCLUDING padding (so the
// content gets width-2), and MaxWidth does NOT prevent a wrap - it clips a block that has
// already wrapped. Prose is allowed to wrap here, unlike a single-line field; what must
// never happen is a border wider than the screen.
// clampLines trims every line of a rendered view to w.
//
// It is the LAST line of defence, not the design: a view should shorten its own prose and
// compress its own columns, because a clamp cuts mid-word and tells the operator nothing
// about what was lost. But a screen that runs off the terminal wraps, and a wrapped dense
// view is what makes the whole app look broken - so the invariant is worth holding
// unconditionally even where the fix above it is imperfect.
func clampLines(s string, w int) string {
	if w <= 0 {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		if lipgloss.Width(ln) > w {
			lines[i] = truncVisible(ln, w)
		}
	}
	return strings.Join(lines, "\n")
}

func panelFit(body string, w int) string {
	const chrome = 4 // 2 border + 2 padding
	avail := max(8, w-chrome)
	inner := lipgloss.Width(body)
	if inner > avail {
		inner = avail
	}
	return stPanel.Width(inner + 2).Render(body)
}

// rnd rounds a float term contribution to the nearest int for the breakdown line.
func rnd(v float64) int { return int(v + 0.5) }

func truncVisible(s string, n int) string {
	if lipgloss.Width(s) <= n {
		return s
	}
	return ansi.Truncate(s, n, "")
}

// truncVisibleTail is truncVisible with a graceful "…" tail (folded to "..." under ASCII):
// a line that is actually cut ends in an ellipsis so the clip reads as intentional, never a
// jarring mid-word hard cut. A line that fits is returned untouched. Used by the hand-off
// plates so a narrow terminal degrades cleanly.
func truncVisibleTail(s string, n int) string {
	if lipgloss.Width(s) <= n || n <= 0 {
		return s
	}
	return ansi.Truncate(s, n, glyphs.Fold("…"))
}

// clampBrowse keeps m.cursor + m.browseTop valid against the current FILTERED view.
// Called after anything that can change the visible-set size (a re-scan, a filter
// edit, a toggle, a sort) so the cursor never points past the list and the window
// never strands rows. Pointer receiver: it mutates the model in place.
func (m *model) clampBrowse() {
	vis := m.visibleBands()
	n := len(vis)
	// STICKY SELECTION: keep the cursor on the SAME band across re-sorts/redraws. A periodic
	// re-scan re-sorts the list (by signal), so a bare positional cursor would suddenly point at
	// a different band - Enter would then tune the WRONG one. Re-find the selected model in the
	// new order and move the cursor to it.
	if m.selectedModel != "" {
		for i, b := range vis {
			if b.model == m.selectedModel {
				m.cursor = i
				break
			}
		}
	}
	if m.cursor >= n {
		m.cursor = n - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	// Remember the band now under the cursor, so the next re-sort re-anchors to it.
	if n > 0 && m.cursor >= 0 && m.cursor < n {
		m.selectedModel = vis[m.cursor].model
	}
	if m.browseTop > m.cursor {
		m.browseTop = m.cursor
	}
	if m.browseTop < 0 {
		m.browseTop = 0
	}
}

// pad truncates (with an ellipsis) or right-pads s to n display runes.
func pad(s string, n int) string {
	// A NON-POSITIVE width is empty, not a panic. Every column width here is derived from
	// the terminal width, so a window narrow enough to drive one to zero would take
	// r[:n-1] to r[:-1] and crash the whole app - on the one input an operator can produce
	// by dragging a window edge. Found by a lock walking bandNameCell down to w=0.
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s + strings.Repeat(" ", n-len(r))
}

// fmtCtx renders a context window like the web's fmtCtx: "131k" / "32k" / "-". The
// caller adds the "~" + dim styling for an estimated window.
func fmtCtx(ctx int) string {
	if ctx <= 0 {
		return "-"
	}
	if ctx >= 1000 {
		return fmt.Sprintf("%dk", (ctx+500)/1000)
	}
	return strconv.Itoa(ctx)
}

// fmtTtft renders a probe TTFT like the web: "180ms" / "1.4s" / "-" (unmeasured).
func fmtTtft(ms float64) string {
	if ms <= 0 {
		return "-"
	}
	if ms >= 1000 {
		return fmt.Sprintf("%.1fs", ms/1000)
	}
	return fmt.Sprintf("%dms", int(ms+0.5))
}

// clampRows bounds a row count to [0, max] - the viewport height is min(content, max)
// so a short transcript renders exactly as tall as it is (no padding, unchanged layout)
// and a tall one caps at max rows and becomes scrollable.
func clampRows(rows, max int) int {
	if rows > max {
		rows = max
	}
	if rows < 0 {
		rows = 0
	}
	return rows
}

// plural renders "1 band" / "3 bands": a count with its noun, pluralised unless n == 1.
//
// The -es cases are here because a bare +s produced "searchs" and "fetchs" the moment
// this was used for anything but bands. A count is a thing a reader is being asked to
// trust, and a line that cannot spell its own noun is a line they stop trusting.
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	suffix := "s"
	if strings.HasSuffix(noun, "s") || strings.HasSuffix(noun, "x") ||
		strings.HasSuffix(noun, "ch") || strings.HasSuffix(noun, "sh") {
		suffix = "es"
	}
	return fmt.Sprintf("%d %s%s", n, noun, suffix)
}

// humanTokens renders a token count compactly: 340, 1.3k, 12.0k.
func humanTokens(n int) string {
	if n >= 1000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return strconv.Itoa(n)
}

// humanLatency renders a request duration as a calm readout: 850ms below a second, 2.1s above.
func humanLatency(d time.Duration) string {
	if d <= 0 {
		return ""
	}
	if d >= time.Second {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return fmt.Sprintf("%dms", d.Milliseconds())
}
