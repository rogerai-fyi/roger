package runtime_test

// UNPACKING THE RUNTIME (features/serving/local_models.feature §3).
//
// An archive is UNTRUSTED INPUT even after its digest checks out: the digest proves it is the file
// the manifest named, not that every path inside it is harmless. The classic failure is "zip
// slip" - an entry named ../../.bashrc, or a symlink pointing at /etc, written by an extractor that
// joined paths without checking where they land. The real upstream archive contains relative
// symlinks (libmtmd.so -> libmtmd.so.0), so links must work - and they must stay inside the tree.

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/runtime"
)

type entry struct {
	name, body, link string
	mode             int64
	dir              bool
}

func tarGz(t *testing.T, entries []entry) string {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Mode: e.mode}
		switch {
		case e.dir:
			h.Typeflag = tar.TypeDir
		case e.link != "":
			h.Typeflag, h.Linkname = tar.TypeSymlink, e.link
		default:
			h.Typeflag, h.Size = tar.TypeReg, int64(len(e.body))
		}
		if h.Mode == 0 {
			h.Mode = 0o644
		}
		require.NoError(t, tw.WriteHeader(h))
		if h.Typeflag == tar.TypeReg {
			_, err := tw.Write([]byte(e.body))
			require.NoError(t, err)
		}
	}
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	p := filepath.Join(t.TempDir(), "rt.tar.gz")
	require.NoError(t, os.WriteFile(p, buf.Bytes(), 0o644))
	return p
}

func TestARealShapedArchiveUnpacksWithItsLinksAndExecBits(t *testing.T) {
	arc := tarGz(t, []entry{
		{name: "llama-b1/", dir: true, mode: 0o755},
		{name: "llama-b1/llama-server", body: "#!/bin/sh\n", mode: 0o755},
		{name: "llama-b1/libmtmd.so.0", body: "lib", mode: 0o755},
		{name: "llama-b1/libmtmd.so", link: "libmtmd.so.0"},
		{name: "llama-b1/LICENSE", body: "MIT", mode: 0o644},
	})
	dest := t.TempDir()
	require.NoError(t, runtime.Unpack(arc, dest))

	fi, err := os.Stat(filepath.Join(dest, "llama-b1", "llama-server"))
	require.NoError(t, err)
	require.NotZero(t, fi.Mode()&0o100, "the server lost its exec bit and could never be started")

	// The relative link resolves, inside the tree, to the real library.
	b, err := os.ReadFile(filepath.Join(dest, "llama-b1", "libmtmd.so"))
	require.NoError(t, err)
	require.Equal(t, "lib", string(b))
}

func TestAnEntryThatClimbsOutOfTheTreeIsRefused(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "escaped")
	for name, e := range map[string]entry{
		"dotdot":   {name: "../../escaped", body: "x"},
		"absolute": {name: outside, body: "x"},
		"nested":   {name: "llama/../../escaped", body: "x"},
	} {
		t.Run(name, func(t *testing.T) {
			dest := t.TempDir()
			err := runtime.Unpack(tarGz(t, []entry{e}), dest)
			require.ErrorIs(t, err, runtime.ErrUnsafeArchive)
			_, statErr := os.Stat(outside)
			require.True(t, os.IsNotExist(statErr), "an entry was written outside the tree")
		})
	}
}

// The quieter escape: a symlink inside the tree that POINTS outside it, after which a later
// entry written "through" it lands wherever the link aims.
func TestALinkThatPointsOutOfTheTreeIsRefused(t *testing.T) {
	for name, link := range map[string]string{
		"absolute": "/etc",
		"climbing": "../../../etc",
	} {
		t.Run(name, func(t *testing.T) {
			dest := t.TempDir()
			err := runtime.Unpack(tarGz(t, []entry{{name: "lib", link: link}}), dest)
			require.ErrorIs(t, err, runtime.ErrUnsafeArchive)
		})
	}
	// And a link to a sibling directory inside the tree is fine.
	dest := t.TempDir()
	require.NoError(t, runtime.Unpack(tarGz(t, []entry{
		{name: "a/", dir: true, mode: 0o755},
		{name: "a/real", body: "ok"},
		{name: "b/", dir: true, mode: 0o755},
		{name: "b/link", link: "../a/real"},
	}), dest))
}

