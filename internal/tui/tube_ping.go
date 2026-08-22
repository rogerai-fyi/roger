package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"rogerai.fm/roger/v6/internal/glyphs"
)

const (
	tubePingMinWidth    = 24
	tubePingDebutFrames = 10 // 2 seconds at the calm 200ms Ping World cadence.
	tubePingWalkW       = 12
	tubePingWalkH       = 5
)

type tubePingPose uint8

const (
	tubePingIdle tubePingPose = iota
	tubePingTransmit
	tubePingBlink
)

// The body interior is SEVEN cells wide, and that is load-bearing rather than
// arbitrary. ROG is three cells; an odd interior is the only way it pads evenly
// (2/2) and the only way the eye can share a column with the O beneath it. The
// v5.4.8 compaction cut the interior to six, which forced the wordmark to 2/1 and
// shoved it against the right wall - the walk sprite below kept seven and stayed
// balanced, which is why the two forms stopped agreeing. Everything here centres
// on column 7: cap, eye, wordmark, base, and feet.
var tubePingRows = []string{
	"   ▄███████▄",
	"(  █   •   █▓  )",
	"   █  ROG  █▓",
	"    ▀█▄▄▄█▀▒",
	"     ▀   ▀",
}

// tubePingWalkFrames are the scene-sized form of the founder-approved pixel receiver.
// The feet alternate without changing the body or its occupied bounding box.
// Same seven-cell interior and same single axis (column 5 here) as the canonical
// rows above, so the walker and the splash are one mascot rather than two that
// merely resemble each other. Previously the cap was a cell narrower than the body
// on the left, leaving the left wall uncapped, and the eye sat a cell right of the
// wordmark. Only the feet differ between frames; the body box never moves.
var tubePingWalkFrames = [][]string{
	{
		" ▄███████▄",
		" █   •   █▓",
		" █  ROG  █▓",
		"  ▀█▄▄▄█▀▒",
		"   ▀   ▀",
	},
	{
		" ▄███████▄",
		" █   •   █▓",
		" █  ROG  █▓",
		"  ▀█▄▄▄█▀▒",
		"  ▀     ▀",
	},
}

// styleTubePingRow emits a small number of CONTIGUOUS semantic spans. Styling each
// block glyph separately made some terminals show seams and broken geometry even
// though the cell widths were correct.
func styleTubePingRow(row string) string {
	var b strings.Builder
	for len(row) > 0 {
		i := strings.IndexAny(row, "•▓▒()")
		if i < 0 {
			b.WriteString(stKey.Render(row))
			break
		}
		if i > 0 {
			b.WriteString(stKey.Render(row[:i]))
		}
		r := []rune(row[i:])[0]
		switch r {
		case '•':
			b.WriteString(stPingEye.Render("•"))
		case '▓':
			b.WriteString(stPingBody.Render("▓"))
		case '▒':
			b.WriteString(stPingDim.Render("▒"))
		case '(', ')':
			// Carrier waves support the receiver instead of competing with its
			// bright body. Keeping them on the quiet plane also prevents the
			// splash from reading as several disconnected white brackets.
			b.WriteString(stPingDim.Render(string(r)))
		}
		row = row[i+len(string(r)):]
	}
	return b.String()
}

// renderTubePing is the reusable canonical pixel mascot. Each body plane is one
// contiguous terminal span; only the eye and right-side shadow break the span.
func renderTubePing(width, frame int) string {
	return renderTubePingPose(width, frame, tubePingIdle)
}

// renderTubePingPose keeps the canonical glyph data reusable while allowing
// restrained terminal-native life. Poses alter only the eye and matched carrier
// waves; the body, wordmark, depth planes, and occupied box never move.
func renderTubePingPose(width, frame int, pose tubePingPose) string {
	if width < tubePingMinWidth || glyphs.ASCII() {
		return renderPing(pingIdleFrames[(frame/6)%len(pingIdleFrames)], "•")
	}
	rows := append([]string(nil), tubePingRows...)
	eye := "•"
	switch pose {
	case tubePingTransmit:
		eye = "O"
		if frame%2 != 0 {
			rows[1] = strings.Replace(rows[1], "(  ", "(( ", 1)
			rows[1] = strings.Replace(rows[1], "  )", " ))", 1)
		}
	case tubePingBlink:
		if frame%2 == 0 {
			eye = "─"
		}
	}
	rows[1] = strings.Replace(rows[1], "•", "\x00", 1)
	out := make([]string, len(rows))
	for i, row := range rows {
		out[i] = strings.Replace(styleTubePingRow(row), "\x00", stPingEye.Render(eye), 1)
	}
	return strings.Join(out, "\n")
}

