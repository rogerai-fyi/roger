package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/rogerai-fyi/roger/internal/glyphs"
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

var tubePingRows = []string{
	"     ▄██████▄",
	"((  █   •   █▓  ))",
	"    █  ROG  █▓",
	"     ▀█▄▄▄▄█▀▒",
	"      ▀    ▀",
}

// tubePingWalkFrames are the scene-sized form of the founder-approved pixel receiver.
// The feet alternate without changing the body or its occupied bounding box.
var tubePingWalkFrames = [][]string{
	{
		"  ▄██████▄",
		" █   •   █▓",
		" █  ROG  █▓",
		"  ▀█▄▄▄▄█▀▒",
		"   ▀    ▀",
	},
	{
		"  ▄██████▄",
		" █   •   █▓",
		" █  ROG  █▓",
		"  ▀█▄▄▄▄█▀▒",
		"  ▀      ▀",
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
			rows[1] = strings.Replace(rows[1], "((  ", "((( ", 1)
			rows[1] = strings.Replace(rows[1], "  ))", " )))", 1)
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

// compactTubePingCorner renders the three-row reactive AGENT form. All states keep
// the same bounding box; only the carrier, eye, or dial arm changes.
func compactTubePingCorner(state agentPose, frame int, live bool) []string {
	eye := "•"
	left, right := "(", ")"
	armL, armR := " ", " "
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
			armR = "∩"
		} else {
			armL = "∩"
		}
	}
	if !live && state == poseWaiting {
		eye = "•"
	}
	// One object, one bounding box, one depth plane. The capped head, the wordmark
	// body, and the bevelled base all span columns 2-6, and the ▓/▒ shadow sits in
	// column 7 on every row. Carrier waves and tool arms hang OUTSIDE that box, so a
	// pose can never change the silhouette. The first pass floated a 3-cell head
	// (▟•▙) over a 5-cell body and stepped the shadow between columns 5 and 6, which
	// on a real terminal read as a small head perched on a detached white slab.
	//
	// The wordmark stays on the quiet plane. At five cells there is no room for the
	// canonical `█  ROG  █` breathing space, so separating ROG from the walls by
	// COLOUR is what stops the row collapsing into one bright block - and it keeps
	// the eye, not the lettering, as the brightest thing on the mark.
	top := stPingDim.Render(left+" ") + stKey.Render("▄█") + stPingEye.Render(eye) +
		stKey.Render("█▄") + stPingBody.Render("▓") + stPingDim.Render(" "+right)
	mid := stPingDim.Render(" ") + stPingDim.Render(armL) + stKey.Render("█") +
		stPingBody.Render("ROG") + stKey.Render("█") + stPingBody.Render("▓") + stPingDim.Render(armR)
	base := stPingDim.Render("  ") + stKey.Render("▀█▄█▀") + stPingDim.Render("▒")
	return []string{top, mid, base}
}

func tubePingWorldSprite(frame int) []string {
	return tubePingWalkFrames[(frame/4)%len(tubePingWalkFrames)]
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
	art := renderTubePingPose(w, frame, pose)
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
