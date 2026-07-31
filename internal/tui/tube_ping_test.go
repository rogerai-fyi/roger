package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const approvedTubePing = "" +
	"   ▄███████▄\n" +
	"(  █   •   █▓  )\n" +
	"   █  ROG  █▓\n" +
	"    ▀█▄▄▄█▀▒\n" +
	"     ▀   ▀"

var approvedCompactTubePing = []string{
	"   ▄███████▄",
	"(  █   •   █▓  )",
	"   █  ROG  █▓",
	"    ▀█▄▄▄█▀▒",
	"     ▀   ▀",
}

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

// The face has to sit on ONE vertical axis. The wordmark is three cells wide, so
// it can only be padded evenly inside a body whose interior is an ODD width - at
// seven cells ROG gets 2/2, at six it can only ever get 2/1 and the line reads
// visibly shoved against the right wall. The v5.4.8 compaction narrowed the
// interior to six, which is what pushed both the wordmark and the eye off centre;
// the walk sprite in the same release kept seven and stayed balanced.
func TestTubePingFaceIsCentredOnOneAxis(t *testing.T) {
	rows := strings.Split(stripANSI(renderTubePing(80, 0)), "\n")

	var eyeRow, wordRow string
	for _, r := range rows {
		if strings.Contains(r, "•") {
			eyeRow = r
		}
		if strings.Contains(r, "ROG") {
			wordRow = r
		}
	}
	if eyeRow == "" || wordRow == "" {
		t.Fatalf("Tube Ping is missing its eye or wordmark:\n%s", strings.Join(rows, "\n"))
	}

	walls := []int{}
	for c, ch := range []rune(eyeRow) {
		if ch == '█' {
			walls = append(walls, c)
		}
	}
	if len(walls) < 2 {
		t.Fatalf("eye row has no body walls: %q", eyeRow)
	}
	left, right := walls[0], walls[len(walls)-1]
	if interior := right - left - 1; interior%2 == 0 {
		t.Errorf("body interior is %d cells (even) - a 3-cell wordmark can never be padded evenly:\n%s",
			interior, strings.Join(rows, "\n"))
	}

	wr := []rune(wordRow)
	rIdx, gIdx := -1, -1
	for c := 0; c+2 < len(wr); c++ {
		if wr[c] == 'R' && wr[c+1] == 'O' && wr[c+2] == 'G' {
			rIdx, gIdx = c, c+2
			break
		}
	}
	if rIdx < 0 {
		t.Fatalf("wordmark row has no ROG: %q", wordRow)
	}
	if padL, padR := rIdx-(left+1), (right-1)-gIdx; padL != padR {
		t.Errorf("ROG padding is L=%d R=%d, want symmetric:\n%s", padL, padR, strings.Join(rows, "\n"))
	}

	// Rune index, not strings.Index - the row is full of multi-byte block glyphs,
	// so a byte offset would be compared against rune columns and lie.
	eye := -1
	for c, ch := range []rune(eyeRow) {
		if ch == '•' {
			eye = c
			break
		}
	}
	if want := (rIdx + gIdx) / 2; eye != want {
		t.Errorf("eye sits in column %d, want %d - directly above the O of ROG:\n%s",
			eye, want, strings.Join(rows, "\n"))
	}
}

// Correct glyph rows are not enough: the SPLASH has to render them on one axis.
// The rows are deliberately different widths (the eye row carries the carrier
// waves) and rely on their built-in leading spaces to line up when the block is
// left-aligned. lipgloss.JoinVertical(Center, …) centres every line independently,
// which pads narrow rows more than wide ones and shears the mascot apart - the cap
// lands in one column, the eye row in another, the wordmark in a third.
func TestTubePingTitleRendersTheMascotOnOneAxis(t *testing.T) {
	for _, size := range []struct{ w, h int }{{46, 14}, {80, 24}, {120, 32}} {
		lines := strings.Split(stripANSI(tubePingTitle(size.w, size.h, 0)), "\n")

		wallCol := func(needle string) int {
			for _, l := range lines {
				if !strings.Contains(l, needle) {
					continue
				}
				for c, ch := range []rune(l) {
					if ch == '█' {
						return c
					}
				}
			}
			return -1
		}
		eyeWall, wordWall := wallCol("•"), wallCol("ROG")
		if eyeWall < 0 || wordWall < 0 {
			t.Fatalf("%dx%d: mascot rows missing:\n%s", size.w, size.h, strings.Join(lines, "\n"))
		}
		if eyeWall != wordWall {
			t.Errorf("%dx%d: body walls sheared - eye row wall at %d, wordmark wall at %d:\n%s",
				size.w, size.h, eyeWall, wordWall, strings.Join(lines, "\n"))
		}
	}
}

func TestTubePingCanonicalSilhouette(t *testing.T) {
	got := strings.TrimRight(stripANSI(renderTubePing(80, 0)), " \n")
	if got != approvedTubePing {
		t.Fatalf("Tube Ping silhouette drifted:\n%s\nwant:\n%s", got, approvedTubePing)
	}
}

