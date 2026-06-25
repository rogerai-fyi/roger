package tui

import (
	"strings"
	"testing"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
)

// TestSignalLevelDiffersByValue: the meter LEVEL is a real readout of the broker's
// 0..100 signal, so two bands with different signals render different towers (bands
// genuinely differ, not a uniform decoration). Frozen frame + idle (inFlight 0) so the
// comparison is purely the level, not animation phase.
func TestSignalLevelDiffersByValue(t *testing.T) {
	low := signalBarsRaw(frozenFrame, 20, 0, true, 0, 1)
	high := signalBarsRaw(frozenFrame, 90, 0, true, 0, 1)
	if low == high {
		t.Errorf("signal 20 and 90 rendered the same tower %q == %q; the level must reflect the real signal", low, high)
	}
}

// TestSignalAnimatesOnlyWhenActive: the animation is an HONEST activity readout. We
// drive signalTowerAt (the pure render at a given frame) so the motion is observable
// independent of the process-wide quiet freeze (tests run with stdout not-a-TTY, which
// freezes signalBarsRaw itself - see TestSignalReducedMotionStatic for that gate).
//   - An idle band (amp from inFlight 0, no tps) is STEADY: identical across frames.
//   - A band actively serving (amp from inFlight > 0) ANIMATES: it differs across frames.
//   - More in-flight load swings WIDER (a busier band is more visibly active).
//   - Offline is the flat tower regardless of signal/activity (via signalBarsRaw).
func TestSignalAnimatesOnlyWhenActive(t *testing.T) {
	const base = 5

	// Idle: signalAmp == 0 -> steady. The same render at every frame.
	if a := signalAmp(0, 0); a != 0 {
		t.Errorf("an idle band (no in-flight, no tps) must have amp 0, got %d", a)
	}
	idleA := signalTowerAt(0, base, signalAmp(0, 0))
	idleB := signalTowerAt(1, base, signalAmp(0, 0))
	idleC := signalTowerAt(2, base, signalAmp(0, 0))
	if !(idleA == idleB && idleB == idleC) {
		t.Errorf("an idle-but-online band must be steady (no animation): %q %q %q", idleA, idleB, idleC)
	}

	// Active: signalAmp > 0 -> the tower scans (differs across frames).
	if a := signalAmp(2, 0); a == 0 {
		t.Fatalf("a band actively serving (inFlight 2) must have a non-zero amp")
	}
	moved := false
	prev := signalTowerAt(0, base, signalAmp(2, 0))
	for f := 1; f < 8; f++ {
		cur := signalTowerAt(f, base, signalAmp(2, 0))
		if cur != prev {
			moved = true
			break
		}
		prev = cur
	}
	if !moved {
		t.Errorf("a band actively serving (inFlight>0) must animate across frames, but it was static")
	}

	// A busy band swings wider than a barely-busy one: heavier load -> >= amplitude ->
	// >= the set of distinct phases rendered across a frame sweep.
	distinct := func(inFlight int) int {
		seen := map[string]bool{}
		amp := signalAmp(inFlight, 0)
		for f := 0; f < 12; f++ {
			seen[signalTowerAt(f, base, amp)] = true
		}
		return len(seen)
	}
	if distinct(5) < distinct(1) {
		t.Errorf("a heavily-loaded band should swing at least as wide as a lightly-loaded one (%d vs %d)", distinct(5), distinct(1))
	}

	// Offline is the flat no-signal tower no matter the signal / activity.
	if got := signalBarsRaw(3, 90, 500, false, 9, 4); got != signalFlat() {
		t.Errorf("offline band must be the flat tower regardless of activity, got %q", got)
	}
}