// compactTubePingMark is the one-row station bug used by persistent TUI chrome.
// Its occupied width never changes, so replacing the old radio tower cannot shift the
// section badge or increase header height.
func compactTubePingMark() string {
	if glyphs.ASCII() {
		return stPingDim.Render("(( ") + stPingEye.Render("•") + stPingDim.Render(" ))")
	}
	return stKey.Render("▟") + stPingEye.Render("•") + stKey.Render("▙") + stPingBody.Render("▓")
}

// compactTubePingCorner renders the five-row reactive AGENT form. All states keep
// the same bounding box; only the carrier, eye, or dial arm changes.
func compactTubePingCorner(state agentPose, frame int, live bool) []string {
	eye := "•"
	left, right := "(", ")"
	switch state {
	case poseThinking:
		if (frame/cornerCadence)%2 != 0 {
			left, right = "‹", "›"
		}
	case poseStreaming:
		eye = cornerEyeFor(state, frame)
		left, right = "(", ")"
	case poseTool:
		if (frame/cornerCadence)%2 == 0 {
			right = "∩"
		} else {
			left = "∩"
		}
	}
	if !live && state == poseWaiting {
		eye = "•"
	}
	// The roomier corner mark preserves the hero's four readable planes: bright
	// face, ▓ side wall, ▒ lower bevel, and detached feet. Carrier/tool gestures
	// live outside the body, leaving its occupied box stable across every pose.
	rows := append([]string(nil), tubePingRows...)
	rows[1] = strings.Replace(rows[1], "(  ", left+"  ", 1)
	rows[1] = strings.Replace(rows[1], "  )", "  "+right, 1)
	out := make([]string, len(rows))
	for i, row := range rows {
		row = strings.Replace(row, "•", "\x00", 1)
		out[i] = strings.Replace(styleTubePingRow(row), "\x00", stPingEye.Render(eye), 1)
	}
	return out
}

func tubePingWorldSprite(frame int) []string {
	return tubePingWalkFrames[(frame/4)%len(tubePingWalkFrames)]
}

// padBlock right-pads every line of a multi-line block to the widest one.
//
// lipgloss.JoinVertical(lipgloss.Center, …) centres each line INDEPENDENTLY. The
// mascot's rows are deliberately different widths - the eye row carries the carrier
// waves - and they share one axis only through their built-in leading spaces. Centring
// them line by line pads the narrow rows more than the wide ones and shears the body
// apart: cap in one column, eye row in another, wordmark in a third. Equalising the
// widths first makes the centring shift every row by the same amount, so the block
// moves as one object.
func padBlock(block string) string {
	lines := strings.Split(block, "\n")
	width := 0
	for _, l := range lines {
		if n := lipgloss.Width(l); n > width {
			width = n
		}
	}
	for i, l := range lines {
		if pad := width - lipgloss.Width(l); pad > 0 {
			lines[i] = l + strings.Repeat(" ", pad)
		}
	}
	return strings.Join(lines, "\n")
}

// tubePingTitle is the short, fullscreen z-debut. Place handles both horizontal
// and vertical centering; tiny screens inherit classic Ping rather than clipping.
func tubePingTitle(w, h, frame int) string {
	if w <= 0 || h <= 0 {
		return ""
	}
	pose := tubePingIdle
	if !quiet {
		switch {
		case frame == 2:
			pose = tubePingBlink
		case frame >= 5 && frame <= 7:
			pose = tubePingTransmit
		}
	}
	art := padBlock(renderTubePingPose(w, frame, pose))
	lockup := stKey.Bold(true).Render("ROGER·AI") + stDim.Render(" · ") + stKey.Render("ON AIR")
	if w >= 36 {
		lockup = stKey.Bold(true).Render("ROGER·AI") +
			stDim.Render("  ·  TUBE PING  ·  ") +
			stKey.Render("ON AIR")
	}
	rows := []string{art, "", lockup}
	if h >= 9 {
		rows = append(rows, "", stDim.Render("press any key to return"))
	}
	if h < 7 {
		rows = []string{art, lockup}
	}
	if h <= 5 {
		rows = []string{lockup}
	}
	block := lipgloss.JoinVertical(lipgloss.Center, rows...)
	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, block)
}
