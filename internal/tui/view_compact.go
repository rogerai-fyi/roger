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
	"strings"

	"github.com/charmbracelet/lipgloss"
	"rogerai.fm/roger/v6/internal/agent"
)

// shortTerminal reports a window with no rows to spare for decoration. A frame taller than
// the terminal scrolls the alt buffer and strands the previous frame's header above it -
// the stacked-ROGER-logos failure - so on a short window the blank separators and the
// teaching signposts come out before the content does.
func (m model) shortTerminal() bool { return m.height > 0 && m.height <= 22 }

func (m model) compactHeader(w int) string {
	dot := stRed.Render(beaconDot())
	brand := stBrand.Render("ROGER") + stTag.Render("·AI")
	sep := stDim.Render(" · ")
	hint := stDim.Render("m:expand")

	var mid string
	if m.connected != nil {
		// Channel context: the load-bearing "what am I on + price + balance".
		o := m.connected
		// "♪ now playing" framing: the tuned-in model reads like a track on a deck.
		mid = stLive.Render("♪ ") + stGold.Render(channelGlyph(o)) + stLive.Render(" on ") + stSelText.Render("@"+o.NodeID) +
			sep + stKey.Render(o.Model) +
			sep + stEmber.Render(dollars(o.PriceOut)+"/1M") + priceTierSuffix(o.PriceTier, o.PriceOut)
	} else {
		// Browsing: the section + how many LLM bands are on air. Counts LLM (chat) bands only so
		// the figure matches the windowshade deck (which renders voice-excluded visibleBands);
		// voices do NOT take deck cells. Instead they fold into a single dim "· N DJs" count
		// (only when any are on air) — voice's one, quiet compact affordance.
		summary := "scanning…"
		if m.scanned {
			summary = fmt.Sprintf("%d on air · %d bands", m.llmBandsOnAir(), m.llmBands())
			if v := m.voiceBandsOnAir(); v > 0 {
				summary += " · " + plural(v, "DJ")
			}
		}
		section := "TUNE IN"
		if m.inShareSection() {
			section = "SHARE"
		}
		state := stKey.Render(section) + sep + stDim.Render(summary)
		if m.onAir && m.share != nil {
			state = m.headlineBadge() + sep + state
		}
		mid = state
	}

	// The account tag carries the wallet, the other load-bearing bit. The compact form
	// is terse - ✓ @login · $bal collapses to just $bal (or /login when anonymous) - so
	// the dense strip stays short and the m:expand hint never gets crowded out.
	acct := m.accountTag(true)
	if m.loggedInState() && m.ghLogin != "" {
		// Logged in: keep the callsign + balance (the identity is worth the few cols).
		acct = stGold.Render(glyphLineage) + stDim.Render(" @") + stSelText.Render(m.ghLogin)
		if m.haveBal {
			acct += stDim.Render(" ") + stEmber.Render(dollars(m.balance))
		}
	}

	hintVis := lipgloss.Width(hint)
	// The abstract EQ pane was replaced by per-band signal bars in the windowshade list (which
	// are meaningful at a glance); the header now carries the clear "N on air · M bands" count.
	left := dot + " " + brand + sep + mid + sep + acct
	// Right-align the hint when there's room; otherwise it trails inline. We measure on
	// the visible (ANSI-stripped) width so color never throws off the geometry.
	leftVis := lipgloss.Width(left)
	rule := stHeadRule.Render(strings.Repeat("-", w))
	if leftVis+2+hintVis <= w {
		gap := w - leftVis - hintVis
		return left + strings.Repeat(" ", gap) + hint + "\n" + rule
	}
	// Too narrow for the gap: trim the left strip to fit "… m:expand" on one line so it
	// never overflows. truncVisible cuts on display width, ANSI-safe.
	budget := w - hintVis - 1
	if budget < 0 {
		budget = 0
	}
	return truncVisible(left, budget) + " " + hint + "\n" + rule
}

// compactBandList renders the COMPACT windowshade band deck: ON-AIR bands only, packed two per
// row as a name + a STATIC signal bar (reduced motion - the bar height is the band's signal,
// not a frame). The selected band carries the › cursor. No column grid, offline rows, prices,
// ctx, or flags - just the at-a-glance "what's live + how strong". Width-clamped per cell.
func (m model) compactBandList(w int, vis []band, total int) string {
	if len(vis) == 0 {
		return "  " + stDim.Render(beaconDot()+" no stations on air right now · ") + stKey.Render("[2]") +
			stDim.Render(" share · ") + stKey.Render("m") + stDim.Render(" expand · r re-scan") + "\n"
	}
	var b strings.Builder
	colW, step := w/2, 2
	if colW < 18 {
		colW, step = w, 1 // too slim to pair: ONE band per row (step matches, so none dropped)
	}
	for i := 0; i < len(vis); i += step {
		row := "  " + m.compactBandCell(vis[i], i == m.cursor, colW-3)
		if step == 2 && i+1 < len(vis) {
			row += " " + m.compactBandCell(vis[i+1], i+1 == m.cursor, colW-3)
		}
		b.WriteString(truncVisible(row, w) + "\n")
	}
	return b.String()
}

