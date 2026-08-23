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
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// chatTranscriptRows is the maximum height (rows) the CHANNEL transcript region may
// occupy, leaving room for the header, heading, prompt + footer. Kept identical to the
// pre-viewport tail budget so the layout is unchanged.
func (m model) chatTranscriptRows() int {
	chrome := 8
	if m.compact {
		chrome = 6
	}
	// Reserve the transient status + update-notice rows WHEN PRESENT so a toast never pushes
	// the channel hint bar off the bottom of the terminal (the "disappearing menu" fix): the
	// footer hint always stays on screen; the transcript gives back a row instead.
	if m.status != "" {
		chrome++
	}
	if m.updateLine != "" {
		chrome++
	}
	chrome += max(0, m.chatPromptRowCount(m.effWidth())-1)
	max := m.height - chrome
	if max < 6 {
		if m.height > 0 {
			max = 1
		} else {
			max = 12
		}
	}
	return max
}

func (m model) chatView(w int) string {
	var b strings.Builder
	sys := ""
	if m.sysPrompt != "" {
		sys = stDim.Render(" · system set")
	}
	// Section-tab heading. MODE CLARITY: TUNE-IN (basic chat, NO tools) must read as
	// visibly distinct from the AGENT (tool-calling) view, which shares the same shape - so
	// here the accent bar is MONO (vs the AGENT's red bar) and the label spells out
	// "TUNE-IN · chat (no tools)". Matches the [1] TUNE IN preset naming. COMPACT keeps the
	// identity but trims the parenthetical.
	// The cost readout is the header's last field on a MARKET channel. A DIRECT channel
	// (your own private band, model running here) has no meter at all, so it prints the
	// route in its place - never "cost $0.00", which would assert a measured charge.
	costField := stDim.Render("   cost ") + stEmber.Render(dollars(m.sessCost))
	costFieldCompact := stDim.Render(" · ") + stEmber.Render(dollars(m.sessCost))
	if m.chatLocalChat != "" {
		costField = stDim.Render("   ") + stRed.Render(glyphOnAir) + stDim.Render(" direct · nothing metered")
		costFieldCompact = stDim.Render(" · ") + stRed.Render(glyphOnAir) + stDim.Render(" direct")
	}
	if m.compact {
		head := "  " + stDim.Render("▌") + " " + stBrand.Render("TUNE-IN") + stDim.Render(" · chat  ") +
			stGold.Render(channelGlyph(m.connected)) + stDim.Render(" "+m.connected.NodeID+" · ") + stKey.Render(m.connected.Model) +
			costFieldCompact + sys
		b.WriteString(truncVisible(head, w) + "\n")
	} else {
		b.WriteString("  " + stDim.Render("▌") + " " + stBrand.Render("TUNE-IN") + stDim.Render(" · chat (no tools)") +
			stDim.Render("   ") + stGold.Render(channelGlyph(m.connected)) + stDim.Render(" "+m.connected.NodeID+" · ") + stKey.Render(m.connected.Model) +
			costField + sys + "\n")
	}
	// Scrollable transcript: an independent viewport (you ▸ / them ◂) that the user can
	// page through (PgUp/PgDn, mouse wheel, arrows once history is exhausted) while the
	// input below keeps typing. Sized to min(content, budget) so a short transcript reads
	// exactly as before and a tall one caps + scrolls. The persisted scroll position (and
	// auto-stick-to-bottom) is managed in refreshScroll; here we only render at it.
	content := transcriptContent(m.displayChatLines(w), w)
	m.chatVP.Width = w
	m.chatVP.Height = clampRows(lineRows(content), m.chatTranscriptRows())
	m.chatVP.SetContent(content)
	if m.chatVP.Height > 0 {
		b.WriteString(m.chatVP.View() + "\n")
	}
	// While a reply is in flight, Ping relays it: a subtle one-line transmit with an
	// elapsed-seconds readout so a slow CPU inference reads as progress, not a hang.
	// It sits just under the last message and never displaces the transcript.
	if m.relaying {
		elapsed := 0
		if !m.relayStart.IsZero() {
			elapsed = int(time.Since(m.relayStart).Seconds())
		}
		// COMPACT freezes the ((•)) working spinner to a static (•) glyph + phrase (no
		// ring animation), per the reduced-motion contract.
		line := "  " + m.transmitLineFor(elapsed)
		// Once the session has billed turns, the running session-so-far rides on the wait via
		// the SAME shared sessionFooter the AGENT prints after each turn — so a multi-turn
		// channel reads its running ↑↓ + cost while it holds the channel (the in-flight turn
		// itself hasn't billed yet, so this is honestly the prior turns' total).
		if f := sessionFooter(m.sessTokensIn, m.sessTokensOut, m.sessCost); f != "" {
			line += "   " + f
		}
		b.WriteString(line + "\n")
	}
	// The always-live channel prompt uses the same lossless, cell-aware textarea
	// contract as AGENT. Continuation rows align under the authored value.
	b.WriteString("\n" + strings.Join(m.chatPromptLines(w), "\n") + "\n")
	// Phase 2 (de-crowd): the single hint bar (the footer, Zone 4) is the ONE place the
	// channel keys are taught - the duplicate in-view key line that used to print here is
	// gone, giving the transcript back a row.
	return b.String()
}

