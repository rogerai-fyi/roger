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
	// The fully-resolved finale: CHANNEL OPEN + the endpoint block + roger that. The
	// offer is confidential here, so the finale carries the TEE "confidential" mark
	// (◆) - NOT a generic "verified" word.
	mm.connectStage = connectStageDone
	out := stripANSI(mm.connectingView(100))
	for _, want := range []string{"CHANNEL OPEN", "confidential", "BASE URL", "API KEY", "MODEL", "http://127.0.0.1:4141/v1", "roger-local", "gpt-oss-20b", "roger that."} {
		if !strings.Contains(out, want) {
			t.Errorf("connect finale missing %q:\n%s", want, out)
		}
	}

	// A NON-confidential channel must NOT claim "confidential": it carries the lineage
	// mark instead, proving the ◆ badge is reserved for the real TEE tier.
	mm.connected = &offer{NodeID: "plain-home", Model: "gpt-oss-20b", PriceOut: 0.30, Online: true, TPS: 62, Confidential: false}
	out2 := stripANSI(mm.connectingView(100))
	if strings.Contains(out2, "confidential") {
		t.Errorf("a standard (non-TEE) channel must not show 'confidential':\n%s", out2)
	}
	if !strings.Contains(out2, "lineage") {
		t.Errorf("a standard channel should carry the lineage mark:\n%s", out2)
	}
}

