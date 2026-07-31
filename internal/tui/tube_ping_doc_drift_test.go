package tui

// tube_ping_doc_drift_test.go pins EVERY written copy of the Tube Ping art to the art the
// TUI actually draws.
//
// The doc and the code disagreed at HEAD: the handoff doc carried the six-cell interior
// under the heading "do not redesign" while tubePingRows had already been corrected to
// seven on a single axis. A doc that forbids a redesign and then shows the rejected drawing
// is worse than no doc - the next reader implements the wrong thing on the doc's authority.
//
// The first version of this test hardcoded one file path, and that was the same mistake in
// miniature: it passed while two further copies (the durable mascot doc and the approved
// .feature) still showed the old art. It now SWEEPS - any file under docs/ or features/ that
// draws the receiver is checked by existing, so a new copy cannot be added unguarded.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// rogRow is the wordmark line, present in every copy of the art and in no other prose.
const rogRow = "ROG"

// dedent removes the common leading indentation of a block, so an art block quoted inside a
// Gherkin docstring compares equal to the same art at the left margin.
func dedent(lines []string) []string {
	min := -1
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		n := len(l) - len(strings.TrimLeft(l, " "))
		if min == -1 || n < min {
			min = n
		}
	}
	if min <= 0 {
		return lines
	}
	out := make([]string, len(lines))
	for i, l := range lines {
		if len(l) >= min {
			out[i] = l[min:]
		} else {
			out[i] = strings.TrimLeft(l, " ")
		}
	}
	return out
}

func TestEveryWrittenTubePingMatchesTheCode(t *testing.T) {
	roots := []string{"../../docs", "../../features"}
	// The wordmark sits on row 3 of a 5-row drawing.
	const before, after = 2, 2

	checked := 0
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			if ext := filepath.Ext(path); ext != ".md" && ext != ".feature" {
				return nil
			}
			b, readErr := os.ReadFile(filepath.Clean(path))
			if readErr != nil {
				return readErr
			}
			lines := strings.Split(string(b), "\n")
			for i, l := range lines {
				// The wordmark row of the art, not prose that happens to say ROG.
				if !strings.Contains(l, rogRow) || !strings.Contains(l, "█") {
					continue
				}
				if i < before || i+after >= len(lines) {
					t.Errorf("%s:%d: the art is truncated at the file boundary", path, i+1)
					continue
				}
				got := dedent(append([]string(nil), lines[i-before:i+after+1]...))
				checked++
				for n := range tubePingRows {
					if got[n] != tubePingRows[n] {
						t.Errorf("%s:%d drifted from tubePingRows on row %d:\n  written: %q\n  code:    %q",
							path, i-before+n+1, n+1, got[n], tubePingRows[n])
					}
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}

	// A sweep that finds nothing reports success while guarding nothing.
	if checked == 0 {
		t.Fatal("no written copy of the Tube Ping art was found; the sweep has gone blind")
	}
}
