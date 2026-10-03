package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"archive/tar"
	"compress/gzip"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/provision"
)

func runtimeArchive(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range []struct {
		name, body string
		mode       int64
	}{
		{"llama-bX/llama-server", "#!/bin/sh\necho ok\n", 0o755},
		{"llama-bX/LICENSE", "MIT", 0o644},
	} {
		require.NoError(t, tw.WriteHeader(&tar.Header{Name: e.name, Mode: e.mode, Size: int64(len(e.body)), Typeflag: tar.TypeReg}))
		_, _ = tw.Write([]byte(e.body))
	}
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	return buf.Bytes()
}

func TestInstallFetchesVerifiesUnpacksAndThenCostsNothing(t *testing.T) {
	body := runtimeArchive(t)
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	sum := sha256.Sum256(body)
	a := Asset{Platform: "ubuntu-x64", Tag: "bX", Name: "rt.tar.gz", URL: srv.URL,
		Bytes: int64(len(body)), SHA256: hex.EncodeToString(sum[:]), Exe: "llama-bX/llama-server"}
	dir := t.TempDir()
	f := &provision.Fetcher{FreeSpace: func(string) (int64, bool) { return 1 << 40, true }}

	exe, err := installAsset(context.Background(), a, dir, f)
	require.NoError(t, err)
	fi, err := os.Stat(exe)
	require.NoError(t, err)
	require.NotZero(t, fi.Mode()&0o100, "the installed server is not executable")

	// The archive is given back once unpacked.
	_, err = os.Stat(filepath.Join(dir, "rt.tar.gz"))
	require.True(t, os.IsNotExist(err), "the archive was left taking up space")

	// The second run costs nothing: no request at all.
	exe2, err := installAsset(context.Background(), a, dir, f)
	require.NoError(t, err)
	require.Equal(t, exe, exe2)
	require.Equal(t, int64(1), atomic.LoadInt64(&hits), "an installed runtime was fetched again")

	// A NEW pin is not mistaken for the old one: the marker records the digest.
	b := a
	b.SHA256 = hex.EncodeToString(make([]byte, 32))
	require.False(t, isVerified(treeFor(dir, b), b), "a different pin was taken as already installed")
}

// A digest that does not match means the runtime is never unpacked, let alone run.
func TestARuntimeThatFailsItsDigestIsNeverUnpacked(t *testing.T) {
	body := runtimeArchive(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	a := Asset{Platform: "ubuntu-x64", Tag: "bX", Name: "rt.tar.gz", URL: srv.URL,
		Bytes: int64(len(body)), SHA256: hex.EncodeToString(make([]byte, 32)), Exe: "llama-bX/llama-server"}
	dir := t.TempDir()
	f := &provision.Fetcher{FreeSpace: func(string) (int64, bool) { return 1 << 40, true }}
	_, err := installAsset(context.Background(), a, dir, f)
	require.ErrorIs(t, err, provision.ErrCorrupt)
	_, statErr := os.Stat(treeFor(dir, a))
	require.True(t, os.IsNotExist(statErr), "a runtime that failed its digest was unpacked anyway")
}

func TestInstalledSaysWhetherAFetchIsNeeded(t *testing.T) {
	_, _, ok := Installed(Host{OS: "linux", Arch: "amd64"}, t.TempDir())
	require.False(t, ok)
	_, _, ok = Installed(Host{OS: "linux", Arch: "arm"}, t.TempDir())
	require.False(t, ok, "a machine with no runtime reported one as installed")
}

func TestInstallRefusesAMachineWithNoRuntimeBeforeFetching(t *testing.T) {
	_, _, err := Install(context.Background(), Host{OS: "linux", Arch: "arm"}, t.TempDir(),
		&provision.Fetcher{})
	require.ErrorIs(t, err, ErrNoRuntime)
}

func servedAsset(t *testing.T, body []byte, name, exe string) (Asset, func()) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	sum := sha256.Sum256(body)
	return Asset{Platform: "p", Tag: "bX", Name: name, URL: srv.URL, Bytes: int64(len(body)),
		SHA256: hex.EncodeToString(sum[:]), Exe: exe}, srv.Close
}

func roomy() *provision.Fetcher {
	return &provision.Fetcher{FreeSpace: func(string) (int64, bool) { return 1 << 40, true }}
}

// A verified download that is not an archive leaves nothing behind at the trusted path.
func TestAVerifiedDownloadThatIsNotAnArchiveLeavesNoTree(t *testing.T) {
	a, done := servedAsset(t, []byte("not an archive"), "rt.tar.gz", "llama-bX/llama-server")
	defer done()
	dir := t.TempDir()
	_, err := installAsset(context.Background(), a, dir, roomy())
	require.Error(t, err)
	for _, p := range []string{treeFor(dir, a), treeFor(dir, a) + ".staging"} {
		_, statErr := os.Stat(p)
		require.True(t, os.IsNotExist(statErr), "%s was left behind", p)
	}
}

// An archive that unpacks but has no server in it is not a runtime.
func TestAnArchiveWithoutTheServerIsNotARuntime(t *testing.T) {
	a, done := servedAsset(t, runtimeArchive(t), "rt.tar.gz", "llama-bX/missing-server")
	defer done()
	_, err := installAsset(context.Background(), a, t.TempDir(), roomy())
	require.ErrorIs(t, err, ErrNoRuntime)
}

