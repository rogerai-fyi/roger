package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const approvedTubePing = "" +
	"     ▄██████▄\n" +
	"((  █   •   █▓  ))\n" +
	"    █  ROG  █▓\n" +
	"     ▀█▄▄▄▄█▀▒\n" +
	"      ▀    ▀"

func tubePingWorldPosition(buf [][]worldCell) (int, bool) {
	for _, row := range buf {
		for x := 0; x+2 < len(row); x++ {
			if row[x].r == 'R' && row[x+1].r == 'O' && row[x+2].r == 'G' {
				return x, true
			}
		}
	}
	return 0, false
}

func worldContainsClassicPing(buf [][]worldCell) bool {
	for _, row := range buf {
		for x := 0; x+4 < len(row); x++ {
			if row[x].r == '│' && row[x+1].r == ' ' && row[x+2].r == 'R' &&
				row[x+3].r == ' ' && row[x+4].r == '│' {
				return true
			}
		}
	}
	return false
}

func TestTubePingCanonicalSilhouette(t *testing.T) {
	got := strings.TrimRight(stripANSI(renderTubePing(80, 0)), " \n")
	if got != approvedTubePing {
		t.Fatalf("Tube Ping silhouette drifted:\n%s\nwant:\n%s", got, approvedTubePing)
	}
}

func TestTubePingFallsBackToClassicAtTinyWidth(t *testing.T) {
	got := stripANSI(renderTubePing(18, 0))
	if strings.ContainsAny(got, "█▓▒") {
		t.Fatalf("tiny Tube Ping clipped instead of using classic Ping:\n%s", got)
	}
	if !strings.Contains(got, "•") || !strings.Contains(got, "R") {
		t.Fatalf("classic Ping fallback is not recognizable:\n%s", got)
	}
}

func TestEnteringPingWorldDebutsTubePingThenClassicWorld(t *testing.T) {
	m := pwModel(modeBrowse)
	m.width, m.height = 80, 24
	out, _ := m.enterPingWorld()
	m = asModel(out)

	first := stripANSI(m.View())
	if !strings.Contains(first, "ROG") || !strings.Contains(first, "TUBE PING") {
		t.Fatalf("z debut did not show Tube Ping:\n%s", first)
	}
	if strings.Contains(first, ".fyi") {
		t.Fatal("debut rendered the ordinary world underneath the title card")
	}

	for i := 0; i < tubePingDebutFrames; i++ {
		out, _ = m.update(tickMsg{gen: m.tickGen})
		m = asModel(out)
	}
	after := stripANSI(m.View())
	if strings.Contains(after, "TUBE PING") || !strings.Contains(after, "R O G E R") {
		t.Fatalf("Tube Ping debut did not transition into classic Ping World:\n%s", after)
	}
}

func TestAnyKeyWakesDuringTubePingDebut(t *testing.T) {
	m := pwModel(modeChat)
	m.width, m.height = 80, 24
	out, _ := m.enterPingWorld()
	m = asModel(out)
	out, _ = m.onKey(tea.KeyMsg{Type: tea.KeyEsc})
	m = asModel(out)
	if m.mode != modeChat {
		t.Fatalf("wake during debut returned to %v, want chat", m.mode)
	}
}

func TestTubePingTitleResponsiveFallback(t *testing.T) {
	for _, tc := range []struct{ w, h int }{
		{18, 5}, {24, 7}, {32, 9}, {35, 10}, {40, 12},
		{80, 24}, {120, 32}, {190, 50},
	} {
		got := tubePingTitle(tc.w, tc.h, 0)
		lines := strings.Split(got, "\n")
		if len(lines) > tc.h {
			t.Fatalf("%dx%d title has %d rows", tc.w, tc.h, len(lines))
		}
		for _, line := range lines {
			if lipgloss.Width(line) > tc.w {
				t.Fatalf("%dx%d title overflow: %q", tc.w, tc.h, stripANSI(line))
			}
		}
	}
}