// compactBandCell is one windowshade cell: a 2-col marker (› cursor + ◉ on-air), the band name,
// and a static signal bar. The selected band's name is highlighted.
func (m model) compactBandCell(bd band, sel bool, width int) string {
	sig := int(bandSignal(bd))
	if sig < 0 {
		sig = 0
	}
	if sig > 100 {
		sig = 100
	}
	bar := strings.Repeat(string(spectrumBlocks[sig*(len(spectrumBlocks)-1)/100]), 5)
	nameW := width - 9 // 2 marker + 1 sp + name + 1 sp + 5 bar
	if nameW > 18 {
		nameW = 18 // keep names tight so the bar sits close (no big gap); 2 cells still fit
	}
	if nameW < 6 {
		nameW = 6
	}
	name := bd.model
	if len([]rune(name)) > nameW {
		name = string([]rune(name)[:nameW])
	}
	marker := stDim.Render(" ") + stRed.Render(glyphOnAir) // unselected: " ◉"
	nameSty := stKey
	if sel {
		marker = stSelText.Render(">") + stRed.Render(glyphOnAir) // selected: ">◉" (the TUI carat)
		nameSty = stSelText
	}
	return marker + " " + nameSty.Render(fmt.Sprintf("%-*s", nameW, name)) + " " + stDim.Render(bar)
}

// compactOnAirLine is the windowshade (compact mode) one-line ON AIR summary: the
// beacon + band count + aggregate served + total earnings, e.g.
// "(•) ON AIR · sharing 3 · 42 served · $0.18 · /share off". It sums EVERY live
// band (not just the headline), and is width-truncated + NO_COLOR safe.
func (m model) compactOnAirLine(w int) string {
	live := m.liveShares()
	if len(live) == 0 {
		return ""
	}
	anyOnAir := false
	var totReqs int64
	var totEarn float64
	for _, s := range live {
		if s.Link() == agent.LinkOnAir {
			anyOnAir = true
		}
		r, _ := s.Served()
		totReqs += r
		totEarn += s.Earnings()
	}
	badge := stRed.Render(glyphOnAir + " ON AIR")
	if !anyOnAir {
		badge = stEmber.Render(glyphOffAir + " RECONNECTING")
	}
	line := "  " + badge +
		stDim.Render(fmt.Sprintf(" · sharing %d · %d served · ", len(live), totReqs)) +
		stEmber.Render(dollars(totEarn)) +
		stDim.Render(" · /share off")
	return truncVisible(line, w)
}

// compactFooter is the windowshade single-line key-hint footer: a hairline rule, a
// terse per-mode hint, then the account tag and the `m expand` reminder. Width-safe:
// the hint is trimmed to fit before the rule, and a fresh status note (if any) rides
// one line under it so an action still surfaces an outcome.
// compactKnowsMode reports whether the windowshade footer has a key line written FOR this
// screen. Anything else must not be handed the default, which teaches the dial's keys.
func compactKnowsMode(md mode) bool {
	switch md {
	case modeBrowse, modeChat, modeAgent, modeShare, modeLimits,
		modeShareEditor, modeShareSetup, modeConnectConfirm, modeOverLimit:
		return true
	}
	return false
}

func (m model) compactFooter(w int) string {
	rule := stHeadRule.Render(strings.Repeat("-", w))
	var keys string
	switch m.mode {
	case modeChat:
		keys = "talk · esc disconnect · tab peek · shift-tab agent · ⌃y copy"
	case modeAgent:
		keys = "ask · ⌃y copy · /model · esc exit · write/run confirm"
	case modeShare:
		// esc was missing entirely: the windowshade's densest screen was also the one
		// with no stated exit.
		keys = "↑↓ · ⏎/a air · p price · b card · esc"
	case modeLimits:
		keys = "↑↓ · ⏎ edit · d clear · esc"
	case modeShareEditor:
		keys = "tab field · ⏎ save · esc"
	case modeShareSetup:
		keys = "↑↓ · ⏎ · r · esc"
	case modeConnectConfirm:
		keys = "⏎/y accept · esc deny"
	case modeOverLimit:
		keys = "⏎ save · ↑↓ · w wait · esc"
	default:
		keys = "↑↓ · ⏎ tune · s sort · / · ?"
	}
	hint := stDim.Render(keys) + stDim.Render("  ·  ") + stKey.Render("m") + stDim.Render(" expand") +
		stDim.Render("  ·  ") + m.accountTag(true)
	line := truncVisible("  "+hint, w)
	st := ""
	if m.status != "" {
		// Tail-ellipsis, not a hard cut: a status line is where broker rejections land,
		// and a bare clip ("...: brok") reads as a corrupted message rather than a
		// truncated one - the operator cannot tell there is more to know.
		st = "\n" + truncVisibleTail("  "+m.status, w)
	}
	return rule + "\n" + line + st
}
