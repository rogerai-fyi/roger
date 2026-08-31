package harness

// observe_edit_test.go - the read-before-write guard, against the two tools that arrived
// with edit_file and read_file paging. Both holes were found by the pre-push audit, and
// both are safety properties rather than conveniences: one refuses a legitimate write, the
// other permits a destructive one.

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTemp(t *testing.T, root, rel, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, rel), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// An edit is a write. Leaving the observation stale after one made the NEXT write_file to
// that path fail with "changed on disk since you read it" - blaming the operator for the
// agent's own edit, and sending it to re-read a file it had just correctly changed.
func TestAnEditRefreshesTheObservationItInvalidates(t *testing.T) {
	root := t.TempDir()
	writeTemp(t, root, "f.txt", "alpha\nbeta\n")
	l := NewLoop(root, "", nil, nil)

	l.noteObserved("read_file", map[string]any{"path": "f.txt"})
	// The agent edits it, as edit_file really would.
	writeTemp(t, root, "f.txt", "alpha\ndelta\n")
	l.noteWritten("edit_file", map[string]any{"path": "f.txt"})

	if msg := l.GuardWriteNeedsRead("write_file", map[string]any{"path": "f.txt"}, ConversationView{}); msg != "" {
		t.Fatalf("a write after the agent's OWN edit was refused: %s", msg)
	}
}

// A partial read is not an observation of the file. read_file gained offset/limit so a long
// file could be paged; treating a window as a full observation let read_file(offset:1,
// limit:1) license a write_file that replaces everything the model never saw - the exact
// blind overwrite this guard exists to prevent.
func TestAWindowedReadDoesNotLicenseAWholeFileWrite(t *testing.T) {
	root := t.TempDir()
	writeTemp(t, root, "big.txt", "l1\nl2\nl3\nl4\nl5\n")
	l := NewLoop(root, "", nil, nil)

	for _, args := range []map[string]any{
		{"path": "big.txt", "offset": float64(1), "limit": float64(1)},
		{"path": "big.txt", "offset": float64(2)},
		{"path": "big.txt", "limit": float64(2)},
	} {
		l.observed = observations{} // each window starts from nothing seen
		l.noteObserved("read_file", args)
		if msg := l.GuardWriteNeedsRead("write_file", map[string]any{"path": "big.txt"}, ConversationView{}); msg == "" {
			t.Errorf("a windowed read %v licensed a whole-file write: the model would replace "+
				"lines it never saw", args)
		}
	}

	// The whole-file read still licenses it, or the guard would block ordinary work.
	l.observed = observations{}
	l.noteObserved("read_file", map[string]any{"path": "big.txt"})
	if msg := l.GuardWriteNeedsRead("write_file", map[string]any{"path": "big.txt"}, ConversationView{}); msg != "" {
		t.Fatalf("a full read must still license a write: %s", msg)
	}
}
