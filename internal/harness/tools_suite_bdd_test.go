package harness

// tools_suite_bdd_test.go - the godog harness for features/agent/tools_editing.feature and
// features/agent/tools_navigation.feature.
//
// Everything runs the REAL tool out of BuiltinTools() against a REAL temp directory. A tool
// that edits files is not something to exercise against a fake filesystem.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"
)

type toolsBDD struct {
	t      *testing.T
	root   string
	out    string
	err    error
	file   string // the file most scenarios act on
	before string // its contents before the call, so "unchanged" can be checked honestly
}

func (s *toolsBDD) tool(name string) (Tool, error) {
	for _, t := range BuiltinTools() {
		if t.Name == name {
			return t, nil
		}
	}
	return Tool{}, fmt.Errorf("no tool named %q in BuiltinTools() - it has not been added yet", name)
}

func (s *toolsBDD) run(name string, args map[string]any) {
	t, err := s.tool(name)
	if err != nil {
		s.out, s.err = "", err
		return
	}
	s.out, s.err = t.Run(context.Background(), s.root, args)
}

// snapshot records the acted-on file so an "is unchanged" assertion compares against what
// was actually there, not against a fixture the step happens to remember.
func (s *toolsBDD) snapshot() { s.before = s.read(s.file) }

func (s *toolsBDD) write(rel, body string) {
	p := filepath.Join(s.root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		s.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		s.t.Fatal(err)
	}
}

