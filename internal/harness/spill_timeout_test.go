package harness

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ── SPILL ────────────────────────────────────────────────────────────────────
// Our sizing was already better than theirs; our disposal was worse. We cut at the
// budget and threw the rest away, so a model told "(truncated)" had no way to ask for
// the part it needed.

func TestOversizedResultIsSavedNotDiscarded(t *testing.T) {
	root := t.TempDir()
	l := NewLoop(root, "sys", nil, nil)
	l.MaxToolOutput = 500
	full := strings.Repeat("payload line\n", 400)

	got := l.clipOrSpill("web_fetch", full)
	if len(got) > 500 {
		t.Errorf("the model-facing result must still fit the budget, got %d bytes", len(got))
	}
	if !strings.Contains(got, "read_file") {
		t.Errorf("the notice must name the one move that gets the rest: %q", got)
	}
	// The path it names must exist, be inside the root, and hold EVERY byte.
	var path string
	for _, f := range strings.Fields(got) {
		if strings.Contains(f, spillDirName) {
			path = f
		}
	}
	if path == "" {
		t.Fatalf("the notice must name a path: %q", got)
	}
	body, err := os.ReadFile(filepath.Join(root, path))
	if err != nil {
		t.Fatalf("the spilled file must exist where the model was told: %v", err)
	}
	if string(body) != full {
		t.Errorf("the spill must be lossless: %d bytes saved, %d produced", len(body), len(full))
	}
}

// read_file is EXEMPT: spilling a read hands the model a path, and the obvious next
// move for a model holding a path is to read it - which spills again.
func TestReadFileIsNeverSpilled(t *testing.T) {
	l := NewLoop(t.TempDir(), "sys", nil, nil)
	l.MaxToolOutput = 300
	got := l.clipOrSpill("read_file", strings.Repeat("x", 5000))
	if strings.Contains(got, spillDirName) {
		t.Error("spilling read_file invites a read -> spill -> read loop")
	}
	if !strings.Contains(got, "truncated") {
		t.Errorf("it should fall back to plain truncation: %q", got)
	}
}

// A SPILL FAILURE MUST NEVER TURN A GOOD RESULT INTO AN ERROR. The call succeeded; the
// model just gets the old notice instead of a path.
func TestSpillFailureDegradesToTruncation(t *testing.T) {
	l := NewLoop(t.TempDir(), "sys", nil, nil)
	l.MaxToolOutput = 300
	l.spill = newSpillStore("") // no root: saving is unavailable
	got := l.clipOrSpill("web_fetch", strings.Repeat("y", 5000))
	if got == "" {
		t.Fatal("a failed spill must not lose the result")
	}
	if !strings.Contains(got, "truncated") {
		t.Errorf("it must fall back cleanly: %q", got)
	}
}

// /clear throws the conversation away, and the spilled files belong to it.
func TestResetCleansUpSpilledFiles(t *testing.T) {
	root := t.TempDir()
	l := NewLoop(root, "sys", nil, nil)
	l.MaxToolOutput = 300
	l.clipOrSpill("web_fetch", strings.Repeat("z", 5000))
	if l.spill.dir == "" {
		t.Fatal("precondition: something should have been spilled")
	}
	dir := l.spill.dir
	l.Reset()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Error("a cleared session must not leave its tool output in someone's project")
	}
}

// A result that FITS is untouched - no notice, no file, no directory.
func TestResultWithinBudgetIsUntouched(t *testing.T) {
	root := t.TempDir()
	l := NewLoop(root, "sys", nil, nil)
	l.MaxToolOutput = 5000
	in := "a small result"
	if got := l.clipOrSpill("web_fetch", in); got != in {
		t.Errorf("a result inside the budget must pass through: %q", got)
	}
	if _, err := os.Stat(filepath.Join(root, spillDirName)); !os.IsNotExist(err) {
		t.Error("nothing spilled means no spill directory")
	}
}

// ── PER-TOOL TIMEOUTS ────────────────────────────────────────────────────────
// A tool that hangs hangs the TURN: the loop waits on Run, the working line never
// settles, and esc is the only way out.

func TestHungToolFailsItsCallInsteadOfTheTurn(t *testing.T) {
	hang := Tool{
		Name: "hang", Description: "hang", Timeout: 40 * time.Millisecond,
		Params: map[string]any{"type": "object"},
		Run: func(ctx context.Context, root string, args map[string]any) (string, error) {
			<-ctx.Done() // a well-behaved tool returns when its context is cancelled
			return "", ctx.Err()
		},
	}
	var c ToolCall
	c.ID = "x"
	c.Function.Name = "hang"
	c.Function.Arguments = "{}"
	p := plannedCall{call: c, tool: hang, args: map[string]any{}}

	start := time.Now()
	_, err := runWithTimeout(context.Background(), p)
	if err == nil {
		t.Fatal("a hung call must fail")
	}
	if took := time.Since(start); took > time.Second {
		t.Errorf("the deadline must actually fire, took %s", took)
	}
	// The message must be actionable, not just "error".
	for _, want := range []string{"hang", "longer than"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the timeout must say what timed out and that it did: %q", err)
		}
	}
}

