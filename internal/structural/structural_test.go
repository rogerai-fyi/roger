package structural

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

type fakeT struct{ msg string }

func (f *fakeT) Helper() {}
func (f *fakeT) Fatalf(format string, args ...any) {
	if f.msg == "" {
		f.msg = fmt.Sprintf(format, args...)
	}
}

func TestForbidDeps(t *testing.T) {
	f := &fakeT{}
	ForbidDeps(f, "rogerai.fm/roger/v7/internal/netbind", []string{"rogerai.fm/roger/v7/internal/netpriv"})
	require.Contains(t, f.msg, "links rogerai.fm/roger/v7/internal/netpriv")

	f = &fakeT{}
	ForbidDeps(f, "rogerai.fm/roger/v7/internal/netbind", []string{"rogerai.fm/roger/v7/internal/netpriv"}, "rogerai.fm/roger/v7/internal/netpriv")
	require.Empty(t, f.msg, "an allowed package passes")

	f = &fakeT{}
	ForbidDeps(f, "rogerai.fm/roger/v7/internal/netpriv", []string{"rogerai.fm/roger/v7/internal/client"})
	require.Empty(t, f.msg)
}

func TestDepsFailures(t *testing.T) {
	f := &fakeT{}
	DepsAtLeast(f, "rogerai.fm/roger/v7/internal/netpriv", 1000)
	require.Contains(t, f.msg, "did not enumerate the graph")

	old := GoTool
	GoTool = "go-that-does-not-exist"
	defer func() { GoTool = old }()
	f = &fakeT{}
	require.Nil(t, Deps(f, "rogerai.fm/roger/v7/internal/netpriv"))
	require.Contains(t, f.msg, "proves nothing")
}

func TestScanSource(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\nvar x = 1\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a_test.go"), []byte("package a\n// net.Dial is fine in a test\n"), 0o600))
	f := &fakeT{}
	require.Equal(t, 1, ScanSource(f, dir, NoOutbound, NoListen))
	require.Empty(t, f.msg)

	require.NoError(t, os.WriteFile(filepath.Join(dir, "b.go"), []byte("package a\nfunc f() { net.Listen(\"tcp\", \"\") }\n"), 0o600))
	f = &fakeT{}
	ScanSource(f, dir, NoOutbound, NoListen)
	require.Contains(t, f.msg, "net.Listen")

	f = &fakeT{}
	ScanSource(f, t.TempDir(), NoOutbound)
	require.Contains(t, f.msg, "read no source")

	f = &fakeT{}
	ScanSource(f, "[", NoOutbound)
	require.NotEmpty(t, f.msg, "a bad pattern fails")

	unreadable := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(unreadable, "x.go"), 0o700)) // a directory named like a file
	f = &fakeT{}
	ScanSource(f, unreadable, NoOutbound)
	require.NotEmpty(t, f.msg, "an unreadable file fails")
}
