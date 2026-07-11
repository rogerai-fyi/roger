package main

import (
	"bytes"
	"crypto/ed25519"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rogerai-fyi/roger/internal/capsule"
)

func ephemeralKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return priv
}

// TestCmdContextDispatch covers routing: no args (usage), an unknown verb, and help.
func TestCmdContextDispatch(t *testing.T) {
	cfg := config{}
	if err := cmdContext(cfg, nil); err != nil {
		t.Errorf("no args should print usage, got %v", err)
	}
	if err := cmdContext(cfg, []string{"-h"}); err != nil {
		t.Errorf("help should return nil, got %v", err)
	}
	if err := cmdContext(cfg, []string{"bogus"}); err == nil {
		t.Error("unknown context verb should error")
	}
}

// failIO is a reader+writer that always errors, to exercise the ReadAll/Write error paths.
type failIO struct{}

func (failIO) Read([]byte) (int, error)  { return 0, os.ErrClosed }
func (failIO) Write([]byte) (int, error) { return 0, os.ErrClosed }

// TestCmdContextRoutesToExportImport drives the export/import verbs THROUGH cmdContext (not
// the subcommand funcs directly) so the dispatch arms are covered.
func TestCmdContextRoutesToExportImport(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	draft := filepath.Join(dir, "d.json")
	cap := filepath.Join(dir, "c.rcap.json")
	_ = os.WriteFile(draft, []byte(`{"id":"c","messages":[{"role":"user","content":"hi","x_roger":{"turn":0,"agent":"user","ts":1}}]}`), 0o600)
	if err := cmdContext(config{}, []string{"export", draft, "-o", cap}); err != nil {
		t.Fatalf("cmdContext export: %v", err)
	}
	_, err := captureCtxStdout(func() error { return cmdContext(config{}, []string{"import", cap}) })
	if err != nil {
		t.Fatalf("cmdContext import: %v", err)
	}
}

// TestContextUsageFuncs runs the help text functions (no panic, non-empty output).
func TestContextUsageFuncs(t *testing.T) {
	for _, fn := range []func(){contextUsage, contextExportUsage, contextImportUsage} {
		if out, _ := captureCtxStdout(func() error { fn(); return nil }); len(out) == 0 {
			t.Error("usage must print something")
		}
	}
}

// TestContextFailingIO exercises the ReadAll / Write error branches.
func TestContextFailingIO(t *testing.T) {
	priv := ephemeralKey(t)
	if err := contextExport(failIO{}, &bytes.Buffer{}, priv); err == nil {
		t.Error("export read error must propagate")
	}
	if err := contextImportSummary(failIO{}, &bytes.Buffer{}); err == nil {
		t.Error("import read error must propagate")
	}
	if err := contextImportMerge(failIO{}, []byte(`{}`), &bytes.Buffer{}, priv); err == nil {
		t.Error("merge read error must propagate")
	}
	// A valid capsule but a write that fails -> writeCapsule error.
	var buf bytes.Buffer
	_ = contextExport(strings.NewReader(`{"id":"c","messages":[]}`), &buf, priv)
	if err := contextExport(strings.NewReader(`{"id":"c","messages":[]}`), failIO{}, priv); err == nil {
		t.Error("export write error must propagate")
	}
}

// TestContextExportCore: a valid draft signs + verifies (with a real trailing newline);
// bad JSON errors.
func TestContextExportCore(t *testing.T) {
	priv := ephemeralKey(t)
	draft := `{"id":"cap_u","thread":{"title":"T"},"redaction":"full","messages":[{"role":"user","content":"hi","x_roger":{"turn":0,"agent":"user","ts":1}}]}`
	var out bytes.Buffer
	if err := contextExport(strings.NewReader(draft), &out, priv); err != nil {
		t.Fatalf("contextExport: %v", err)
	}
	if !strings.HasSuffix(out.String(), "\n") {
		t.Error("export must end with a newline")
	}
	c, err := capsule.Import(out.Bytes())
	if err != nil || !c.Verify() {
		t.Fatalf("exported capsule must import+verify: %v", err)
	}
	if c.Meta.ExportedBy != "roger-cli" {
		t.Errorf("exported_by = %q, want roger-cli", c.Meta.ExportedBy)
	}
	if err := contextExport(strings.NewReader("{bad"), &bytes.Buffer{}, priv); err == nil {
		t.Error("bad draft JSON must error")
	}
}

// TestContextImportSummaryVerifyFail: a tampered capsule yields an error and no summary.
func TestContextImportSummaryVerifyFail(t *testing.T) {
	priv := ephemeralKey(t)
	var signedBuf bytes.Buffer
	_ = contextExport(strings.NewReader(`{"id":"c","messages":[{"role":"user","content":"hi","x_roger":{"turn":0,"agent":"user","ts":1}}]}`), &signedBuf, priv)
	tampered := strings.Replace(signedBuf.String(), `"hi"`, `"evil"`, 1)
	if err := contextImportSummary(strings.NewReader(tampered), &bytes.Buffer{}); err == nil {
		t.Error("tampered capsule must fail import")
	}
}

