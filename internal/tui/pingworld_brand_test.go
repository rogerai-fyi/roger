package tui

import (
	"strings"
	"testing"
)

// TestPingWorldBrandCarriesDotFM: the Ping World screensaver's center surface band shows
// the ROGER·AI brand AND the domain (founder ask) — so the relax-view quietly doubles as
// the URL. That domain is now rogerai.fm: the screensaver was the last user-visible place
// still reading .fyi after the migration. Rendered seeded (no live data) at a width wide
// enough to stamp the brand.
func TestPingWorldBrandCarriesDotFM(t *testing.T) {
	out := stripANSI(renderWorldData(80, 24, 0, 7, nil))
	if !strings.Contains(out, "R O G E R") {
		t.Fatalf("surface band should carry the ROGER·AI brand:\n%s", out)
	}
	if !strings.Contains(out, ".fm") {
		t.Fatalf("surface band should carry the .fm domain, got:\n%s", out)
	}
	if strings.Contains(out, ".fyi") {
		t.Fatalf("surface band still shows the pre-migration .fyi domain:\n%s", out)
	}
}