// TestSignalReducedMotionStatic: under reduced-motion (the frozen frame anim() picks
// for quiet / windowshade), even an actively-serving band renders a STATIC truthful
// tower - no animation. The level still reflects the real signal; only the motion is
// removed. We assert the frozen-frame render is stable (it is the single phase a pipe /
// NO_COLOR / compact mode sees).
func TestSignalReducedMotionStatic(t *testing.T) {
	// The frozen frame is a single fixed phase: rendering it twice is identical (a still),
	// even for a busy band. (sigFrame() returns frozenFrame in compact mode, and anim()
	// pins it under quiet - both collapse the scan to this one stable frame.)
	a := signalBarsRaw(frozenFrame, 70, 0, true, 6, 2)
	b := signalBarsRaw(frozenFrame, 70, 0, true, 6, 2)
	if a != b {
		t.Errorf("the frozen reduced-motion frame must be a stable still: %q != %q", a, b)
	}
	// And it must still be a truthful, non-blank meter (the level carries through).
	if a == signalFlat() {
		t.Errorf("reduced-motion still must keep the real level (non-blank), got the flat tower")
	}
}

// TestScanOffsetBounded: the per-cell animation offset stays within [-amp,+amp] and is
// 0 for an idle (amp==0) cell - so the wave never lifts the resting LEVEL, it only adds
// honest motion around the real signal.
func TestScanOffsetBounded(t *testing.T) {
	for _, amp := range []int{0, 1, 2} {
		for phase := -10; phase <= 10; phase++ {
			off := scanOffset(phase, amp)
			if amp == 0 && off != 0 {
				t.Errorf("idle (amp 0) must have zero offset, got %d at phase %d", off, phase)
			}
			if off < -amp || off > amp {
				t.Errorf("scanOffset(%d,%d)=%d out of [-%d,%d]", phase, amp, off, amp, amp)
			}
		}
	}
}

// TestGroupBandsCarriesInFlight: the broker's per-offer in_flight flows offer -> band
// (summed across online stations), so the band meter has a REAL activity figure to
// animate from (offline stations do not count toward load).
func TestGroupBandsCarriesInFlight(t *testing.T) {
	offers := []offer{
		{NodeID: "a", Model: "m", PriceOut: 0.2, Online: true, Signal: 60, InFlight: 2},
		{NodeID: "b", Model: "m", PriceOut: 0.3, Online: true, Signal: 55, InFlight: 3},
		{NodeID: "c", Model: "m", PriceOut: 0.4, Online: false, InFlight: 9}, // offline: ignored
	}
	bands := groupBands(offers, nil)
	if len(bands) != 1 {
		t.Fatalf("groupBands -> %d bands want 1", len(bands))
	}
	if bands[0].inFlight != 5 {
		t.Errorf("band.inFlight = %d want 5 (2+3 across ONLINE stations only)", bands[0].inFlight)
	}
}

// TestBrowseSignalWiredToRealActivity: end to end, the broker's per-offer in_flight
// reaches the band the browse meter animates from - so the meter's motion is driven by
// REAL load, not a decorative loop. (The render itself is frozen here because tests run
// with stdout not-a-TTY = reduced-motion; the motion path is covered by
// TestSignalAnimatesOnlyWhenActive. This asserts the data wiring + that the busy and
// idle bands compute different animation amplitudes.)
func TestBrowseSignalWiredToRealActivity(t *testing.T) {
	bandFor := func(inFlight int) band {
		var m tea.Model = New("http://broker.local", "tester")
		m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
		m, _ = m.Update(offersMsg{
			{NodeID: "n", Model: "busy", PriceIn: 0.2, PriceOut: 0.3, Online: true, Signal: 70, InFlight: inFlight},
		})
		mm := asModel(m)
		if len(mm.bands) != 1 {
			t.Fatalf("expected 1 band, got %d", len(mm.bands))
		}
		return mm.bands[0]
	}
	busy := bandFor(3)
	idle := bandFor(0)
	if busy.inFlight == 0 {
		t.Errorf("the busy band must carry the real in-flight load into the meter, got 0")
	}
	if idle.inFlight != 0 {
		t.Errorf("the idle band must carry zero in-flight load, got %d", idle.inFlight)
	}
	// The meter the browse row builds (signalAmp from the band's real load) animates for
	// the busy band and is steady for the idle one.
	if signalAmp(busy.inFlight, busy.cheapest.TPS) == 0 {
		t.Errorf("the busy band's meter must animate (amp > 0)")
	}
	if signalAmp(idle.inFlight, idle.cheapest.TPS) != 0 {
		t.Errorf("the idle band's meter must be steady (amp 0)")
	}
}

