package gguftest_test

import (
	"os"
	"path/filepath"
	"testing"

	"rogerai.fm/roger/v6/internal/detect"
	"rogerai.fm/roger/v6/internal/detect/gguftest"
)

// The helper writes a header the real parser reads back: what it says is what detect reports.
func TestAWrittenHeaderIsReadBackByTheParser(t *testing.T) {
	p := filepath.Join(t.TempDir(), "adapter.gguf")
	gguftest.WriteStrings(t, p, map[string]string{
		"general.architecture": "llama", "general.type": "adapter", "adapter.type": "lora",
		"general.base_model.0.name": "Base",
	})
	s, err := detect.ReadShape(p)
	if err == nil {
		t.Fatal("a string-only header has no shape to resolve; want an error beside the kind")
	}
	if s.Kind != "adapter" || s.AdapterType != "lora" || s.Arch != "llama" || s.BaseModel != "Base" {
		t.Errorf("read back %+v", s)
	}
	gguftest.WriteStrings(t, p, map[string]string{})
	if s, _ := detect.ReadShape(p); s.Kind != "" {
		t.Errorf("an empty header says nothing, got kind %q", s.Kind)
	}
}

// recording is a testing.TB whose Fatal records instead of stopping the test.
type recording struct {
	testing.TB
	fatal string
}

func (r *recording) Fatal(args ...any) { r.fatal = args[0].(error).Error() }

func TestAPathThatCannotBeWrittenIsFatal(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a-file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := &recording{TB: t}
	gguftest.WriteStrings(r, filepath.Join(file, "under-a-file.gguf"), nil) // the directory cannot be made
	if r.fatal == "" {
		t.Error("a parent that is a file must be fatal")
	}
	r = &recording{TB: t}
	gguftest.WriteStrings(r, dir, nil) // the path is a directory, so the write fails
	if r.fatal == "" {
		t.Error("a path that is a directory must be fatal")
	}
}