// TestContextImportMergeCore: merges append-only, re-signs, and rejects a bad base.
func TestContextImportMergeCore(t *testing.T) {
	priv := ephemeralKey(t)
	base, _ := capsule.Export(capsule.Draft{ID: "c", Thread: capsule.Thread{BaseWatermark: 1}, Messages: []capsule.Message{{Role: "user", Content: "hi", XRoger: capsule.XRoger{Turn: 0, Agent: "user", TS: 1}}}}, priv, "roger-cli", func() int64 { return 1 })
	baseRaw, _ := base.Marshal()
	guest, _ := capsule.Export(capsule.Draft{ID: "c", Thread: capsule.Thread{BaseWatermark: 2}, Messages: []capsule.Message{
		{Role: "user", Content: "hi", XRoger: capsule.XRoger{Turn: 0, Agent: "user", TS: 1}},
		{Role: "assistant", Content: "yo", XRoger: capsule.XRoger{Turn: 1, Agent: "opencode", TS: 2}},
	}}, priv, "roger-cli", func() int64 { return 2 })
	guestRaw, _ := guest.Marshal()

	var out bytes.Buffer
	if err := contextImportMerge(bytes.NewReader(guestRaw), baseRaw, &out, priv); err != nil {
		t.Fatalf("merge: %v", err)
	}
	merged, err := capsule.Import(out.Bytes())
	if err != nil || !merged.Verify() {
		t.Fatalf("merged must import+verify: %v", err)
	}
	if len(merged.Messages) != 2 {
		t.Errorf("merged turns = %d, want 2", len(merged.Messages))
	}
	// A malformed base is rejected.
	if err := contextImportMerge(bytes.NewReader(guestRaw), []byte("{bad"), &bytes.Buffer{}, priv); err == nil {
		t.Error("malformed base must error")
	}
	// A forked incoming (turn 0 rewritten) is rejected by the append-only merge.
	fork, _ := capsule.Export(capsule.Draft{ID: "c", Thread: capsule.Thread{BaseWatermark: 1}, Messages: []capsule.Message{{Role: "user", Content: "REWRITTEN", XRoger: capsule.XRoger{Turn: 0, Agent: "user", TS: 1}}}}, priv, "roger-cli", func() int64 { return 3 })
	forkRaw, _ := fork.Marshal()
	if err := contextImportMerge(bytes.NewReader(forkRaw), baseRaw, &bytes.Buffer{}, priv); err == nil {
		t.Error("forked incoming must error")
	}
}

// TestCmdContextExportFileErrors: a missing input file and an unwritable output error out.
func TestCmdContextExportFileErrors(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := cmdContextExport([]string{filepath.Join(t.TempDir(), "nope.json")}); err == nil {
		t.Error("missing input file must error")
	}
	dir := t.TempDir()
	draft := filepath.Join(dir, "d.json")
	_ = os.WriteFile(draft, []byte(`{"id":"c","messages":[]}`), 0o600)
	// -o points at a directory -> os.Create fails.
	if err := cmdContextExport([]string{draft, "-o", dir}); err == nil {
		t.Error("unwritable -o must error")
	}
}

// TestCmdContextImportFileErrors: missing input, and --into with a missing base.
func TestCmdContextImportFileErrors(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := cmdContextImport([]string{filepath.Join(t.TempDir(), "nope.json")}); err == nil {
		t.Error("missing input file must error")
	}
	dir := t.TempDir()
	cap := filepath.Join(dir, "c.rcap.json")
	priv := ephemeralKey(t)
	var b bytes.Buffer
	_ = contextExport(strings.NewReader(`{"id":"c","messages":[]}`), &b, priv)
	_ = os.WriteFile(cap, b.Bytes(), 0o600)
	if err := cmdContextImport([]string{cap, "--into", filepath.Join(dir, "missing-base.json")}); err == nil {
		t.Error("--into with a missing base must error")
	}
}

// TestOpenHelpers covers openIn/openOut for stdin/stdout, files, and the leadingPositional
// flags-first branch, plus short().
func TestOpenHelpers(t *testing.T) {
	if r, c, err := openIn("-"); err != nil || r != os.Stdin {
		t.Error("openIn - must be stdin")
	} else {
		c()
	}
	if w, c, err := openOut("-"); err != nil || w != os.Stdout {
		t.Error("openOut - must be stdout")
	} else {
		c()
	}
	f := filepath.Join(t.TempDir(), "f")
	_ = os.WriteFile(f, []byte("x"), 0o600)
	if _, c, err := openIn(f); err != nil {
		t.Errorf("openIn file: %v", err)
	} else {
		c()
	}
	if _, c, err := openOut(f); err != nil {
		t.Errorf("openOut file: %v", err)
	} else {
		c()
	}
	if _, _, err := openIn(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Error("openIn missing must error")
	}
	if _, _, err := openOut(t.TempDir()); err == nil {
		t.Error("openOut dir must error")
	}
	// flags-first: no leading positional.
	if p, rest := leadingPositional([]string{"-o", "x", "file"}); p != "" || len(rest) != 3 {
		t.Errorf("leadingPositional flags-first = %q,%v", p, rest)
	}
	if p, _ := leadingPositional([]string{"file", "-o", "x"}); p != "file" {
		t.Errorf("leadingPositional positional-first = %q, want file", p)
	}
	if short("abc") != "abc" {
		t.Error("short must pass through a short key")
	}
	if !strings.HasSuffix(short(strings.Repeat("a", 40)), "…") {
		t.Error("short must ellipsize a long key")
	}
}
