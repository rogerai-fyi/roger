package tui

import "testing"

// TestOffersTransientEmptyKeepsList pins the band-list flicker fix: a single empty /discover
// (a rescan that load-balanced onto a still-syncing broker instance) must NOT blank a populated
// list; only a SUSTAINED empty does. The alternating-instance case (full, empty, full, empty)
// never blanks because each full scan resets the counter.
func TestOffersTransientEmptyKeepsList(t *testing.T) {
	full := []offer{{Model: "gpt-oss-20b", Online: true}}
	m := seedFor(120, modeBrowse, false)
	m.offers = full
	m.bands = m.mergeStickyBand(groupBands(m.offers, m.limits))
	m.loadedOnce = true

	out, _ := m.Update(offersMsg(nil)) // transient empty
	om := asModel(out)
	if len(om.offers) == 0 {
		t.Error("a single transient empty scan should KEEP the last-known offers (no flicker)")
	}
	if om.emptyScans != 1 {
		t.Errorf("emptyScans = %d, want 1", om.emptyScans)
	}

	out2, _ := om.Update(offersMsg(full)) // a full scan resets the counter
	if asModel(out2).emptyScans != 0 {
		t.Error("a non-empty scan should reset emptyScans (alternating-instance case never blanks)")
	}

	cur := asModel(out2) // now drive SUSTAINED empties -> eventually blanks (genuine empty)
	for i := 0; i < emptyScansToBlank; i++ {
		o, _ := cur.Update(offersMsg(nil))
		cur = asModel(o)
	}
	if len(cur.offers) != 0 {
		t.Errorf("after %d consecutive empty scans a genuine empty should finally show", emptyScansToBlank)
	}
}

// TestOffersFirstLoadEmptyAccepts: the FIRST scan (loadedOnce false) accepts an empty result -
// a genuinely empty market shows immediately on startup (the debounce only guards a populated list).
func TestOffersFirstLoadEmptyAccepts(t *testing.T) {
	m := seedFor(120, modeBrowse, false)
	m.offers = nil
	m.loadedOnce = false
	out, _ := m.Update(offersMsg(nil))
	om := asModel(out)
	if !om.loadedOnce {
		t.Error("first scan should set loadedOnce")
	}
	if len(om.offers) != 0 {
		t.Error("first-load empty should be accepted (genuinely no stations)")
	}
}