// TestFreqEntryFlipsModeAndLabel: pressing ~ opens the PRIVATE FREQUENCY input
// (modeFreqEntry); a successful resolve (freqResolvedMsg ok) flips the header to
// PRIVATE FREQ + the code, filters the band list to ONLY that band, and esc returns to
// the OPEN MARKET. The label color/mode change is the distinct "left the public
// marketplace" indicator.
func TestFreqEntryFlipsModeAndLabel(t *testing.T) {
	var m tea.Model = New("http://broker.local", "tester")
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	// Seed a public market list (two public bands).
	m, _ = m.Update(offersMsg{
		{NodeID: "p1", Model: "public-a", PriceOut: 0.3, Online: true, Signal: 50},
		{NodeID: "p2", Model: "public-b", PriceOut: 0.4, Online: true, Signal: 50},
	})

	// Default header reads OPEN MARKET.
	if !strings.Contains(stripANSI(m.View()), "OPEN MARKET") {
		t.Fatalf("default header should read OPEN MARKET:\n%s", stripANSI(m.View()))
	}

	// ~ opens the dedicated freq-entry input.
	m, _ = m.Update(keyRunes("~"))
	if asModel(m).mode != modeFreqEntry {
		t.Fatalf("~ should open modeFreqEntry, got mode %v", asModel(m).mode)
	}
	if !strings.Contains(stripANSI(m.View()), "PRIVATE FREQ") {
		t.Errorf("the freq-entry input should label itself PRIVATE FREQ:\n%s", stripANSI(m.View()))
	}

	// Simulate a SUCCESSFUL resolve landing (the off-loop client.ResolveBand returning a
	// private band's offers): the header flips to PRIVATE FREQ + the code, and the list is
	// filtered to ONLY that band.
	m, _ = m.Update(freqResolvedMsg{
		freq:  "147.520 MHz 8F3K9M2Q",
		label: "147.520 MHz · 8F3K-9M2Q",
		offers: []offer{
			{NodeID: "priv", Model: "secret-model", PriceOut: 0.5, Online: true, Signal: 60},
		},
		ok: true,
	})
	mm := asModel(m)
	if mm.tuneFreq == "" {
		t.Errorf("a resolved freq should set tuneFreq")
	}
	view := stripANSI(mm.View())
	if !strings.Contains(view, "PRIVATE FREQ 147.520 MHz") {
		t.Errorf("tuned header should read PRIVATE FREQ + the code:\n%s", view)
	}
	if !strings.Contains(view, "secret-model") {
		t.Errorf("the band list should be filtered to the private band:\n%s", view)
	}
	if strings.Contains(view, "public-a") || strings.Contains(view, "public-b") {
		t.Errorf("the public bands must NOT show while on a private freq:\n%s", view)
	}

	// esc returns to OPEN MARKET (clears the freq).
	m, _ = mm.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if asModel(m).tuneFreq != "" {
		t.Errorf("esc should clear the private freq back to OPEN MARKET")
	}
}

