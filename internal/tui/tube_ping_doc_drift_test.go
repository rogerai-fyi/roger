package tui

// tube_ping_doc_drift_test.go pins the handoff doc's "Canonical Tube Ping - do not redesign"
// art to the art the TUI actually draws.
//
// The two disagreed at HEAD: the doc carried the six-cell interior while tubePingRows had
// already been corrected to seven on a single axis. A doc that says "do not redesign" and
// then shows the rejected drawing is worse than no doc - the next reader implements the
// wrong thing on the doc's authority. Prose cannot be trusted to track code by convention,
// so it is tested instead.

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

const handoffDoc = "../../docs/tui-restart-handoff-2026-07-29.md"

func TestHandoffDocShowsTheCanonicalTubePing(t *testing.T) {
	b, err := os.ReadFile(handoffDoc)
	if err != nil {
		t.Fatalf("read %s: %v", handoffDoc, err)
	}
	body := string(b)

	const heading = "## Canonical Tube Ping"
	i := strings.Index(body, heading)
	if i < 0 {
		t.Fatalf("%s no longer has a %q section", handoffDoc, heading)
	}

	block := regexp.MustCompile("(?s)```text\n(.*?)```").FindStringSubmatch(body[i:])
	if block == nil {
		t.Fatalf("%s: the canonical section has no ```text art block", handoffDoc)
	}
	got := strings.Split(strings.TrimRight(block[1], "\n"), "\n")

	if len(got) != len(tubePingRows) {
		t.Fatalf("doc art has %d rows, tubePingRows has %d:\ndoc:\n%s\ncode:\n%s",
			len(got), len(tubePingRows), strings.Join(got, "\n"), strings.Join(tubePingRows, "\n"))
	}
	for n := range tubePingRows {
		if got[n] != tubePingRows[n] {
			t.Errorf("row %d drifted:\n  doc:  %q\n  code: %q", n+1, got[n], tubePingRows[n])
		}
	}
}