// Was TestTubePingEyeUsesOpticallyCorrectedColumn, which required the eye to sit
// ONE cell right of the ROG centre. That "optical correction" was not a design
// choice - it was the six-cell interior of the v5.4.8 compaction showing through:
// at that width the wordmark could not centre, so the eye was moved to match the
// off-centre wordmark instead of the wordmark being fixed. The pre-5.4.8 mascot
// had eye and O in the SAME column, and the restored seven-cell interior does too.
func TestTubePingEyeSharesTheWordmarkColumn(t *testing.T) {
	for _, tc := range []struct {
		name   string
		pose   tubePingPose
		marker string
	}{
		{name: "idle", pose: tubePingIdle, marker: "•"},
		{name: "transmit", pose: tubePingTransmit, marker: "O"},
		{name: "blink", pose: tubePingBlink, marker: "─"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := strings.Split(stripANSI(renderTubePingPose(80, 0, tc.pose)), "\n")
			if len(got) < 3 {
				t.Fatalf("Tube Ping has %d rows, want at least 3", len(got))
			}
			wordmarkRow := ""
			for _, line := range got {
				if strings.Contains(line, "ROG") {
					wordmarkRow = line
					break
				}
			}
			if wordmarkRow == "" {
				t.Fatalf("Tube Ping has no ROG wordmark:\n%s", strings.Join(got, "\n"))
			}
			eye := terminalCellBefore(t, got[1], tc.marker)
			wordmarkCenter := terminalCellBefore(t, wordmarkRow, "O")
			if eye != wordmarkCenter {
				t.Fatalf("%s eye column=%d, want %d - the same column as the O of ROG:\n%s",
					tc.name, eye, wordmarkCenter, strings.Join(got, "\n"))
			}
			for row, line := range got {
				if width, want := lipgloss.Width(line), lipgloss.Width(tubePingRows[row]); width != want {
					t.Fatalf("%s row %d width=%d, want stable width=%d", tc.name, row, width, want)
				}
			}
		})
	}
}

// The walker shares the canonical face rule: eye and wordmark on one column. It
// previously carried the same off-by-one as the splash, so the two forms drifted.
func TestTubePingWalkerSharesTheWordmarkColumnAndStableBounds(t *testing.T) {
	baselineWidth := 0
	for frame, rows := range tubePingWalkFrames {
		eye := terminalCellBefore(t, rows[1], "•")
		wordmarkCenter := terminalCellBefore(t, rows[2], "O")
		if eye != wordmarkCenter {
			t.Fatalf("walker frame %d eye column=%d, want %d - the same column as the O of ROG:\n%s",
				frame, eye, wordmarkCenter, strings.Join(rows, "\n"))
		}
		maxWidth := 0
		for row, line := range rows {
			width := lipgloss.Width(line)
			if width > tubePingWalkW {
				t.Fatalf("walker frame %d row %d width=%d exceeds bound %d", frame, row, width, tubePingWalkW)
			}
			if width > maxWidth {
				maxWidth = width
			}
		}
		if frame == 0 {
			baselineWidth = maxWidth
		} else if maxWidth != baselineWidth {
			t.Fatalf("walker frame %d occupied width=%d, want stable width=%d", frame, maxWidth, baselineWidth)
		}
	}
}

func terminalCellBefore(t *testing.T, line, marker string) int {
	t.Helper()
	index := strings.Index(line, marker)
	if index < 0 {
		t.Fatalf("marker %q missing from %q", marker, line)
	}
	return lipgloss.Width(line[:index])
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
		if len(lines) != len(approvedCompactTubePing) {
			t.Fatalf("state %d returned %d rows, want stable %d-row mark", state, len(lines), len(approvedCompactTubePing))
		}
		plain := stripANSI(strings.Join(lines, "\n"))
		if !strings.Contains(plain, "ROG") || !strings.Contains(plain, "▓") {
			t.Fatalf("state %d is not a compact Tube Ping:\n%s", state, plain)
		}
		if state == poseWaiting {
			corner := stripANSI(strings.Join(compactTubePingCorner(state, 0, true), "\n"))
			if corner == strings.Join(approvedCompactTubePing, "\n") {
				continue
			}
			t.Fatalf("waiting Tube Ping silhouette drifted:\n%s\nwant:\n%s",
				corner, strings.Join(approvedCompactTubePing, "\n"))
		}
	}
}

// The AGENT corner has to read as one stable object. Each state retains the same
// occupied row widths; the side plane stays vertical and the bevel steps inward.
func TestAgentCornerSharesOneBoundingBoxAndOneShadowColumn(t *testing.T) {
	for _, state := range []agentPose{poseWaiting, poseThinking, poseStreaming, poseTool} {
		rows := compactTubePingCorner(state, 0, true)
		if len(rows) != len(approvedCompactTubePing) {
			t.Fatalf("state %d: %d rows, want %d", state, len(rows), len(approvedCompactTubePing))
		}
		dump := stripANSI(strings.Join(rows, "\n"))
		for i, row := range rows {
			if got, want := lipgloss.Width(stripANSI(row)), lipgloss.Width(approvedCompactTubePing[i]); got != want {
				t.Errorf("state %d row %d width=%d, want stable width=%d:\n%s",
					state, i, got, want, dump)
			}
		}
		faceDepth := terminalCellBefore(t, stripANSI(rows[1]), "▓")
		wordmarkDepth := terminalCellBefore(t, stripANSI(rows[2]), "▓")
		bevelDepth := terminalCellBefore(t, stripANSI(rows[3]), "▒")
		if faceDepth != wordmarkDepth || bevelDepth != faceDepth-1 {
			t.Errorf("state %d has incoherent 3D planes: face=%d wordmark=%d bevel=%d:\n%s",
				state, faceDepth, wordmarkDepth, bevelDepth, dump)
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
	}
	if !strings.Contains(tx0, "(  █") || !strings.Contains(tx0, "█▓  )") {
		t.Fatalf("transmit rest frame lost the canonical matched carriers:\n%s", tx0)
	}
	if !strings.Contains(tx1, "(( █") || !strings.Contains(tx1, "█▓ ))") {
		t.Fatalf("transmit live frame did not grow both carriers:\n%s", tx1)
	}
	if tx0 == tx1 {
		t.Fatal("transmit carrier animation is motionless")
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