func TestARuntimeThatCannotBeReachedSaysSo(t *testing.T) {
	a := Asset{Platform: "p", Tag: "bX", Name: "rt.tar.gz", URL: "http://127.0.0.1:1/rt", Bytes: 10,
		SHA256: hex.EncodeToString(make([]byte, 32)), Exe: "x"}
	_, err := installAsset(context.Background(), a, t.TempDir(), roomy())
	require.ErrorIs(t, err, provision.ErrUnreachable)
}

// A directory that cannot be made (a file is in the way) fails before any download.
func TestAnUnwritableRuntimeDirFailsBeforeFetching(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(blocker, nil, 0o644))
	a := Asset{Platform: "p", Tag: "bX", Name: "rt.tar.gz", URL: "http://127.0.0.1:1/rt", SHA256: "x", Exe: "x"}
	_, err := installAsset(context.Background(), a, filepath.Join(blocker, "rt"), roomy())
	require.Error(t, err)
	require.NotErrorIs(t, err, provision.ErrUnreachable, "it tried to fetch into a directory it could not make")
	require.Error(t, Unpack("x.tar.gz", filepath.Join(blocker, "dest")))
}

func tarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		require.NoError(t, tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}))
		_, _ = tw.Write([]byte(body))
	}
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	return buf.Bytes()
}

// A CUDA build's runtime is a second pinned archive; its libraries go beside the server, where the
// build's own libraries look for them ($ORIGIN), and both digests are checked before anything runs.
func TestABuildsSecondArchiveIsVerifiedAndPlacedBesideTheServer(t *testing.T) {
	main := runtimeArchive(t)
	libs := tarGz(t, map[string]string{"cudart-x/libcudart.so.12": "cudart", "cudart-x/libcublas.so.12": "cublas"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/libs" {
			_, _ = w.Write(libs)
			return
		}
		_, _ = w.Write(main)
	}))
	defer srv.Close()
	ms, ls := sha256.Sum256(main), sha256.Sum256(libs)
	a := Asset{Platform: "ubuntu-cuda-x", Tag: "bX", Build: BuildCUDA, Name: "rt.tar.gz", URL: srv.URL + "/main",
		Bytes: int64(len(main)), SHA256: hex.EncodeToString(ms[:]), Exe: "llama-bX/llama-server",
		Libs: &Part{Name: "libs.tar.gz", URL: srv.URL + "/libs", Bytes: int64(len(libs)), SHA256: hex.EncodeToString(ls[:])}}
	dir := t.TempDir()
	f := &provision.Fetcher{FreeSpace: func(string) (int64, bool) { return 1 << 40, true }}
	exe, err := installAsset(context.Background(), a, dir, f)
	require.NoError(t, err)
	for _, lib := range []string{"libcudart.so.12", "libcublas.so.12"} {
		_, err := os.Stat(filepath.Join(filepath.Dir(exe), lib))
		require.NoError(t, err, "%s sits beside the server", lib)
	}
	_, err = os.Stat(filepath.Join(dir, "libs.tar.gz"))
	require.True(t, os.IsNotExist(err), "the second archive is given back too")
	require.True(t, isVerified(treeFor(dir, a), a))
	_, ok := installedAt(dir, a)
	require.True(t, ok)

	// A changed runtime pin is not taken as installed, even when the build's own pin is unchanged.
	b := a
	b.Libs = &Part{Name: "libs.tar.gz", URL: srv.URL + "/libs", Bytes: int64(len(libs)), SHA256: hex.EncodeToString(make([]byte, 32))}
	require.False(t, isVerified(treeFor(dir, b), b))

	// A runtime archive that fails its digest leaves nothing installed.
	dir2 := t.TempDir()
	_, err = installAsset(context.Background(), b, dir2, f)
	require.ErrorIs(t, err, provision.ErrCorrupt)
	_, statErr := os.Stat(treeFor(dir2, b))
	require.True(t, os.IsNotExist(statErr), "a build whose runtime failed its digest was installed anyway")
}

// Review 2026-10-02: the second archive is code roger loads, so flattening it beside the server may
// never replace a file already there (the server, a library it loads) and never follows a link.
func TestASecondArchiveCanNeitherReplaceNorLinkOut(t *testing.T) {
	for name, libs := range map[string][]byte{
		"a file named like the server": tarGz(t, map[string]string{"cudart-x/llama-server": "evil"}),
		"two files with one name":      tarGz(t, map[string]string{"cudart-x/a/libx.so": "1", "cudart-x/b/libx.so": "2"}),
	} {
		root := t.TempDir()
		dest := filepath.Join(root, "llama-bX")
		require.NoError(t, os.MkdirAll(dest, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dest, "llama-server"), []byte("real"), 0o755))
		archive := filepath.Join(t.TempDir(), "libs.tar.gz")
		require.NoError(t, os.WriteFile(archive, libs, 0o644))
		err := placeBeside(archive, root, dest)
		require.ErrorIs(t, err, ErrUnsafeArchive, name)
		b, _ := os.ReadFile(filepath.Join(dest, "llama-server"))
		require.Equal(t, "real", string(b), "%s: the server was not replaced", name)
	}

	// A link, even one that stays inside its own archive, is not followed into the server's directory.
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: "cudart-x/libx.so", Typeflag: tar.TypeSymlink, Linkname: "libx.so.12"}))
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	root := t.TempDir()
	archive := filepath.Join(t.TempDir(), "libs.tar.gz")
	require.NoError(t, os.WriteFile(archive, buf.Bytes(), 0o644))
	require.ErrorIs(t, placeBeside(archive, root, filepath.Join(root, "llama-bX")), ErrUnsafeArchive)
}
