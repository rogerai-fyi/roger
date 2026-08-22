package tui

import (
	"strings"
	"testing"
)

// The band card is the operator's only view of what the MARKET will see them as. When
// detection found nothing it must say so, because a hidden row cannot tell "this model
// published no metadata" apart from "detection is broken" — and those need different
// reactions from the operator.
func TestCardStatesUndetectedRatherThanHidingIt(t *testing.T) {
	got := stripANSI(cfgVariant(shareRow{model: "qwen3-27b-q4_k_m", upstream: "http://127.0.0.1:11434/v1"}))
	if got == "" {
		t.Fatal("an undetected variant rendered as an empty row")
	}
	if !strings.Contains(got, "nothing detected") {
		t.Fatalf("did not state its own absence: %q", got)
	}
	// The id is full of things a guesser would mine. None may appear as a claim.
	if strings.Contains(strings.ToLower(got), "q4_k_m") {
		t.Fatalf("a quant was mined from the model id: %q", got)
	}
}

// With no server there is nothing to have detected, and "nothing detected" would blame
// detection for the absence of a runtime. Different cause, different line.
func TestNoServerIsNotTheSameAsNothingDetected(t *testing.T) {
	got := stripANSI(cfgVariant(shareRow{model: "m"}))
	if strings.Contains(got, "nothing detected") {
		t.Fatalf("no-server row blamed detection: %q", got)
	}
}

func TestCardShowsEveryDetectedField(t *testing.T) {
	got := stripANSI(cfgVariant(shareRow{
		model: "m", upstream: "http://127.0.0.1:11434/v1",
		quant: "IQ4_XS", weights: "bartowski", variant: "instruct",
	}))
	for _, want := range []string{"IQ4_XS", "bartowski", "instruct"} {
		if !strings.Contains(got, want) {
			t.Fatalf("%q missing from card: %q", want, got)
		}
	}
}

// Partial detection is the common case and must not print a dangling separator.
func TestCardPartialDetectionHasNoDanglingSeparator(t *testing.T) {
	got := strings.TrimSpace(stripANSI(cfgVariant(shareRow{
		model: "m", upstream: "http://x/v1", quant: "Q4_K_M",
	})))
	if got != "Q4_K_M" {
		t.Fatalf("partial render = %q, want just the quant", got)
	}
}

// cfgVariant existing is not enough — the row must actually be ON the card. This is the
// lock that would have caught the original gap, where detection was wired end to end to
// the broker and the operator's own surface never showed it.
func TestVariantRowIsOnTheCard(t *testing.T) {
	m := model{cfgModel: "m"}
	sr := shareRow{model: "m", upstream: "http://127.0.0.1:11434/v1", quant: "Q5_K_M"}
	rows := m.cfgProviderRows(sr, BandRow{}, false, false, false)
	for _, r := range rows {
		if r.label == "variant" {
			if !strings.Contains(stripANSI(r.value), "Q5_K_M") {
				t.Fatalf("variant row present but empty: %q", r.value)
			}
			return
		}
	}
	t.Fatal("no variant row on the band card - detection is invisible to the operator again")
}