// An interrupted turn (esc) must keep saying CANCELLED. Reporting a timeout that never
// happened would send the operator looking for a slow tool instead of their own esc.
func TestCancellationIsNotReportedAsATimeout(t *testing.T) {
	slow := Tool{
		Name: "slow", Description: "slow", Timeout: 5 * time.Second,
		Params: map[string]any{"type": "object"},
		Run: func(ctx context.Context, root string, args map[string]any) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		},
	}
	var c ToolCall
	c.ID = "x"
	c.Function.Name = "slow"
	p := plannedCall{call: c, tool: slow, args: map[string]any{}}

	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(30 * time.Millisecond); cancel() }()
	_, err := runWithTimeout(ctx, p)
	if err == nil {
		t.Fatal("a cancelled call must report something")
	}
	if strings.Contains(err.Error(), "longer than") {
		t.Errorf("an esc must not be dressed up as a timeout: %q", err)
	}
}

// A tool with no declared deadline is unchanged.
func TestToolWithoutTimeoutIsUnbounded(t *testing.T) {
	quick := Tool{
		Name: "quick", Description: "quick", Params: map[string]any{"type": "object"},
		Run: func(ctx context.Context, root string, args map[string]any) (string, error) {
			if _, ok := ctx.Deadline(); ok {
				t.Error("a tool that declared no Timeout must not be given one")
			}
			return "done", nil
		},
	}
	var c ToolCall
	c.Function.Name = "quick"
	out, err := runWithTimeout(context.Background(), plannedCall{call: c, tool: quick, args: map[string]any{}})
	if err != nil || out != "done" {
		t.Errorf("out=%q err=%v", out, err)
	}
}

// Every shipped tool declares a bound, or the whole mechanism has a hole in it.
func TestEveryBuiltinToolDeclaresADeadline(t *testing.T) {
	for _, tl := range BuiltinTools() {
		if tl.Timeout <= 0 {
			t.Errorf("%s has no Timeout - a hang in it hangs the turn", tl.Name)
		}
	}
}

// A SPILL MUST NOT LITTER SOMEONE'S REPO. The files have to live under the workspace
// root - that is the sandbox read_file is bounded by - and the workspace root is
// usually a git repo, so without care `.roger-spill/` turns up in the operator's
// `git status`. A .gitignore containing "*" inside the directory hides its whole
// contents including itself.
func TestSpillIsInvisibleToGit(t *testing.T) {
	root := t.TempDir()
	l := NewLoop(root, "sys", nil, nil)
	l.MaxToolOutput = 300
	l.clipOrSpill("web_fetch", strings.Repeat("q", 5000))

	ignore := filepath.Join(root, spillDirName, ".gitignore")
	body, err := os.ReadFile(ignore)
	if err != nil {
		t.Fatalf("the spill directory must ignore itself: %v", err)
	}
	if strings.TrimSpace(string(body)) != "*" {
		t.Errorf("the ignore must cover everything including itself, got %q", body)
	}
}

// A finished session leaves NOTHING behind - not even an empty marker directory.
func TestSpillCleanupRemovesTheParentToo(t *testing.T) {
	root := t.TempDir()
	l := NewLoop(root, "sys", nil, nil)
	l.MaxToolOutput = 300
	l.clipOrSpill("web_fetch", strings.Repeat("q", 5000))
	l.Close()
	if _, err := os.Stat(filepath.Join(root, spillDirName)); !os.IsNotExist(err) {
		t.Error("a finished session must not leave an empty spill directory in the project")
	}
}

// ...but it must not take a CONCURRENT session's spill with it, and must leave that
// session's ignore file in place or its files become visible to git.
func TestSpillCleanupSparesAnotherSession(t *testing.T) {
	root := t.TempDir()
	a := NewLoop(root, "sys", nil, nil)
	b := NewLoop(root, "sys", nil, nil)
	a.MaxToolOutput, b.MaxToolOutput = 300, 300
	a.clipOrSpill("web_fetch", strings.Repeat("a", 5000))
	b.clipOrSpill("web_fetch", strings.Repeat("b", 5000))

	a.Close()
	if _, err := os.Stat(b.spill.dir); err != nil {
		t.Errorf("one session's cleanup must not delete another's spill: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, spillDirName, ".gitignore")); err != nil {
		t.Error("the surviving session's spill must stay invisible to git")
	}
}