// TestChannelGlyphHonesty: the confidential ◆ is shown ONLY for a TEE-attested
// (Confidential) channel; a standard channel carries the lineage ✓ instead. This is
// the load-bearing badge disambiguation - ◆ must never appear for a non-TEE node.
func TestChannelGlyphHonesty(t *testing.T) {
	conf := &offer{NodeID: "tee", Confidential: true}
	std := &offer{NodeID: "plain", Confidential: false}
	if g := channelGlyph(conf); g != glyphConf {
		t.Errorf("confidential channel glyph = %q, want %q (◆)", g, glyphConf)
	}
	if g := channelGlyph(std); g != glyphLineage {
		t.Errorf("standard channel glyph = %q, want %q (✓)", g, glyphLineage)
	}
	if g := channelGlyph(nil); g != glyphLineage {
		t.Errorf("nil channel glyph = %q, want %q (✓)", g, glyphLineage)
	}
	if glyphConf == glyphLineage {
		t.Fatal("confidential and lineage marks must be DISTINCT glyphs")
	}
	// The endpoint panel surfaces the confidential mark only for a confidential channel.
	m := New("http://broker.local", "tester")
	m.width = 100
	m.connected = std
	if strings.Contains(stripANSI(m.endpointPanel(100)), "confidential") {
		t.Error("endpoint panel claimed 'confidential' for a standard channel")
	}
	m.connected = conf
	if !strings.Contains(stripANSI(m.endpointPanel(100)), "confidential") {
		t.Error("endpoint panel missing 'confidential' for a TEE channel")
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

// ---- connect / disconnect / reconnect flow (founder bug fixes) ----

// freeBand is the canonical free offer used by the flow tests so a connect needs
// no login / wallet (anonymous can tune free bands).
func freeBand(mdl string) offer {
	return offer{NodeID: "demo", Region: "home", Model: mdl, PriceIn: 0, PriceOut: 0, Online: true, TPS: 62, FreeNow: true}
}

// connectFree drives the model through connect -> openChannel into the open channel
// without binding a real TCP socket (proxyUp is pre-set), returning the connected
// model. It is the shared setup for the flow tests.
func connectFree(t *testing.T, mm model, mdl string) model {
	t.Helper()
	// pre-bind so openChannel skips the real net.Listen.
	mm.proxyUp = true
	mm.endpoint = "http://127.0.0.1:4141/v1"
	// put the cursor on the wanted band.
	for i, b := range mm.bands {
		if b.model == mdl {
			mm.cursor = i
		}
	}
	nm, _ := mm.connect()
	mm = nm.(model)
	if mm.mode != modeConnectConfirm {
		t.Fatalf("connect did not open the confirm (mode=%d)", mm.mode)
	}
	nm, _ = mm.openChannel()
	mm = nm.(model)
	return mm
}

// TestBandSurvivesDisconnect (#1): a band you connect to then disconnect from must
// STAY in the browse list as a selectable station - even after its node ages out of
// /discover and after a manual re-scan (r). Regression for the vanishing-band bug.
func TestBandSurvivesDisconnect(t *testing.T) {
	var tm tea.Model = New("http://broker.local", "tester")
	tm, _ = tm.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	tm, _ = tm.Update(offersMsg{freeBand("gpt-oss-20b")})
	mm := connectFree(t, tm.(model), "gpt-oss-20b")
	if mm.connected == nil {
		t.Fatalf("openChannel did not connect")
	}
	// disconnect (esc from the channel / d from the list both call disconnect()).
	nm, _ := mm.disconnect()
	mm = nm.(model)
	if mm.connected != nil {
		t.Fatalf("disconnect left a live channel")
	}
	// Now simulate the node aging out of /discover (empty scan, as a periodic re-scan
	// would deliver) - the OLD bug dropped the band here and r could not bring it back.
	nm, _ = mm.Update(offersMsg{})
	mm = nm.(model)
	found := false
	for _, b := range mm.bands {
		if b.model == "gpt-oss-20b" {
			found = true
		}
	}
	if !found {
		t.Fatalf("band vanished from the list after disconnect + node age-out:\n%s", stripANSI(mm.View()))
	}
	// It is still rendered + selectable in the browse view.
	out := stripANSI(mm.View())
	if !strings.Contains(out, "gpt-oss-20b") {
		t.Errorf("disconnected band not shown in browse list:\n%s", out)
	}
	// And r (re-scan) still keeps it even when /discover stays empty.
	var km tea.Model = mm
	km, _ = km.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	mm = km.(model)
	mm2, _ := mm.Update(offersMsg{})
	mm = mm2.(model)
	stillFound := false
	for _, b := range mm.bands {
		if b.model == "gpt-oss-20b" {
			stillFound = true
		}
	}
	if !stillFound {
		t.Errorf("band vanished after r re-scan:\n%s", stripANSI(mm.View()))
	}
}

// TestListDisconnectToggle (#2): the connected band is MARKED in the browse list
// (lit "connected" row) and a from-the-list d disconnects it; Enter on it re-opens
// the channel. So the user can see + toggle the connection from the list.
func TestListDisconnectToggle(t *testing.T) {
	var tm tea.Model = New("http://broker.local", "tester")
	tm, _ = tm.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	tm, _ = tm.Update(offersMsg{freeBand("gpt-oss-20b")})
	mm := connectFree(t, tm.(model), "gpt-oss-20b")
	// jump to browse (tab peeks at the band without disconnecting).
	var km tea.Model = mm
	km, _ = km.Update(tea.KeyMsg{Type: tea.KeyTab})
	mm = km.(model)
	if mm.mode != modeBrowse {
		t.Fatalf("tab did not return to browse (mode=%d)", mm.mode)
	}
	// The list marks the connected row.
	out := stripANSI(mm.View())
	if !strings.Contains(out, "connected") {
		t.Errorf("connected band not marked in the list:\n%s", out)
	}
	if !strings.Contains(out, "d disconnect") {
		t.Errorf("footer missing the d disconnect hint:\n%s", out)
	}
	// d disconnects from the list.
	km = mm
	km, _ = km.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	mm = km.(model)
	if mm.connected != nil {
		t.Fatalf("d in the list did not disconnect")
	}
	if mm.mode != modeBrowse {
		t.Errorf("d left an odd mode=%d (want browse)", mm.mode)
	}
	// Enter on the (still-present, now-warm) band re-connects fast (see #3 test).
	for i, b := range mm.bands {
		if b.model == "gpt-oss-20b" {
			mm.cursor = i
		}
	}
	km = mm
	km, _ = km.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm = km.(model)
	// warm reconnect to a free band drops into the channel (the confirm still gates a
	// cold connect; here connect() opens the confirm, which is fine - the key point is
	// the band is selectable + tunable again).
	if mm.mode == modeBrowse && mm.connected == nil {
		// connect() should have advanced past plain browse (confirm or channel).
		t.Errorf("Enter on the reconnectable band did nothing (mode=%d)", mm.mode)
	}
}

// TestWarmReconnectSkipsStagedSequence (#3): a FIRST (cold) connect runs the staged
// tune-in; a re-connect to a band tuned earlier this session is FAST - it drops
// straight into the open channel with no staged modeConnecting dwell. (The staged
// view is also skippable via any key, covered by the modeConnecting key handler.)
func TestWarmReconnectSkipsStagedSequence(t *testing.T) {
	var tm tea.Model = New("http://broker.local", "tester")
	tm, _ = tm.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	tm, _ = tm.Update(offersMsg{freeBand("gpt-oss-20b")})
	mm := connectFree(t, tm.(model), "gpt-oss-20b")
	// First connect: under quiet (tests are non-TTY) the staged view auto-resolves to
	// the channel, but the band is now marked recent/warm.
	if !mm.recentBands["gpt-oss-20b"] {
		t.Fatalf("first connect did not record the band as recent")
	}
	if mm.mode != modeChat {
		t.Fatalf("first connect did not reach the channel (mode=%d)", mm.mode)
	}
	// disconnect, then re-connect: the warm path goes STRAIGHT to the channel.
	nm, _ := mm.disconnect()
	mm = nm.(model)
	mm.proxyUp = true
	mm.endpoint = "http://127.0.0.1:4141/v1"
	nm, _ = mm.connect()
	mm = nm.(model)
	nm, _ = mm.openChannel()
	mm = nm.(model)
	if mm.mode != modeChat {
		t.Errorf("warm reconnect did not drop straight into the channel (mode=%d)", mm.mode)
	}
	if mm.connectStage != connectStageDone {
		t.Errorf("warm reconnect did not skip the staged sequence (stage=%d)", mm.connectStage)
	}
}

// TestStagedSequenceSkippable (#3): in the staged tune-in, any key jumps straight to
// the open channel (an impatient operator should not sit through the animation).
func TestStagedSequenceSkippable(t *testing.T) {
	mm := New("http://broker.local", "tester")
	mm.width, mm.height = 100, 30
	mm.connected = &offer{NodeID: "nyx", Model: "gpt-oss-20b", Online: true}
	mm.endpoint = "http://127.0.0.1:4141/v1"
	mm.apikey = "roger-local"
	mm.mode = modeConnecting
	mm.connectStage = 1 // mid-sequence
	var km tea.Model = mm
	km, _ = km.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm = km.(model)
	if mm.mode != modeChat {
		t.Errorf("enter during the staged sequence did not skip to the channel (mode=%d)", mm.mode)
	}
}
