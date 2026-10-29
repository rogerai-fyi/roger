package servedfile

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const goodSHA = "2e8040ceae78a1b2c3d4e5f60718293a4b5c6d7e8f9001122334455667788990"

func good() File {
	return File{Repo: "bartowski/SmolLM2-135M-Instruct-GGUF", File: "SmolLM2-135M-Instruct-Q4_K_M.gguf",
		Quant: "Q4_K_M", SHA256: goodSHA, Bytes: 105_000_000}
}

func TestAValidReportIsKeptWhole(t *testing.T) {
	got := Clean(good())
	require.NotNil(t, got)
	require.Equal(t, good(), *got)
	require.Equal(t, "bartowski/SmolLM2-135M-Instruct-GGUF:Q4_K_M", got.Ref())
}

func TestAReportThatCannotNameOneFileIsDropped(t *testing.T) {
	for name, mut := range map[string]func(*File){
		"no repo":            func(f *File) { f.Repo = "" },
		"repo without owner": func(f *File) { f.Repo = "SmolLM2" },
		"repo with a path":   func(f *File) { f.Repo = "a/b/c" },
		"repo traversal":     func(f *File) { f.Repo = "../etc" },
		"repo too long":      func(f *File) { f.Repo = "o/" + strings.Repeat("x", 200) },
		"no file":            func(f *File) { f.File = "" },
		"not a gguf":         func(f *File) { f.File = "model.safetensors" },
		"absolute file":      func(f *File) { f.File = "/etc/model.gguf" },
		"file traversal":     func(f *File) { f.File = "../x.gguf" },
		"control in file":    func(f *File) { f.File = "a\x1b[2J.gguf" },
		"short digest":       func(f *File) { f.SHA256 = "abc" },
		"upper digest":       func(f *File) { f.SHA256 = strings.ToUpper(goodSHA) },
		"no digest":          func(f *File) { f.SHA256 = "" },
		"control in quant":   func(f *File) { f.Quant = "Q4\n" },
		"negative size":      func(f *File) { f.Bytes = -1 },
	} {
		f := good()
		mut(&f)
		require.Nil(t, Clean(f), name)
	}
}

func TestTheRefNamesTheFileWhenThereIsNoQuant(t *testing.T) {
	f := good()
	f.Quant = ""
	require.Equal(t, "bartowski/SmolLM2-135M-Instruct-GGUF:SmolLM2-135M-Instruct-Q4_K_M.gguf", f.Ref())
}

func TestTwoReportsAreTheSameFileOnlyWhenRepoFileAndDigestAgree(t *testing.T) {
	a, b := good(), good()
	b.Repo = strings.ToUpper(b.Repo) // the hub's repository names are case-insensitive
	require.True(t, a.Same(b))
	b = good()
	b.SHA256 = strings.Repeat("0", 64)
	require.False(t, a.Same(b))
	b = good()
	b.File = "other.gguf"
	require.False(t, a.Same(b))
}

func TestFromIDSplitsTheRepositoryOutOfAModelID(t *testing.T) {
	f := FromID("bartowski/SmolLM2-135M-Instruct-GGUF:Q4_K_M", "SmolLM2-135M-Instruct-Q4_K_M.gguf", "Q4_K_M", goodSHA, 1)
	require.NotNil(t, f)
	require.Equal(t, "bartowski/SmolLM2-135M-Instruct-GGUF", f.Repo)
	require.Nil(t, FromID("my-hand-placed-model", "m.gguf", "", goodSHA, 1), "a model placed by hand names no repository")
}
