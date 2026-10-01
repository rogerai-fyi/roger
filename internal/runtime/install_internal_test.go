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