func TestTubePingWalksInsideWorldWithoutReplacingClassicPing(t *testing.T) {
	first := worldBuffer(120, 32, 0, 7)
	later := worldBuffer(120, 32, 48, 7)
	x0, ok0 := tubePingWorldPosition(first)
	x1, ok1 := tubePingWorldPosition(later)
	if !ok0 || !ok1 {
		t.Fatal("supported Ping World frames must contain the Tube Ping walker")
	}
	if x0 == x1 {
		t.Fatalf("Tube Ping walker did not move: x=%d at both frames", x0)
	}
	if !worldContainsClassicPing(first) || !worldContainsClassicPing(later) {
		t.Fatal("Tube Ping walker replaced classic Ping")
	}
}

func TestTubePingWorldWalkerOmittedWhenItCannotFit(t *testing.T) {
	buf := worldBuffer(40, 12, 0, 7)
	if _, ok := tubePingWorldPosition(buf); ok {
		t.Fatal("small world contains a clipped Tube Ping walker")
	}
	if !worldContainsClassicPing(buf) {
		t.Fatal("small world lost classic Ping fallback")
	}
}

func TestHeaderUsesCompactTubePingStationBug(t *testing.T) {
	m := browseSeed(120)
	got := stripANSI(m.header(120))
	if !strings.Contains(got, "▟•▙▓") {
		t.Fatalf("main header does not use compact Tube Ping station bug:\n%s", got)
	}
}

func TestAgentCornerUsesCompactTubePingFamily(t *testing.T) {
	for _, state := range []agentPose{poseWaiting, poseThinking, poseStreaming, poseTool} {
		lines := agentCornerPing(state, 24, false, false, true)
		if len(lines) != 3 {
			t.Fatalf("state %d returned %d rows, want stable 3-row mark", state, len(lines))
		}
		plain := stripANSI(strings.Join(lines, "\n"))
		if !strings.Contains(plain, "ROG") || !strings.Contains(plain, "▓") {
			t.Fatalf("state %d is not a compact Tube Ping:\n%s", state, plain)
		}
	}
}

// cornerBodySpan reports the first and last column occupied by the receiver ITSELF
// on one corner row - excluding the carrier waves, the tool arms, and the depth
// plane, which are all decoration hung off the body rather than part of it.
func cornerBodySpan(row string) (first, last int) {
	const decoration = " ()‹›∩▓▒"
	first, last = -1, -1
	for c, ch := range []rune(stripANSI(row)) {
		if strings.ContainsRune(decoration, ch) {
			continue
		}
		if first < 0 {
			first = c
		}
		last = c
	}
	return first, last
}

// The 3-row AGENT corner has to read as ONE object. Every row shares a left and a
// right edge column, and the depth plane (▓ on the lit rows, ▒ on the base) sits in
// a single column all the way down. The first pass floated a 3-cell head over a
// 5-cell body and stepped the shadow between columns 5 and 6, which on a real
// terminal read as a small head sitting on a detached white slab.
func TestAgentCornerSharesOneBoundingBoxAndOneShadowColumn(t *testing.T) {
	for _, state := range []agentPose{poseWaiting, poseThinking, poseStreaming, poseTool} {
		rows := compactTubePingCorner(state, 0, true)
		if len(rows) != 3 {
			t.Fatalf("state %d: %d rows, want 3", state, len(rows))
		}
		dump := stripANSI(strings.Join(rows, "\n"))

		wantFirst, wantLast := cornerBodySpan(rows[0])
		for i, row := range rows {
			first, last := cornerBodySpan(row)
			if first != wantFirst || last != wantLast {
				t.Errorf("state %d row %d spans columns %d-%d, want %d-%d (one bounding box):\n%s",
					state, i, first, last, wantFirst, wantLast, dump)
			}
		}

		shadowCol := -1
		for i, row := range rows {
			col := -1
			for c, ch := range []rune(stripANSI(row)) {
				if ch != '▓' && ch != '▒' {
					continue
				}
				if col >= 0 {
					t.Fatalf("state %d row %d has more than one depth glyph:\n%s", state, i, dump)
				}
				col = c
			}
			if col < 0 {
				t.Errorf("state %d row %d has no depth plane:\n%s", state, i, dump)
				continue
			}
			if shadowCol < 0 {
				shadowCol = col
				continue
			}
			if col != shadowCol {
				t.Errorf("state %d row %d puts the depth plane in column %d, want %d (one plane, one column):\n%s",
					state, i, col, shadowCol, dump)
			}
		}
		if shadowCol >= 0 && shadowCol != wantLast+1 {
			t.Errorf("state %d: depth plane sits in column %d, want %d - immediately right of the body:\n%s",
				state, shadowCol, wantLast+1, dump)
		}
	}
}