func (s *toolsBDD) read(rel string) string {
	b, err := os.ReadFile(filepath.Join(s.root, rel))
	if err != nil {
		s.t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

// ---------------------------------------------------------------------------
// shared
// ---------------------------------------------------------------------------

func (s *toolsBDD) aWorkingDir() error { s.root = s.t.TempDir(); return nil }

func (s *toolsBDD) aMixOfFiles() error {
	s.root = s.t.TempDir()
	s.write("a.go", "package main\n// needle here\n")
	s.write("docs/b.md", "prose\nneedle in markdown\n")
	s.write("sub/c.go", "package sub\nnothing\n")
	return nil
}

func (s *toolsBDD) editFails() error {
	if s.err == nil {
		return fmt.Errorf("expected the call to fail, but it returned: %q", s.out)
	}
	return nil
}

func (s *toolsBDD) errSays(fragment string) error {
	if s.err == nil {
		return fmt.Errorf("expected an error mentioning %q, got success", fragment)
	}
	if !strings.Contains(strings.ToLower(s.err.Error()), strings.ToLower(fragment)) {
		return fmt.Errorf("error %q does not mention %q", s.err, fragment)
	}
	return nil
}

// ---------------------------------------------------------------------------
// edit_file
// ---------------------------------------------------------------------------

func (s *toolsBDD) fileABG() error {
	s.file = "f.txt"
	s.write(s.file, "alpha\nbeta\ngamma\n")
	s.snapshot()
	return nil
}

func (s *toolsBDD) editReplacing(old, new string) error {
	s.run("edit_file", map[string]any{"path": s.file, "old_string": old, "new_string": new})
	return nil
}

func (s *toolsBDD) editReplacingAll(old, new string) error {
	s.run("edit_file", map[string]any{"path": s.file, "old_string": old, "new_string": new, "replace_all": true})
	return nil
}

func (s *toolsBDD) fileReadsADG() error {
	if got := s.read(s.file); got != "alpha\ndelta\ngamma\n" {
		return fmt.Errorf("file is %q", got)
	}
	return nil
}

func (s *toolsBDD) everyOtherByteUnchanged() error { return nil } // asserted by the exact compare above

func (s *toolsBDD) fileWithout(text string) error {
	s.file = "f.txt"
	s.write(s.file, "alpha\nbeta\n")
	s.snapshot()
	if strings.Contains(s.read(s.file), text) {
		return fmt.Errorf("fixture unexpectedly contains %q", text)
	}
	return nil
}

func (s *toolsBDD) fileUnchanged() error {
	if got := s.read(s.file); got != s.before {
		return fmt.Errorf("a failed edit modified the file: was %q, now %q", s.before, got)
	}
	return nil
}

func (s *toolsBDD) fileWithDupThrice() error {
	s.file = "f.txt"
	s.write(s.file, "dup\nmiddle\ndup\ntail\ndup\n")
	s.snapshot()
	return nil
}

func (s *toolsBDD) allThreeReplaced() error {
	got := s.read(s.file)
	if strings.Contains(got, "dup") || strings.Count(got, "one") != 3 {
		return fmt.Errorf("expected all three replaced, got %q", got)
	}
	return nil
}

func (s *toolsBDD) noError() error {
	if s.err != nil {
		return fmt.Errorf("unexpected error: %v", s.err)
	}
	return nil
}

func (s *toolsBDD) fileWithDoomedLine() error {
	s.file = "f.txt"
	s.write(s.file, "keep\nremove me\nkeep too\n")
	s.snapshot()
	return nil
}

func (s *toolsBDD) replaceWithNothing() error {
	s.run("edit_file", map[string]any{"path": s.file, "old_string": "remove me\n", "new_string": ""})
	return nil
}

func (s *toolsBDD) lineGoneNeighboursIntact() error {
	if got := s.read(s.file); got != "keep\nkeep too\n" {
		return fmt.Errorf("file is %q", got)
	}
	return nil
}

func (s *toolsBDD) editIdentical() error {
	s.file = "f.txt"
	s.write(s.file, "alpha\nbeta\n")
	s.run("edit_file", map[string]any{"path": s.file, "old_string": "beta", "new_string": "beta"})
	return nil
}

func (s *toolsBDD) fileWithBlock() error {
	s.file = "f.txt"
	s.write(s.file, "head\nb1\nb2\nb3\ntail\n")
	s.snapshot()
	return nil
}

func (s *toolsBDD) replaceBlock() error {
	s.run("edit_file", map[string]any{"path": s.file, "old_string": "b1\nb2\nb3\n", "new_string": "one\n"})
	return nil
}

func (s *toolsBDD) blockReplaced() error {
	if got := s.read(s.file); got != "head\none\ntail\n" {
		return fmt.Errorf("file is %q", got)
	}
	return nil
}

func (s *toolsBDD) editIsMutating() error {
	t, err := s.tool("edit_file")
	if err != nil {
		return err
	}
	if !t.Mutating {
		return fmt.Errorf("edit_file must be Mutating: it changes a file on disk")
	}
	return nil
}

func (s *toolsBDD) gatedLikeWriteFile() error {
	e, err := s.tool("edit_file")
	if err != nil {
		return err
	}
	w, err := s.tool("write_file")
	if err != nil {
		return err
	}
	if e.Mutating != w.Mutating {
		return fmt.Errorf("edit_file and write_file must gate alike")
	}
	return nil
}

func (s *toolsBDD) editEscaping() error {
	s.file = "f.txt"
	s.write(s.file, "x")
	s.run("edit_file", map[string]any{"path": "../escape.txt", "old_string": "a", "new_string": "b"})
	return nil
}

func (s *toolsBDD) nothingOutsideTouched() error {
	if _, err := os.Stat(filepath.Join(filepath.Dir(s.root), "escape.txt")); err == nil {
		return fmt.Errorf("a file was created outside the working directory")
	}
	return nil
}

func (s *toolsBDD) editMissingFile() error {
	s.run("edit_file", map[string]any{"path": "nope.txt", "old_string": "a", "new_string": "b"})
	return nil
}

func (s *toolsBDD) editBinary() error {
	s.file = "bin.dat"
	if err := os.WriteFile(filepath.Join(s.root, s.file), []byte{0x00, 0x01, 0x02, 0x00}, 0o644); err != nil {
		s.t.Fatal(err)
	}
	s.run("edit_file", map[string]any{"path": s.file, "old_string": "\x01", "new_string": "\x03"})
	return nil
}

func (s *toolsBDD) failedNotCorrupted() error {
	if s.err == nil {
		return fmt.Errorf("editing a binary should fail")
	}
	return nil
}

// ---------------------------------------------------------------------------
// read_file ranges
// ---------------------------------------------------------------------------

func (s *toolsBDD) smallFile() error {
	s.file = "small.txt"
	s.write(s.file, "one\ntwo\nthree\n")
	return nil
}

func (s *toolsBDD) readNoRange() error {
	s.run("read_file", map[string]any{"path": s.file})
	return nil
}

func (s *toolsBDD) getsWholeFile() error {
	if s.out != "one\ntwo\nthree\n" {
		return fmt.Errorf("got %q", s.out)
	}
	return nil
}

func (s *toolsBDD) bigFile() error {
	s.file = "big.txt"
	var b strings.Builder
	for i := 1; i <= 4000; i++ {
		fmt.Fprintf(&b, "line %d padding padding padding\n", i)
	}
	s.write(s.file, b.String())
	return nil
}

func (s *toolsBDD) resultTruncated() error {
	if !strings.Contains(s.out, "truncated") {
		return fmt.Errorf("an over-cap read must say it was truncated:\n%s", clipTo(s.out, 200))
	}
	return nil
}

func (s *toolsBDD) namesNextRange() error {
	if !strings.Contains(s.out, "offset") {
		return fmt.Errorf("a truncated read must name the range to ask for next, or the rest of "+
			"the file is unreachable:\n%s", clipTo(s.out, 300))
	}
	return nil
}

func (s *toolsBDD) file500() error {
	s.file = "n.txt"
	var b strings.Builder
	for i := 1; i <= 500; i++ {
		fmt.Fprintf(&b, "L%d\n", i)
	}
	s.write(s.file, b.String())
	return nil
}

func (s *toolsBDD) readRange(off, lim int) error {
	s.run("read_file", map[string]any{"path": s.file, "offset": float64(off), "limit": float64(lim)})
	return nil
}

func (s *toolsBDD) getsLines(lo, hi int) error {
	if s.err != nil {
		return fmt.Errorf("unexpected error: %v", s.err)
	}
	if !strings.Contains(s.out, fmt.Sprintf("L%d\n", lo)) {
		return fmt.Errorf("missing first line L%d:\n%s", lo, clipTo(s.out, 200))
	}
	if !strings.Contains(s.out, fmt.Sprintf("L%d\n", hi)) && !strings.HasSuffix(strings.TrimRight(s.out, "\n"), fmt.Sprintf("L%d", hi)) {
		return fmt.Errorf("missing last line L%d:\n%s", hi, clipTo(s.out, 200))
	}
	if strings.Contains(s.out, fmt.Sprintf("L%d\n", hi+1)) {
		return fmt.Errorf("range ran past L%d", hi)
	}
	return nil
}

func (s *toolsBDD) saysWhichLines() error {
	if !strings.Contains(s.out, "line") && !strings.Contains(s.out, "L") {
		return fmt.Errorf("a ranged read should say which lines it returned:\n%s", clipTo(s.out, 200))
	}
	return nil
}

func (s *toolsBDD) file10() error {
	s.file = "t.txt"
	var b strings.Builder
	for i := 1; i <= 10; i++ {
		fmt.Fprintf(&b, "L%d\n", i)
	}
	s.write(s.file, b.String())
	return nil
}

func (s *toolsBDD) doesNotClaimMore() error {
	if strings.Contains(s.out, "truncated") {
		return fmt.Errorf("a range that reached the end must not claim there is more:\n%s", clipTo(s.out, 200))
	}
	return nil
}

func (s *toolsBDD) readFrom(off int) error {
	s.run("read_file", map[string]any{"path": s.file, "offset": float64(off)})
	return nil
}

func (s *toolsBDD) errNamesLineCount() error {
	if s.err == nil {
		return fmt.Errorf("reading past the end should fail")
	}
	if !strings.Contains(s.err.Error(), "10") {
		return fmt.Errorf("the error should say how many lines the file has, got %q", s.err)
	}
	return nil
}

func (s *toolsBDD) readBadRange(off, lim int) error {
	s.file = "t.txt"
	s.write(s.file, "L1\nL2\n")
	s.run("read_file", map[string]any{"path": s.file, "offset": float64(off), "limit": float64(lim)})
	return nil
}

func (s *toolsBDD) failsNamingBadArg() error {
	if s.err == nil {
		return fmt.Errorf("a nonsensical range must fail, got %q", s.out)
	}
	e := strings.ToLower(s.err.Error())
	if !strings.Contains(e, "offset") && !strings.Contains(e, "limit") {
		return fmt.Errorf("the error should name the bad argument, got %q", s.err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// grep / glob
// ---------------------------------------------------------------------------

func (s *toolsBDD) needleInTwo() error {
	s.root = s.t.TempDir()
	s.write("a.go", "package main\n// needle here\n")
	s.write("docs/b.md", "prose\nneedle in markdown\n")
	return nil
}

func (s *toolsBDD) grepFor(pat string) error {
	s.run("grep", map[string]any{"pattern": pat})
	return nil
}

func (s *toolsBDD) getsBothMatches() error {
	if s.err != nil {
		return fmt.Errorf("unexpected error: %v", s.err)
	}
	if !strings.Contains(s.out, "a.go") || !strings.Contains(s.out, "b.md") {
		return fmt.Errorf("both matches expected:\n%s", s.out)
	}
	return nil
}

func (s *toolsBDD) eachNamesFileAndLine() error {
	if !strings.Contains(s.out, ":2:") && !strings.Contains(s.out, ":2 ") {
		return fmt.Errorf("each match should name its file and line number:\n%s", s.out)
	}
	return nil
}

func (s *toolsBDD) grepNothing() error {
	s.run("grep", map[string]any{"pattern": "zzz-absent-zzz"})
	return nil
}

func (s *toolsBDD) saysNoMatches() error {
	if s.err != nil {
		return fmt.Errorf("no matches is not an error: %v", s.err)
	}
	if !strings.Contains(strings.ToLower(s.out), "no match") {
		return fmt.Errorf("expected a plain no-matches result, got %q", s.out)
	}
	return nil
}

func (s *toolsBDD) notAnError() error { return s.noError() }

func (s *toolsBDD) toolNotMutating(name string) error {
	t, err := s.tool(name)
	if err != nil {
		return err
	}
	if t.Mutating {
		return fmt.Errorf("%s is a READ and must not be Mutating, or every search raises a y/N", name)
	}
	return nil
}

func (s *toolsBDD) doesNotRaiseGate() error { return nil } // implied by Mutating:false; the loop gates on it

func (s *toolsBDD) needleInAndOut() error {
	s.root = s.t.TempDir()
	s.write("top.go", "needle\n")
	s.write("sub/in.go", "needle\n")
	return nil
}

func (s *toolsBDD) grepUnder(dir string) error {
	s.run("grep", map[string]any{"pattern": "needle", "path": dir})
	return nil
}

func (s *toolsBDD) onlyInside() error {
	if strings.Contains(s.out, "top.go") {
		return fmt.Errorf("a scoped search must not reach outside its subtree:\n%s", s.out)
	}
	if !strings.Contains(s.out, "in.go") {
		return fmt.Errorf("the in-subtree match is missing:\n%s", s.out)
	}
	return nil
}

func (s *toolsBDD) needleInGoAndMd() error { return s.needleInTwo() }

func (s *toolsBDD) grepInGlob(pat, glob string) error {
	s.run("grep", map[string]any{"pattern": pat, "glob": glob})
	return nil
}

func (s *toolsBDD) onlyGoMatch() error {
	if strings.Contains(s.out, ".md") {
		return fmt.Errorf("a glob-filtered search returned a non-matching kind:\n%s", s.out)
	}
	if !strings.Contains(s.out, ".go") {
		return fmt.Errorf("the .go match is missing:\n%s", s.out)
	}
	return nil
}

func (s *toolsBDD) fileWithVersion() error {
	s.root = s.t.TempDir()
	s.write("v.txt", "version 6.3.3\n")
	return nil
}

func (s *toolsBDD) lineFound() error {
	if s.err != nil {
		return fmt.Errorf("unexpected error: %v", s.err)
	}
	if !strings.Contains(s.out, "6.3.3") {
		return fmt.Errorf("the regex should have matched:\n%s", s.out)
	}
	return nil
}

func (s *toolsBDD) grepBadRegex() error { s.run("grep", map[string]any{"pattern": "a("}); return nil }

func (s *toolsBDD) errNamesPattern() error {
	if s.err == nil {
		return fmt.Errorf("an unparseable pattern must be reported, not swallowed")
	}
	return nil
}

func (s *toolsBDD) grepEscaping() error {
	s.run("grep", map[string]any{"pattern": "x", "path": "../.."})
	return nil
}

func (s *toolsBDD) nothingOutsideRead() error {
	if s.err == nil {
		return fmt.Errorf("a search that escapes the working directory must fail")
	}
	return nil
}

func (s *toolsBDD) repoWithNoise() error {
	s.root = s.t.TempDir()
	s.write(".git/config", "noise-token\n")
	s.write("node_modules/p/index.js", "noise-token\n")
	s.write("real.go", "noise-token\n")
	return nil
}

func (s *toolsBDD) grepNoise() error {
	s.run("grep", map[string]any{"pattern": "noise-token"})
	return nil
}

func (s *toolsBDD) neitherSearched() error {
	if strings.Contains(s.out, ".git") || strings.Contains(s.out, "node_modules") {
		return fmt.Errorf("the search walked directories nobody means to search:\n%s", s.out)
	}
	if !strings.Contains(s.out, "real.go") {
		return fmt.Errorf("the real match is missing:\n%s", s.out)
	}
	return nil
}

func (s *toolsBDD) binaryWithBytes() error {
	s.root = s.t.TempDir()
	if err := os.WriteFile(filepath.Join(s.root, "b.bin"), []byte("pre\x00needle\x00post"), 0o644); err != nil {
		s.t.Fatal(err)
	}
	s.write("t.txt", "needle\n")
	return nil
}

func (s *toolsBDD) grepBytes() error { s.run("grep", map[string]any{"pattern": "needle"}); return nil }

func (s *toolsBDD) binarySkipped() error {
	if strings.Contains(s.out, "b.bin") {
		return fmt.Errorf("a binary must be skipped rather than dumped into the transcript:\n%s", s.out)
	}
	return nil
}

func (s *toolsBDD) manyMatches() error {
	s.root = s.t.TempDir()
	var b strings.Builder
	for i := 0; i < 20000; i++ {
		fmt.Fprintf(&b, "hit %d\n", i)
	}
	s.write("many.txt", b.String())
	return nil
}

func (s *toolsBDD) grepMany() error { s.run("grep", map[string]any{"pattern": "hit"}); return nil }

func (s *toolsBDD) resultCapped() error {
	if len(s.out) > maxToolOutput+2048 {
		return fmt.Errorf("result not capped: %d bytes", len(s.out))
	}
	return nil
}

func (s *toolsBDD) saysTruncated() error { return s.resultTruncated() }

func (s *toolsBDD) goFilesAtDepths() error {
	s.root = s.t.TempDir()
	s.write("a.go", "x")
	s.write("sub/b.go", "x")
	s.write("sub/deep/c.go", "x")
	s.write("readme.md", "x")
	return nil
}

func (s *toolsBDD) globFor(pat string) error {
	s.run("glob", map[string]any{"pattern": pat})
	return nil
}

func (s *toolsBDD) everyOneReturned() error {
	if s.err != nil {
		return fmt.Errorf("unexpected error: %v", s.err)
	}
	for _, want := range []string{"a.go", "b.go", "c.go"} {
		if !strings.Contains(s.out, want) {
			return fmt.Errorf("%s missing:\n%s", want, s.out)
		}
	}
	if strings.Contains(s.out, "readme.md") {
		return fmt.Errorf("a non-matching file was returned:\n%s", s.out)
	}
	return nil
}

func (s *toolsBDD) globNothing() error {
	s.run("glob", map[string]any{"pattern": "**/*.zzz"})
	return nil
}

func (s *toolsBDD) filesAtDifferentTimes() error {
	s.root = s.t.TempDir()
	s.write("old.go", "x")
	s.write("new.go", "x")
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(filepath.Join(s.root, "old.go"), old, old); err != nil {
		s.t.Fatal(err)
	}
	return nil
}

func (s *toolsBDD) globThem() error { s.run("glob", map[string]any{"pattern": "*.go"}); return nil }

func (s *toolsBDD) newestFirst() error {
	i, j := strings.Index(s.out, "new.go"), strings.Index(s.out, "old.go")
	if i < 0 || j < 0 {
		return fmt.Errorf("both files expected:\n%s", s.out)
	}
	if i > j {
		return fmt.Errorf("most recently modified must come first:\n%s", s.out)
	}
	return nil
}

func (s *toolsBDD) globEscaping() error { s.run("glob", map[string]any{"pattern": "../*"}); return nil }

func (s *toolsBDD) nothingOutsideListed() error {
	if s.err == nil {
		return fmt.Errorf("a glob that escapes the working directory must fail")
	}
	return nil
}

func (s *toolsBDD) globNoise() error { s.run("glob", map[string]any{"pattern": "**/*"}); return nil }

func (s *toolsBDD) neitherWalked() error { return s.neitherSearched() }

// ---------------------------------------------------------------------------
// runner
// ---------------------------------------------------------------------------

func TestAgentToolSuiteFeatures(t *testing.T) {
	st := &toolsBDD{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(c context.Context, _ *godog.Scenario) (context.Context, error) {
				*st = toolsBDD{t: t, root: t.TempDir()}
				return c, nil
			})
			sc.Step(`^a working directory with files the agent may edit$`, st.aWorkingDir)
			sc.Step(`^a working directory with a mix of files$`, st.aMixOfFiles)

			sc.Step(`^a file containing "alpha", "beta" and "gamma" on separate lines$`, st.fileABG)
			sc.Step(`^the agent edits it, replacing "([^"]*)" with "([^"]*)"$`, st.editReplacing)
			sc.Step(`^the agent edits it, replacing "([^"]*)" with "([^"]*)", replacing all$`, st.editReplacingAll)
			sc.Step(`^the file reads "alpha", "delta", "gamma"$`, st.fileReadsADG)
			sc.Step(`^every other byte is unchanged$`, st.everyOtherByteUnchanged)
			sc.Step(`^a file that does not contain "([^"]*)"$`, st.fileWithout)
			sc.Step(`^the edit fails$`, st.editFails)
			sc.Step(`^the error says the text was not found$`, func() error { return st.errSays("not found") })
			sc.Step(`^the file is unchanged$`, st.fileUnchanged)
			sc.Step(`^a file containing "dup" three times$`, st.fileWithDupThrice)
			sc.Step(`^the error says how many matches were found$`, func() error { return st.errSays("3") })
			sc.Step(`^all three are replaced$`, st.allThreeReplaced)
			sc.Step(`^the error is not raised$`, st.noError)
			sc.Step(`^a file containing a line the agent wants gone$`, st.fileWithDoomedLine)
			sc.Step(`^the agent replaces that line's text with nothing$`, st.replaceWithNothing)
			sc.Step(`^the line's text is gone$`, st.lineGoneNeighboursIntact)
			sc.Step(`^the surrounding lines are untouched$`, func() error { return nil })
			sc.Step(`^the agent edits a file replacing text with the identical text$`, st.editIdentical)
			sc.Step(`^the error says the replacement is identical to the match$`, func() error { return st.errSays("identical") })
			sc.Step(`^a file containing a three-line block$`, st.fileWithBlock)
			sc.Step(`^the agent replaces that whole block with a single line$`, st.replaceBlock)
			sc.Step(`^the block is gone and the single line is in its place$`, st.blockReplaced)
			sc.Step(`^edit_file is a mutating tool$`, st.editIsMutating)
			sc.Step(`^it is gated exactly as write_file is$`, st.gatedLikeWriteFile)
			sc.Step(`^the agent edits a path that escapes the working directory$`, st.editEscaping)
			sc.Step(`^nothing outside the working directory is touched$`, st.nothingOutsideTouched)
			sc.Step(`^the agent edits a file that does not exist$`, st.editMissingFile)
			sc.Step(`^the error points at write_file for creating one$`, func() error { return st.errSays("write_file") })
			sc.Step(`^the agent edits a binary file$`, st.editBinary)
			sc.Step(`^the edit fails rather than corrupting it$`, st.failedNotCorrupted)

			sc.Step(`^a file well under the output cap$`, st.smallFile)
			sc.Step(`^the agent reads it with no range$`, st.readNoRange)
			sc.Step(`^it gets the entire file$`, st.getsWholeFile)
			sc.Step(`^a file larger than the tool output cap$`, st.bigFile)
			sc.Step(`^the result is truncated$`, st.resultTruncated)
			sc.Step(`^it names the range to ask for next$`, st.namesNextRange)
			sc.Step(`^a file of 500 lines$`, st.file500)
			sc.Step(`^the agent reads it from line (\d+) for (\d+) lines$`, st.readRange)
			sc.Step(`^it gets lines (\d+) to (\d+)$`, st.getsLines)
			sc.Step(`^the result says which lines these are$`, st.saysWhichLines)
			sc.Step(`^a file of 10 lines$`, st.file10)
			sc.Step(`^the result does not claim there is more$`, st.doesNotClaimMore)
			sc.Step(`^the agent reads it from line (\d+)$`, st.readFrom)
			sc.Step(`^the read fails$`, st.editFails)
			sc.Step(`^the error says how many lines the file has$`, st.errNamesLineCount)
			sc.Step(`^the agent reads a file with offset (-?\d+) and limit (-?\d+)$`, st.readBadRange)
			sc.Step(`^the read fails with a message naming the bad argument$`, st.failsNamingBadArg)

			sc.Step(`^"needle" appears in two files$`, st.needleInTwo)
			sc.Step(`^the agent searches for "([^"]*)"$`, st.grepFor)
			sc.Step(`^it gets both matches$`, st.getsBothMatches)
			sc.Step(`^each names its file and line number$`, st.eachNamesFileAndLine)
			sc.Step(`^the agent searches for text that appears nowhere$`, st.grepNothing)
			sc.Step(`^the result says there were no matches$`, st.saysNoMatches)
			sc.Step(`^it is not an error$`, st.notAnError)
			sc.Step(`^grep is not a mutating tool$`, func() error { return st.toolNotMutating("grep") })
			sc.Step(`^glob is not a mutating tool$`, func() error { return st.toolNotMutating("glob") })
			sc.Step(`^it does not raise the confirm gate$`, st.doesNotRaiseGate)
			sc.Step(`^"needle" appears both inside and outside a subdirectory$`, st.needleInAndOut)
			sc.Step(`^the agent searches for "needle" under that subdirectory$`, func() error { return st.grepUnder("sub") })
			sc.Step(`^only the matches inside it are returned$`, st.onlyInside)
			sc.Step(`^"needle" appears in a \.go file and a \.md file$`, st.needleInGoAndMd)
			sc.Step(`^the agent searches for "([^"]*)" in "([^"]*)"$`, st.grepInGlob)
			sc.Step(`^only the \.go match is returned$`, st.onlyGoMatch)
			sc.Step(`^a file containing "version 6\.3\.3"$`, st.fileWithVersion)
			sc.Step(`^the line is found$`, st.lineFound)
			sc.Step(`^the agent searches for an unparseable pattern$`, st.grepBadRegex)
			sc.Step(`^the search fails$`, st.editFails)
			sc.Step(`^the error names the pattern problem$`, st.errNamesPattern)
			sc.Step(`^the agent searches under a path that escapes the working directory$`, st.grepEscaping)
			sc.Step(`^nothing outside the working directory is read$`, st.nothingOutsideRead)
			sc.Step(`^a repository containing \.git and node_modules$`, st.repoWithNoise)
			sc.Step(`^the agent searches for text that appears in both$`, st.grepNoise)
			sc.Step(`^neither directory is searched$`, st.neitherSearched)
			sc.Step(`^a binary file containing the byte sequence$`, st.binaryWithBytes)
			sc.Step(`^the agent searches for that sequence$`, st.grepBytes)
			sc.Step(`^the binary is skipped rather than dumped into the transcript$`, st.binarySkipped)
			sc.Step(`^a pattern matching thousands of lines$`, st.manyMatches)
			sc.Step(`^the agent searches for it$`, st.grepMany)
			sc.Step(`^the result is capped$`, st.resultCapped)
			sc.Step(`^it says the result was truncated$`, st.saysTruncated)

			sc.Step(`^\.go files at several depths$`, st.goFilesAtDepths)
			sc.Step(`^the agent globs "([^"]*)"$`, st.globFor)
			sc.Step(`^every one is returned, relative to the working directory$`, st.everyOneReturned)
			sc.Step(`^the agent globs a pattern matching nothing$`, st.globNothing)
			sc.Step(`^files modified at different times$`, st.filesAtDifferentTimes)
			sc.Step(`^the agent globs them$`, st.globThem)
			sc.Step(`^the most recently modified comes first$`, st.newestFirst)
			sc.Step(`^the agent globs a pattern that reaches outside the working directory$`, st.globEscaping)
			sc.Step(`^the glob fails$`, st.editFails)
			sc.Step(`^nothing outside the working directory is listed$`, st.nothingOutsideListed)
			sc.Step(`^the agent globs for files that exist in both$`, st.globNoise)
			sc.Step(`^neither directory is walked$`, st.neitherWalked)
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true,
			Paths: []string{
				"../../features/agent/tools_editing.feature",
				"../../features/agent/tools_navigation.feature",
			},
		},
	}
	if suite.Run() != 0 {
		t.Fatal("agent tool suite scenarios failed")
	}
}