// TestFreqEntryWidthAndNoColorSafe: the PRIVATE FREQUENCY input + the tuned PRIVATE
// FREQ header render without overflowing narrow widths and stay readable under NO_COLOR
// (the accent is stripped but the "PRIVATE FREQ" text still carries the mode).
func TestFreqEntryWidthAndNoColorSafe(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	for _, w := range []int{40, 64, 80, 120} {
		// The freq-entry input strip.
		var m tea.Model = New("http://broker.local", "tester")
		m, _ = m.Update(tea.WindowSizeMsg{Width: w, Height: 30})
		m, _ = m.Update(offersMsg{{NodeID: "n", Model: "m", PriceOut: 0.3, Online: true, Signal: 50}})
		m, _ = m.Update(keyRunes("~"))
		em := asModel(m)
		em.status = "" // the transient status is incidental prose (soft-wraps); test the structure
		entry := stripANSI(em.View())
		if !strings.Contains(entry, "PRIVATE FREQ") && !strings.Contains(entry, "FREQ ▸") {
			t.Errorf("width %d: freq-entry must label itself as a private FREQ input under NO_COLOR:\n%s", w, entry)
		}
		for _, line := range strings.Split(entry, "\n") {
			if vis := utf8.RuneCountInString(line); vis > w {
				t.Errorf("width %d: freq-entry line overflows (%d): %q", w, vis, line)
			}
		}
		// The tuned PRIVATE FREQ header.
		m, _ = em.Update(freqResolvedMsg{freq: "147.520 MHz 8F3K9M2Q", label: "147.520 MHz · 8F3K-9M2Q",
			offers: []offer{{NodeID: "priv", Model: "secret", PriceOut: 0.5, Online: true, Signal: 60}}, ok: true})
		tm := asModel(m)
		tm.status = ""
		tuned := stripANSI(tm.View())
		for _, line := range strings.Split(tuned, "\n") {
			if vis := utf8.RuneCountInString(line); vis > w {
				t.Errorf("width %d: tuned PRIVATE FREQ header line overflows (%d): %q", w, vis, line)
			}
		}
	}
}

// TestFreqWrongIsIndistinguishableFromEmpty: the SECURITY property. A wrong /
// nonexistent code and an EMPTY code must produce the IDENTICAL outcome - the broker's
// uniform "no station" negative - so an attacker cannot tell "this code exists" from
// "no band here". We drive the freqResolvedMsg(ok:false) handler (the single exit for
// every negative the resolve path produces) and assert it is the same for both, with no
// "exists but forbidden" or "you typed nothing" tell, and no header flip into PRIVATE.
func TestFreqWrongIsIndistinguishableFromEmpty(t *testing.T) {
	negative := func(code string) (mode, string, string) {
		var m tea.Model = New("http://broker.local", "tester")
		m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
		m, _ = m.Update(offersMsg{
			{NodeID: "p1", Model: "public-a", PriceOut: 0.3, Online: true, Signal: 50},
		})
		// Every negative case (wrong / empty / nonexistent / off-air) funnels through the
		// same ok:false message in resolveFreq, so simulate that uniform reply.
		m, _ = m.Update(freqResolvedMsg{freq: code, ok: false})
		mm := asModel(m)
		return mm.mode, mm.tuneFreq, stripANSI(mm.status)
	}

	wrongMode, wrongFreq, wrongStatus := negative("GARBAGE-NONEXISTENT-CODE")
	emptyMode, emptyFreq, emptyStatus := negative("")

	if wrongStatus != emptyStatus {
		t.Errorf("a wrong code and an empty code must report IDENTICALLY (no oracle):\n wrong: %q\n empty: %q", wrongStatus, emptyStatus)
	}
	if wrongMode != emptyMode || wrongFreq != emptyFreq {
		t.Errorf("wrong and empty must leave the SAME mode/state: (%v,%q) vs (%v,%q)", wrongMode, wrongFreq, emptyMode, emptyFreq)
	}
	// Neither leaks a private channel: the header stays OPEN MARKET, no tuneFreq set.
	if wrongFreq != "" {
		t.Errorf("a negative resolve must NOT set tuneFreq (no leak of a private channel)")
	}
	// The status must not name the code or otherwise distinguish "real but off-air".
	if strings.Contains(wrongStatus, "GARBAGE") {
		t.Errorf("the negative status must not echo the attempted code: %q", wrongStatus)
	}
}
