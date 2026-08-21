package harness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// observe_test.go - read before write, and don't clobber what changed underneath you.
//
// Two real ways to lose someone's work, both of which write_file allowed until now.
// The confirm gate does not save you from either: it shows a PATH, not "this replaces
// 400 lines you have never seen".

func obsLoop(t *testing.T) (*Loop, string) {
	t.Helper()
	root := t.TempDir()
	l := NewLoop(root, "sys", nil, nil)
	return l, root
}

// THE BLIND OVERWRITE. The model writes a file it never read; the file already exists;
// its contents are gone.
func TestWriteToAnUnreadExistingFileIsRefused(t *testing.T) {
	l, root := obsLoop(t)
	if err := os.WriteFile(filepath.Join(root, "notes.md"), []byte("work nobody wants to lose"), 0o600); err != nil {
		t.Fatal(err)
	}
	reason := l.GuardWriteNeedsRead("write_file", map[string]any{"path": "notes.md"}, ConversationView{})
	if reason == "" {
		t.Fatal("writing over an unread existing file must be refused")
	}
	if !strings.Contains(reason, "read_file") {
		t.Errorf("the refusal must say what to do instead: %q", reason)
	}
	// The file is untouched: a guard refuses, it never writes.
	body, _ := os.ReadFile(filepath.Join(root, "notes.md"))
	if string(body) != "work nobody wants to lose" {
		t.Error("the guard must not modify anything")
	}
}

// Creating a NEW file is the ordinary case and must stay frictionless - there is
// nothing to lose.
func TestWriteToANewFileIsAllowed(t *testing.T) {
	l, _ := obsLoop(t)
	if r := l.GuardWriteNeedsRead("write_file", map[string]any{"path": "brand-new.md"}, ConversationView{}); r != "" {
		t.Errorf("creating a file destroys nothing and must be allowed: %s", r)
	}
}

// Read it, then write it: the normal edit, allowed.
func TestWriteAfterReadingIsAllowed(t *testing.T) {
	l, root := obsLoop(t)
	os.WriteFile(filepath.Join(root, "a.md"), []byte("one"), 0o600)
	l.noteObserved("read_file", map[string]any{"path": "a.md"})
	if r := l.GuardWriteNeedsRead("write_file", map[string]any{"path": "a.md"}, ConversationView{}); r != "" {
		t.Errorf("a write after a read must be allowed: %s", r)
	}
}

// THE STALE OVERWRITE. The model reads, the OPERATOR edits in their editor while the
// turn thinks, the model writes back what it planned from the old text.
func TestWriteIsRefusedAfterTheFileChangedUnderneath(t *testing.T) {
	l, root := obsLoop(t)
	p := filepath.Join(root, "a.md")
	os.WriteFile(p, []byte("original"), 0o600)
	l.noteObserved("read_file", map[string]any{"path": "a.md"})

	// Someone else edits it. mtime must actually move for the version to differ.
	time.Sleep(10 * time.Millisecond)
	os.WriteFile(p, []byte("the operator's own edit, much longer than before"), 0o600)

	reason := l.GuardWriteNeedsRead("write_file", map[string]any{"path": "a.md"}, ConversationView{})
	if reason == "" {
		t.Fatal("a write over a file that changed since the read must be refused")
	}
	if !strings.Contains(reason, "changed") || !strings.Contains(reason, "again") {
		t.Errorf("the refusal must name the conflict and the way forward: %q", reason)
	}
	if body, _ := os.ReadFile(p); !strings.Contains(string(body), "operator's own edit") {
		t.Error("the operator's edit must survive")
	}
}

// The agent's OWN write updates what it has observed, so a second write in the same
// turn is not refused for a change it made itself.
func TestAgentsOwnWriteDoesNotBlockItsNextOne(t *testing.T) {
	l, root := obsLoop(t)
	p := filepath.Join(root, "a.md")
	os.WriteFile(p, []byte("v1"), 0o600)
	l.noteObserved("read_file", map[string]any{"path": "a.md"})

	time.Sleep(10 * time.Millisecond)
	os.WriteFile(p, []byte("v2 written by the agent"), 0o600)
	l.noteWritten("write_file", map[string]any{"path": "a.md"})

	if r := l.GuardWriteNeedsRead("write_file", map[string]any{"path": "a.md"}, ConversationView{}); r != "" {
		t.Errorf("an agent must not be blocked by its own edit: %s", r)
	}
}

// A file that was read and has since been DELETED can be written: re-creating it
// destroys nothing.
func TestWriteToADeletedFileIsAllowed(t *testing.T) {
	l, root := obsLoop(t)
	p := filepath.Join(root, "gone.md")
	os.WriteFile(p, []byte("x"), 0o600)
	l.noteObserved("read_file", map[string]any{"path": "gone.md"})
	os.Remove(p)
	if r := l.GuardWriteNeedsRead("write_file", map[string]any{"path": "gone.md"}, ConversationView{}); r != "" {
		t.Errorf("re-creating a deleted file destroys nothing: %s", r)
	}
}

// A SUBAGENT's read must not license the PARENT's write: the parent has not seen the
// file, which is exactly the situation the blind-overwrite rule exists for.
func TestASubagentsReadDoesNotLicenseTheParentsWrite(t *testing.T) {
	l, root := obsLoop(t)
	os.WriteFile(filepath.Join(root, "a.md"), []byte("content"), 0o600)
	child := l.newSubagent(root)
	child.noteObserved("read_file", map[string]any{"path": "a.md"})

	if r := l.GuardWriteNeedsRead("write_file", map[string]any{"path": "a.md"}, ConversationView{}); r == "" {
		t.Error("the parent has not read this file and must still be refused")
	}
}

// Observations are recorded only for calls that actually SUCCEEDED - a refused or
// failed read must never license a write.
func TestOnlySuccessfulReadsAreObserved(t *testing.T) {
	l, root := obsLoop(t)
	os.WriteFile(filepath.Join(root, "a.md"), []byte("content"), 0o600)
	// A read of a DIFFERENT file does not license this one.
	l.noteObserved("read_file", map[string]any{"path": "other.md"})
	if r := l.GuardWriteNeedsRead("write_file", map[string]any{"path": "a.md"}, ConversationView{}); r == "" {
		t.Error("reading one file must not license writing another")
	}
	// Neither does a non-read tool touching the path.
	l.noteObserved("list_dir", map[string]any{"path": "a.md"})
	if r := l.GuardWriteNeedsRead("write_file", map[string]any{"path": "a.md"}, ConversationView{}); r == "" {
		t.Error("only a read observes a file's contents")
	}
}

// The guard is scoped to write_file and stays out of everything else's way.
func TestWriteGuardIgnoresOtherTools(t *testing.T) {
	l, _ := obsLoop(t)
	for _, tool := range []string{"read_file", "run_shell", "web_fetch", "delegate"} {
		if r := l.GuardWriteNeedsRead(tool, map[string]any{"path": "a.md"}, ConversationView{}); r != "" {
			t.Errorf("%s must be unaffected: %s", tool, r)
		}
	}
}