const (
	chatPromptLead      = "  ▌ you › "
	chatPromptLeadWidth = 10
	chatPromptMaxRows   = 6
)

func (m model) chatPromptRowCount(w int) int {
	contentWidth := max(1, w-chatPromptLeadWidth)
	value := m.chatIn.Value()
	if value == "" {
		return 1
	}
	rows := 0
	for _, logical := range strings.Split(value, "\n") {
		wrapped := ansi.Wrap(logical, contentWidth, "")
		rows += max(1, lineRows(wrapped))
	}
	return min(chatPromptMaxRows, max(1, rows))
}

func (m model) chatPromptLines(w int) []string {
	view := renderComposer(m.chatIn, m.chatIn.Placeholder, chatPromptLead, chatPromptLeadWidth, w, m.chatPromptRowCount(w))
	return tintComposerLines(strings.Split(view, "\n"), w)
}

// The CHANNEL's turns are TAGGED, not pre-rendered, for the same reason the agent's
// are: the telegram blocks span the view, and only the display path knows how wide that
// is. A block built at append time is stuck at whatever the width was when the message
// arrived, and wrong after the next resize.
const (
	chatAskMark   = "\x02" // a YOU turn; payload is the text
	chatReplyMark = "\x1d" // a ROGER turn; payload is "model\x00text"
)

func chatUserBlock(text string) string { return chatAskMark + text }

func chatAnswerBlock(modelName, text string) []string {
	return []string{"", chatReplyMark + modelName + "\x00" + text}
}

// chatUserRows paints a YOU turn's rows (before enclosure).
func chatUserRows(text string, w int) []string {
	rows := strings.Split(ansi.Wrap(ansi.Strip(text), max(1, w-8), ""), "\n")
	out := make([]string, 0, len(rows))
	for i, r := range rows {
		if i == 0 {
			out = append(out, lipgloss.NewStyle().Foreground(cLive).Bold(true).Render("▌ ")+
				lipgloss.NewStyle().Foreground(cSlateText).Bold(true).Render("YOU › "+r))
			continue
		}
		out = append(out, lipgloss.NewStyle().Foreground(cSlateText).Bold(true).Render("      "+r))
	}
	return out
}

// chatReplyRows paints a ROGER turn's rows (before enclosure): a header naming the
// station, then the prose.
func chatReplyRows(modelName, text string, w int) []string {
	head := stLive.Render("◂ ") + lampStyle(roleDial).Bold(true).Render("ROGER ›")
	if modelName != "" {
		head += stDim.Render(" " + modelName)
	}
	out := []string{head}
	for _, para := range strings.Split(text, "\n") {
		for _, line := range strings.Split(ansi.Wrap(para, max(1, w-4), ""), "\n") {
			out = append(out, stLive.Render("▏ ")+line)
		}
	}
	return out
}

// transmitLineFor is transmitLine but honors compact: a static spinner under compact
// (no ring animation), the live animated one otherwise. The elapsed-seconds readout
// is kept in both so a slow station still reads as alive, not hung.
func (m model) transmitLineFor(elapsedSec int) string {
	if m.compact {
		line := staticSpinner()
		if elapsedSec >= 2 {
			line += stDim.Render(fmt.Sprintf("  %ds (holding the channel)", elapsedSec))
		}
		return line
	}
	return transmitLine(m.frame, elapsedSec)
}

// transmitLine is Ping's inline relay indicator: the working spinner (on-air beacon
// + rotating radio phrase) plus an elapsed-seconds readout once a reply is slow.
// Single line, so it never obstructs the chat transcript. The elapsed counter
// reassures on slow inference (CPU MoE replies can take a minute) that the request
// is alive, not hung.
func transmitLine(frame, elapsedSec int) string {
	line := workingSpinner(frame)
	switch {
	case elapsedSec >= 90:
		// Very slow: surface the hard per-call ceiling so the wait reads as BOUNDED, not
		// bottomless - the "is it hung?" question gets a concrete deadline (the relay times
		// out at ~5m), instead of an open-ended spinner.
		line += stDim.Render(fmt.Sprintf("  %ds  (still holding · the station has up to ~5m before it times out)", elapsedSec))
	case elapsedSec >= 2:
		line += stDim.Render(fmt.Sprintf("  %ds  (slow stations can take a minute - holding the channel)", elapsedSec))
	}
	return line
}
