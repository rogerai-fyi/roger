package tui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeBroker accepts every node registration so agent.Start succeeds and the toggled
// session is stored (a real register POST would otherwise fail and bail out).
func fakeBroker(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// rows builds n free share rows m1..mn, each on its own upstream so node ids stay
// distinct.
func freeRows(n int) []shareRow {
	out := make([]shareRow, 0, n)
	for i := 1; i <= n; i++ {
		out = append(out, shareRow{
			model: "m" + string(rune('0'+i)), ctx: 4096,
			upstream: "http://127.0.0.1:80" + string(rune('0'+i)) + "0/v1/chat/completions",
		})
	}
	return out
}

// TestShareSoftLimitDefault: the soft on-air cap defaults to 4 when the host supplies
// no share.max_on_air.
func TestShareSoftLimitDefault(t *testing.T) {
	mm := New("http://broker.local", "tester")
	if got := mm.maxOnAir(); got != 4 {
		t.Fatalf("default soft on-air cap = %d, want 4", got)
	}
	if defaultShareMaxOnAir != 4 {
		t.Fatalf("defaultShareMaxOnAir = %d, want 4", defaultShareMaxOnAir)
	}
	// A host-supplied value overrides it.
	mm2 := NewWithHooks("http://broker.local", "tester", nil, Hooks{ShareMaxOnAir: 7})
	if got := mm2.maxOnAir(); got != 7 {
		t.Fatalf("configured soft cap = %d, want 7", got)
	}
}

// TestShareSlotHeaderAndBlock: the SHARE selector shows the ON AIR n/max slots,
// blocks flipping another row on air at the cap with the clear message, and frees a
// slot when a band goes off air.
func TestShareSlotHeaderAndBlock(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	srv := fakeBroker(t)
	mm := NewWithHooks(srv.URL, "tester", nil, Hooks{ShareMaxOnAir: 2})
	mm.setShareRows(freeRows(3)) // m1,m2,m3 on distinct upstreams

	// Fill the 2 slots: m1 and m2 on air.
	mm.toggleShareAt(0)
	mm.toggleShareAt(1)
	if mm.sharesOnAir() != 2 {
		t.Fatalf("after 2 toggles, on air = %d, want 2", mm.sharesOnAir())
	}

	// The header shows ON AIR 2/2.
	mm.mode = modeShare
	mm.width, mm.height = 100, 30
	v := stripANSI(mm.shareView(100))
	if !strings.Contains(v, "ON AIR 2/2") {
		t.Errorf("share header should show the slot meter ON AIR 2/2:\n%s", v)
	}

	// At the cap, flipping m3 on air is BLOCKED with the clear message; m3 stays off air.
	mm.toggleShareAt(2)
	if mm.sharesOnAir() != 2 {
		t.Fatalf("at the cap, a 3rd toggle must NOT add a band: on air = %d, want 2", mm.sharesOnAir())
	}
	if mm.shares["m3"] != nil {
		t.Errorf("m3 must stay off air at the cap")
	}
	block := stripANSI(mm.status)
	if !strings.Contains(block, "2/2 on air") || !strings.Contains(block, "take one off air") ||
		!strings.Contains(block, "share.max_on_air") || !strings.Contains(block, "restart") {
		t.Errorf("block message wrong: %q", block)
	}

	// Free a slot: take m1 off air. Now m3 fits.
	mm.toggleShareAt(0)
	if mm.sharesOnAir() != 1 {
		t.Fatalf("after taking m1 off air, on air = %d, want 1", mm.sharesOnAir())
	}
	mm.toggleShareAt(2)
	if mm.shares["m3"] == nil {
		t.Errorf("after freeing a slot, m3 should go on air")
	}
	if mm.sharesOnAir() != 2 {
		t.Fatalf("on air after re-filling = %d, want 2", mm.sharesOnAir())
	}
	for _, s := range mm.shares {
		if s != nil {
			s.Stop()
		}
	}
}

// TestShareSlotHeaderNarrowSafe: the slot meter renders cleanly (no ANSI) across
// widths in NO_COLOR, including the compact/dense layout.
func TestShareSlotHeaderNarrowSafe(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	mm := NewWithHooks("http://broker.local", "tester", nil, Hooks{ShareMaxOnAir: 4})
	mm.setShareRows(freeRows(2))
	for _, w := range []int{40, 60, 88, 120} {
		v := mm.shareView(w)
		if strings.Contains(v, "\x1b[") {
			t.Errorf("width %d: ANSI escape leaked under NO_COLOR", w)
		}
		if !strings.Contains(stripANSI(v), "ON AIR 0/4") {
			t.Errorf("width %d: slot meter missing:\n%s", w, stripANSI(v))
		}
	}
	mm.compact = true
	if !strings.Contains(stripANSI(mm.shareView(120)), "ON AIR 0/4") {
		t.Errorf("compact: slot meter missing:\n%s", stripANSI(mm.shareView(120)))
	}
}
