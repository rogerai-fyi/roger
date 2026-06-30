package tui

import (
	"strings"
	"testing"
)

// TestPingWorldBrandCarriesDotFyi: the Ping World screensaver's center surface band shows
// the ROGER·AI brand AND the .fyi domain (founder ask) — so the relax-view quietly doubles
// as the URL. Rendered seeded (no live data) at a width wide enough to stamp the brand.
func TestPingWorldBrandCarriesDotFyi(t *testing.T) {
	out := stripANSI(renderWorldData(80, 24, 0, 7, nil))
	if !strings.Contains(out, "R O G E R") {
		t.Fatalf("surface band should carry the ROGER·AI brand:\n%s", out)
	}
	if !strings.Contains(out, ".fyi") {
		t.Fatalf("surface band should carry the .fyi domain, got:\n%s", out)
	}
}