func TestAZipUnpacksTooAndIsHeldToTheSameRules(t *testing.T) {
	mk := func(names map[string]string) string {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		for n, body := range names {
			w, err := zw.Create(n)
			require.NoError(t, err)
			_, _ = w.Write([]byte(body))
		}
		require.NoError(t, zw.Close())
		p := filepath.Join(t.TempDir(), "rt.zip")
		require.NoError(t, os.WriteFile(p, buf.Bytes(), 0o644))
		return p
	}
	dest := t.TempDir()
	require.NoError(t, runtime.Unpack(mk(map[string]string{"llama-server.exe": "MZ"}), dest))
	_, err := os.Stat(filepath.Join(dest, "llama-server.exe"))
	require.NoError(t, err)

	require.ErrorIs(t, runtime.Unpack(mk(map[string]string{"../evil.exe": "MZ"}), t.TempDir()),
		runtime.ErrUnsafeArchive)
}

func TestSomethingThatIsNotAnArchiveSaysSo(t *testing.T) {
	p := filepath.Join(t.TempDir(), "rt.tar.gz")
	require.NoError(t, os.WriteFile(p, []byte("not an archive"), 0o644))
	require.Error(t, runtime.Unpack(p, t.TempDir()))
	require.Error(t, runtime.Unpack(filepath.Join(t.TempDir(), "absent.tar.gz"), t.TempDir()))
	require.Error(t, runtime.Unpack(filepath.Join(t.TempDir(), "weird.rar"), t.TempDir()))
}

func TestZipDirectoriesAreMadeAndZipLinksAreNotHonoured(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	_, err := zw.Create("rt/sub/")
	require.NoError(t, err)
	h := &zip.FileHeader{Name: "rt/escape"}
	h.SetMode(os.ModeSymlink | 0o777)
	w, err := zw.CreateHeader(h)
	require.NoError(t, err)
	_, _ = w.Write([]byte("/etc/passwd"))
	require.NoError(t, zw.Close())
	archive := filepath.Join(t.TempDir(), "rt.zip")
	require.NoError(t, os.WriteFile(archive, buf.Bytes(), 0o644))

	dest := t.TempDir()
	require.NoError(t, runtime.Unpack(archive, dest))
	fi, err := os.Stat(filepath.Join(dest, "rt", "sub"))
	require.NoError(t, err)
	require.True(t, fi.IsDir())
	_, err = os.Lstat(filepath.Join(dest, "rt", "escape"))
	require.True(t, os.IsNotExist(err), "a zip symlink was honoured")
}

func TestAZipWithAnAbsoluteEntryIsRefused(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("/abs/evil")
	require.NoError(t, err)
	_, _ = w.Write([]byte("x"))
	require.NoError(t, zw.Close())
	archive := filepath.Join(t.TempDir(), "rt.zip")
	require.NoError(t, os.WriteFile(archive, buf.Bytes(), 0o644))
	require.ErrorIs(t, runtime.Unpack(archive, t.TempDir()), runtime.ErrUnsafeArchive)
}

// local_models.feature (2026-10-26): "a runtime archive cannot write outside its folder, even through
// a chain of links". Each link here looks inside the tree on its own; followed one through another,
// t is the folder's parent, so t/pwned would land outside it.
func TestAChainOfLinksCannotWriteOutsideTheFolder(t *testing.T) {
	parent := t.TempDir()
	dest := filepath.Join(parent, "rt")
	err := runtime.Unpack(tarGz(t, []entry{
		{name: "s", link: "."},
		{name: "t", link: "s/.."},
		{name: "t/pwned", body: "escaped"},
	}), dest)
	require.ErrorIs(t, err, runtime.ErrUnsafeArchive)
	_, statErr := os.Lstat(filepath.Join(parent, "pwned"))
	require.True(t, os.IsNotExist(statErr), "nothing was written outside the runtime's folder")
}
