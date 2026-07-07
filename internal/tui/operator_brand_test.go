package tui

// operator_brand_test.go - the per-brand presence SEAM (founder direction 2026-07-06):
// each registry entry can carry a data-only BrandPlate/BrandAccent/BrandGlyph that the
// PATCHING YOU THROUGH screen and the picker render inside RogerAI's frame. The art pass
// lands as registry data; these tests pin that the seam renders it without any change to
// the transition logic, and that the EMPTY default stays the text-only house style.

import (
	"strings"
	"testing"

	"github.com/rogerai-fyi/roger/internal/operator"
)

// brandGuestDetection builds a detection for a fake guest carrying brand data.
func brandGuestDetection() operator.Detection {
	return operator.Detection{
		Guest: operator.Guest{
			Name: "opencode", Bin: "opencode", Provider: "openai",
			InstallHint: "x", KnownGood: "1.17.11", Strategy: operator.StrategyScratchConfig,
			BrandPlate:  "█▀█ █▀█ █▀▀ █▄░█\n█▄█ █▀▀ ██▄ █░▀█",
			BrandAccent: "#fab387",
			BrandGlyph:  "◆",
		},
		Path: "/fake/opencode", Version: "1.17.11",
	}
}

// TestOperatorBrandPlateRenders: a registry BrandPlate lands on the PATCHING screen
// between the header and the wire plate; no logic change needed (data-only seam).
func TestOperatorBrandPlateRenders(t *testing.T) {
	m := asModel(agentReady(t))
	m.operatorHandoff = &operatorHandoff{det: brandGuestDetection()}
	view := stripANSI(m.operatorPatchView(120))
	if !strings.Contains(view, "█▀█ █▀█ █▀▀ █▄░█") || !strings.Contains(view, "█▄█ █▀▀ ██▄ █░▀█") {
		t.Fatalf("the brand plate lines must render on the PATCHING screen:\n%s", view)
	}
	head := strings.Index(view, "PATCHING YOU THROUGH")
	plate := strings.Index(view, "█▀█")
	mic := strings.Index(view, "mic to")
	if !(head < plate && plate < mic) {
		t.Fatalf("plate must sit between the header and the mic-to line (head=%d plate=%d mic=%d)", head, plate, mic)
	}
}

// TestOperatorBrandDefaultUnchanged: no BrandPlate = the text-only house default, with
// no stray blank block where the plate would sit.
func TestOperatorBrandDefaultUnchanged(t *testing.T) {
	d := brandGuestDetection()
	d.Guest.BrandPlate, d.Guest.BrandAccent, d.Guest.BrandGlyph = "", "", ""
	m := asModel(agentReady(t))
	m.operatorHandoff = &operatorHandoff{det: d}
	view := stripANSI(m.operatorPatchView(120))
	if !strings.Contains(view, "PATCHING YOU THROUGH") || !strings.Contains(view, "mic to") {
		t.Fatalf("the default patch view must keep the house layout:\n%s", view)
	}
	if strings.Contains(view, "█") {
		t.Fatalf("no brand art may render without registry data:\n%s", view)
	}
}

// TestOperatorBrandGlyphInPicker: a BrandGlyph decorates the guest's picker row; rows
// without one are untouched.
func TestOperatorBrandGlyphInPicker(t *testing.T) {
	m := asModel(agentReady(t))
	m.operatorDetections = []operator.Detection{brandGuestDetection()}
	m.operatorPicker = true
	m.operatorRows = m.buildOperatorRows()
	view := stripANSI(m.operatorPickerView(120))
	if !strings.Contains(view, "◆") {
		t.Fatalf("the brand glyph must decorate the guest row:\n%s", view)
	}
}