func TestTubePingUsesContiguousStyleSpans(t *testing.T) {
	got := renderTubePing(80, 0)
	// The original broken pass wrapped nearly every block glyph in its own ANSI
	// reset. A contiguous body plane needs only a small, bounded number of spans.
	if n := strings.Count(got, "\x1b[0m"); n > 16 {
		t.Fatalf("Tube Ping is fragmented into %d ANSI spans:\n%q", n, got)
	}
}

func TestTubePingHeroAnimationPreservesIdentity(t *testing.T) {
	base := stripANSI(renderTubePingPose(80, 0, tubePingIdle))
	breathe := stripANSI(renderTubePingPose(80, 1, tubePingIdle))
	if base != approvedTubePing || breathe != approvedTubePing {
		t.Fatalf("idle light changed the canonical silhouette:\nbase:\n%s\nbreathe:\n%s", base, breathe)
	}

	tx0 := stripANSI(renderTubePingPose(80, 0, tubePingTransmit))
	tx1 := stripANSI(renderTubePingPose(80, 1, tubePingTransmit))
	for _, got := range []string{tx0, tx1} {
		if strings.Count(got, "O") != 2 { // widened eye plus the O in ROG
			t.Fatalf("transmit pose must have one widened eye and the unchanged wordmark:\n%s", got)
		}
		if strings.Count(got, "((") != strings.Count(got, "))") {
			t.Fatalf("transmit waves are asymmetric:\n%s", got)
		}
	}

	blink := stripANSI(renderTubePingPose(80, 0, tubePingBlink))
	if strings.Contains(blink, "•") || !strings.Contains(blink, "─") {
		t.Fatalf("blink must briefly close the eye:\n%s", blink)
	}
	if returned := stripANSI(renderTubePingPose(80, 1, tubePingBlink)); returned != approvedTubePing {
		t.Fatalf("blink did not return to the canonical live eye:\n%s", returned)
	}
}

func TestTubePingTitleUsesCompactIdentityLockup(t *testing.T) {
	got := stripANSI(tubePingTitle(50, 30, 0))
	if !strings.Contains(got, "ROGER·AI  ·  TUBE PING  ·  ON AIR") {
		t.Fatalf("splash identity is fragmented:\n%s", got)
	}
	if strings.Contains(got, "R O G E R") {
		t.Fatalf("splash still uses the detached letterspaced wordmark:\n%s", got)
	}
	lines := strings.Split(got, "\n")
	rogRow, lockupRow := -1, -1
	for i, line := range lines {
		if strings.Contains(line, "█  ROG  █") {
			rogRow = i
		}
		if strings.Contains(line, "ROGER·AI  ·  TUBE PING") {
			lockupRow = i
		}
	}
	if rogRow < 0 || lockupRow-rogRow > 4 {
		t.Fatalf("identity lockup is detached from mascot: mascot=%d lockup=%d", rogRow, lockupRow)
	}
}
