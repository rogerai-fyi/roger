package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// browseRowView builds a logged-in browse model on the given offers at a wide width
// (so the ctx + t/s columns are present) and returns the plain (ANSI-stripped) view.
func browseRowView(t *testing.T, w int, offers ...offer) string {
	t.Helper()
	var m tea.Model = New("http://broker.local", "tester")
	m, _ = m.Update(tea.WindowSizeMsg{Width: w, Height: 30})
	m, _ = m.Update(offersMsg(offers))
	m, _ = m.Update(balanceMsg{balance: 100, loggedIn: true})
	m, _ = m.Update(tickMsg{})
	return stripANSI(m.View())
}

// TestBandRowShowsInAndOutPrice: the band LIST row shows the headline price as
// in·out (cheapest input · cheapest output), exactly like the web /models row -
// not an out-only value.
func TestBandRowShowsInAndOutPrice(t *testing.T) {
	out := browseRowView(t, 120, offer{
		NodeID: "a", Model: "qwen3-8b", PriceIn: 0.10, PriceOut: 0.40, Online: true, Signal: 70,
	})
	if !strings.Contains(out, "$/1M in·out") {
		t.Errorf("band header missing the in·out price column:\n%s", out)
	}
	if !strings.Contains(out, "0.10·0.40") {
		t.Errorf("band row missing the in·out price (0.10·0.40):\n%s", out)
	}
}

// TestBandRowSignalIsBrokerValue: the row's signal METER is driven by the broker's
// per-offer signal, NOT a TUI-local recompute from tps. Two bands with IDENTICAL tps
// but different broker signals must render different towers (the web had a
// divergent-local-signal bug; the TUI must show the broker's value).
func TestBandRowSignalIsBrokerValue(t *testing.T) {
	// Same tps (0 -> no tps signal at all), different broker signal. signalBarsRaw is
	// the single render path the row uses; a recompute-from-tps would tie these.
	lowSig := signalBarsRaw(0, 20, 0, true, 0, 1)  // broker signal 20
	highSig := signalBarsRaw(0, 95, 0, true, 0, 1) // broker signal 95
	if lowSig == highSig {
		t.Fatalf("signal meter ignored the broker signal (recompute bug): %q == %q", lowSig, highSig)
	}
	// And the LEVEL must track the broker signal monotonically: a higher broker signal
	// is a taller resting tower (signalLevel is the broker-signal -> glyph map).
	if signalLevel(20) >= signalLevel(95) {
		t.Errorf("higher broker signal must read taller: lvl(20)=%d lvl(95)=%d", signalLevel(20), signalLevel(95))
	}
	// A band carrying a broker signal but zero tps still meters off the broker value
	// (an on-air-but-idle node is not blank) - proves tps is not the driver.
	idle := signalBarsRaw(0, 60, 0, true, 0, 1)
	if idle == signalFlat() {
		t.Errorf("a band with broker signal 60 but tps 0 should still meter, got the flat tower: %q", idle)
	}
}

// TestBandRowCtxEstimatedMarker: a band whose ctx is the ESTIMATED default renders
// the ctx cell with a leading "~"; a DETECTED window renders solid (no "~").
func TestBandRowCtxEstimatedMarker(t *testing.T) {
	est := browseRowView(t, 120, offer{
		NodeID: "a", Model: "est-band", PriceIn: 0.1, PriceOut: 0.2, Ctx: 32768, CtxEstimated: true, Online: true, Signal: 50,
	})
	if !strings.Contains(est, "~33k") && !strings.Contains(est, "~32k") {
		t.Errorf("estimated ctx must render with a leading ~:\n%s", est)
	}
	det := browseRowView(t, 120, offer{
		NodeID: "a", Model: "det-band", PriceIn: 0.1, PriceOut: 0.2, Ctx: 131072, CtxEstimated: false, Online: true, Signal: 50,
	})
	// the detected window is present and NOT prefixed with "~"
	if !strings.Contains(det, "131k") {
		t.Errorf("detected ctx 131072 should render as 131k:\n%s", det)
	}
	if strings.Contains(det, "~131k") {
		t.Errorf("a DETECTED window must not carry the ~ estimated marker:\n%s", det)
	}
}

// TestBandRowHonestEmpty: an unmeasured t/s renders "-" (never a fabricated rate),
// and the t/s + price columns honor "no data" rather than inventing values.
func TestBandRowHonestEmpty(t *testing.T) {
	out := browseRowView(t, 120, offer{
		NodeID: "a", Model: "fresh-band", PriceIn: 0.1, PriceOut: 0.2, Online: true, Signal: 45,
		// no TPS reported
	})
	// the t/s column header is present at this width
	if !strings.Contains(out, "t/s") {
		t.Errorf("wide band table missing the t/s column:\n%s", out)
	}
	// the fresh band's row must show a "-" for the unmeasured t/s (find the row line)
	var row string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "fresh-band") {
			row = line
		}
	}
	if row == "" {
		t.Fatalf("fresh-band row not found:\n%s", out)
	}
	// the row carries the price (real) but a dash for the unmeasured throughput
	if !strings.Contains(row, "0.10·0.20") {
		t.Errorf("row missing real in·out price: %q", row)
	}
	if !strings.Contains(row, "-") {
		t.Errorf("unmeasured t/s should render '-' in the row: %q", row)
	}
}

// TestBandBestTPSReal: the row's headline t/s is the band's FASTEST online station
// (best_tps), mirroring the web - not the cheapest station's tps.
func TestBandBestTPSReal(t *testing.T) {
	bands := groupBands([]offer{
		{NodeID: "cheap-slow", Model: "m", PriceIn: 0.1, PriceOut: 0.10, Online: true, TPS: 30, Signal: 50},
		{NodeID: "dear-fast", Model: "m", PriceIn: 0.2, PriceOut: 0.50, Online: true, TPS: 220, Signal: 60},
	}, nil)
	if len(bands) != 1 {
		t.Fatalf("expected one band, got %d", len(bands))
	}
	if got := bandBestTPS(bands[0]); got != 220 {
		t.Errorf("band best tps = %v, want 220 (the fastest station, not the cheapest)", got)
	}
	// minIn / minOut are the independent cheapest input + output prices.
	if bands[0].minIn != 0.1 || bands[0].minOut != 0.10 {
		t.Errorf("band minIn/minOut = %v/%v, want 0.1/0.10", bands[0].minIn, bands[0].minOut)
	}
}

// TestBandRowOfflineHonestPrice: an offline band shows a bare "-" for price (never a
// fabricated in·out), matching the web's idle row.
func TestBandRowOfflineHonestPrice(t *testing.T) {
	off := band{model: "ghost", online: false, all: []offer{{Model: "ghost", PriceIn: 0.1, PriceOut: 0.2}}}
	if got := priceInOut(off); got != "-" {
		t.Errorf("offline band price = %q, want '-'", got)
	}
	free := band{model: "f", online: true, stations: 1, minIn: 0, minOut: 0}
	if got := priceInOut(free); got != "free" {
		t.Errorf("a fully free band should read 'free', got %q", got)
	}
}
