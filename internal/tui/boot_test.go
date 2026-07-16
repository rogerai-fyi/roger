package tui

// Increment 10: the tube WARM-UP BOOT frames - the ROGER·AI set glowing up like a tube
// radio (dim amber lettering -> settled brand -> the band tuning in). The host gates WHEN
// to play it (once per version); these lock the frames themselves.

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// The warm-up is low-to-full: the last frame is the settled brand + a "tuning in" sweep.
func TestBootFrames(t *testing.T) {
	f := bootFrames()
	if len(f) < 2 {
		t.Fatalf("the warm-up should have several frames, got %d", len(f))
	}
	last := stripANSI(f[len(f)-1])
	if !strings.Contains(last, "R O G E R") {
		t.Errorf("the final frame settles on the brand: %q", last)
	}
	if !strings.Contains(last, "tuning in") {
		t.Errorf("the final frame tunes the band in: %q", last)
	}
}

// PlayBoot draws the frames to the writer; under quiet (NO_COLOR / non-TTY) it prints
// NOTHING (the founder ruling: off entirely under quiet), so a pipe never gets a splash.
func TestPlayBootQuietIsSilent(t *testing.T) {
	defer func(q bool) { quiet = q }(quiet)

	quiet = true
	var buf bytes.Buffer
	PlayBoot(&buf, func(time.Duration) {})
	if buf.Len() != 0 {
		t.Errorf("quiet boot must print nothing, got %q", buf.String())
	}

	quiet = false
	buf.Reset()
	PlayBoot(&buf, func(time.Duration) {})
	if !strings.Contains(stripANSI(buf.String()), "R O G E R") {
		t.Errorf("a live boot draws the brand, got %q", buf.String())
	}
}
