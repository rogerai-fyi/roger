package tui

import (
	"strings"
	"testing"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
)

// TestStationRowIconography: the band table uses the SHARED instrument glyphs
// (◉ on-air, ▁..█ signal tower, ◆ verified) the share table + channel header
// also use, so every surface reads as one designed system. The signal bar must
// render in the row, and a lineage band must carry the ◆ glyph.
func TestStationRowIconography(t *testing.T) {
	var m tea.Model = New("http://broker.local", "tester")
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m, _ = m.Update(offersMsg{
		{NodeID: "demo", Region: "home", Model: "gpt-oss-20b", PriceIn: 0.2, PriceOut: 0.3, Online: true, TPS: 62, Confidential: true},
		{NodeID: "alt", Region: "us-w", Model: "gpt-oss-20b", PriceIn: 0.25, PriceOut: 0.41, Online: true, TPS: 40},
	})
	m, _ = m.Update(balanceMsg{balance: 100, loggedIn: true})
	m, _ = m.Update(tickMsg{})
	out := stripANSI(m.View())
	// a signal tower glyph is present in the row
	if !strings.ContainsAny(out, "▁▂▃▄▅▆▇█") {
		t.Errorf("band row missing the ▁..█ signal tower:\n%s", out)
	}
	// the verified ◆ lineage glyph for the confidential band
	if !strings.Contains(out, glyphVerify) {
		t.Errorf("lineage band missing the ◆ verified glyph:\n%s", out)
	}
	// the SIGNAL column header is present (the inline meter column)
	if !strings.Contains(out, "signal") {
		t.Errorf("band table missing the signal column:\n%s", out)
	}
}

// TestStagedConnectSequence: accepting a connect runs the staged tune-in. Under
// quiet (tests are non-TTY) it resolves to the channel; the resolved sequence
// must surface the CHANNEL-OPEN markers and the clean BASE URL / API KEY / MODEL
// endpoint block (the web-parity finale).
func TestStagedConnectSequence(t *testing.T) {
	// Drive the staged steps directly (quiet skips the animation), so we can assert
	// the intermediate sequence renders without panicking and shows the steps.
	mm := New("http://broker.local", "tester")
	mm.width, mm.height = 100, 30
	mm.connected = &offer{NodeID: "nyx-home", Model: "gpt-oss-20b", PriceOut: 0.30, Online: true, TPS: 62, Confidential: true}
	mm.endpoint = "http://127.0.0.1:4141/v1"
	mm.apikey = "roger-local"
	mm.mode = modeConnecting
	for stage := 0; stage <= connectStageDone; stage++ {
		mm.connectStage = stage
		out := stripANSI(mm.connectingView(100))
		if strings.Contains(mm.connectingView(100), "\x1b[") && stage == connectStageDone {
			t.Errorf("connecting view emitted ANSI under quiet")
		}
		if !strings.Contains(out, "scanning stations") || !strings.Contains(out, "locking strongest") || !strings.Contains(out, "lineage handshake") {
			t.Errorf("stage %d missing the staged steps:\n%s", stage, out)
		}
	}
	// The fully-resolved finale: CHANNEL OPEN + the endpoint block + roger that.
	mm.connectStage = connectStageDone
	out := stripANSI(mm.connectingView(100))
	for _, want := range []string{"CHANNEL OPEN", "verified", "BASE URL", "API KEY", "MODEL", "http://127.0.0.1:4141/v1", "roger-local", "gpt-oss-20b", "roger that."} {
		if !strings.Contains(out, want) {
			t.Errorf("connect finale missing %q:\n%s", want, out)
		}
	}
}

// TestEndpointBlockAligned: the shared BASE URL / API KEY / MODEL plate lines its
// values up under one gutter (mono labels, mono values) and carries the values.
func TestEndpointBlockAligned(t *testing.T) {
	mm := New("http://broker.local", "tester")
	mm.width, mm.height = 100, 30
	mm.connected = &offer{NodeID: "nyx", Model: "qwen3-coder-30b", Online: true}
	mm.endpoint = "http://127.0.0.1:4141/v1"
	mm.apikey = "roger-local"
	block := stripANSI(mm.endpointBlock(100))
	lines := strings.Split(strings.TrimRight(block, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("endpoint block should be 3 rows, got %d:\n%s", len(lines), block)
	}
	// The value column starts at the same offset on every row (aligned gutter).
	off := func(line, val string) int { return strings.Index(line, val) }
	if a, b, c := off(lines[0], "http://"), off(lines[1], "roger-local"), off(lines[2], "qwen3-coder-30b"); a != b || b != c {
		t.Errorf("endpoint block values not aligned: %d/%d/%d\n%s", a, b, c, block)
	}
}

// TestConnectingNoColorNarrowSafe: the staged sequence never overflows narrow
// widths and emits no ANSI under NO_COLOR, at every stage.
func TestConnectingNoColorNarrowSafe(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	for _, w := range []int{40, 50, 64, 80, 120} {
		mm := New("http://broker.local", "tester")
		mm.width, mm.height = w, 24
		mm.connected = &offer{NodeID: "nyx-home", Model: "gpt-oss-20b", PriceOut: 0.30, Online: true, TPS: 62}
		mm.endpoint = "http://127.0.0.1:4141/v1"
		mm.apikey = "roger-local"
		mm.mode = modeConnecting
		for stage := 0; stage <= connectStageDone; stage++ {
			mm.connectStage = stage
			var m tea.Model = mm
			out := m.View()
			if strings.Contains(out, "\x1b[") {
				t.Errorf("width %d stage %d emitted ANSI under NO_COLOR", w, stage)
			}
			for _, line := range strings.Split(out, "\n") {
				if vis := utf8.RuneCountInString(stripANSI(line)); vis > w {
					t.Errorf("width %d stage %d line overflows (%d cols): %q", w, stage, vis, stripANSI(line))
				}
			}
		}
	}
}

// TestSignalGrading: a peaking signal tower glints red (the one accent at the
// peak), while a quiet one stays mono; under NO_COLOR neither emits color.
func TestSignalGrading(t *testing.T) {
	// A high-tps online tower has a peaking (▇/█) cell -> the tinted form differs
	// from the all-dim offline form (color present when not quiet is hard to assert
	// portably, so we assert the raw glyph levels reach the peak band).
	raw := signalBarsRaw(1, 700, true, 4) // fast + 4 stations -> tall tower
	peak := false
	for _, r := range raw {
		if r == '▇' || r == '█' {
			peak = true
		}
	}
	if !peak {
		t.Errorf("a fast, busy band should peak the signal tower, got %q", raw)
	}
	// More stations lift the floor: a 4-station band reads taller than a 1-station
	// one at the same tps.
	one := signalBarsRaw(1, 60, true, 1)
	many := signalBarsRaw(1, 60, true, 4)
	if one == many {
		t.Errorf("station count should lift the signal tower: %q == %q", one, many)
	}
}
