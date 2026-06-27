package update

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// assetTuple builds the anonymous asset struct release uses, so branch tests can
// assemble releases without repeating the struct literal everywhere.
func assetTuple(name, url string) struct {
	AN  string `json:"name"`
	URL string `json:"browser_download_url"`
} {
	return struct {
		AN  string `json:"name"`
		URL string `json:"browser_download_url"`
	}{AN: name, URL: url}
}

// TestLatestReleaseURLDefault pins the production (un-injected) URL builder so the
// default seam body is exercised, not just the test override.
func TestLatestReleaseURLDefault(t *testing.T) {
	got := latestReleaseURL()
	want := "https://api.github.com/repos/rogerai-fyi/roger/releases/latest"
	if got != want {
		t.Fatalf("latestReleaseURL() = %q, want %q", got, want)
	}
}

// TestHTTPGetBadURL covers httpGet's http.NewRequest error branch via a URL with a
// control character that the request builder rejects.
func TestHTTPGetBadURL(t *testing.T) {
	if _, err := httpGet("http://exa\x00mple.com"); err == nil {
		t.Fatal("httpGet on a control-char URL should error from NewRequest")
	}
}

// TestLatestHTTPGetError covers latest()'s httpGet-error branch (line 87) by pointing
// the release URL at a control-char URL so the request cannot even be built.
func TestLatestHTTPGetError(t *testing.T) {
	orig := latestReleaseURL
	latestReleaseURL = func() string { return "http://exa\x00mple/releases" }
	defer func() { latestReleaseURL = orig }()
	if _, err := latest(); err == nil {
		t.Fatal("latest() should surface the httpGet transport/build error")
	}
}

// TestSplitVerNonNumeric covers splitVer's non-numeric-component break (a -rc suffix
// stops the parse) and isNewer's all-parts-equal conservative return.
func TestSplitVerNonNumeric(t *testing.T) {
	got := splitVer("1.2.3-rc1")
	want := []int{1, 2}
	if len(got) != len(want) || got[0] != 1 || got[1] != 2 {
		t.Fatalf("splitVer(1.2.3-rc1) = %v, want %v (parse stops at the non-numeric part)", got, want)
	}
	// Leading empty/garbage component stops immediately.
	if g := splitVer("x.1.2"); len(g) != 0 {
		t.Fatalf("splitVer(x.1.2) = %v, want [] (first component non-numeric)", g)
	}
	// isNewer line 167: numeric parts equal but raw tags differ -> not newer.
	if isNewer("1.0.0", "1.0.0.0") {
		t.Error("1.0.0.0 has the same numeric value as 1.0.0 -> must NOT be newer")
	}
	if isNewer("1.0.0", "1.0.0-rc2") {
		t.Error("a suffix-only difference must NOT count as newer")
	}
}

// TestCachePathFallback covers cachePath's TempDir fallback when UserCacheDir fails
// (no XDG_CACHE_HOME and no HOME).
func TestCachePathFallback(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("HOME", "")
	if runtime.GOOS == "windows" {
		t.Setenv("LocalAppData", "")
	}
	p := cachePath()
	if !strings.HasPrefix(p, os.TempDir()) {
		t.Fatalf("cachePath() = %q, want a path under TempDir %q when UserCacheDir fails", p, os.TempDir())
	}
	if filepath.Base(p) != "update-check.json" {
		t.Fatalf("cachePath() basename = %q, want update-check.json", filepath.Base(p))
	}
}

// TestCachedNoticeStaleSpawnsRefresh covers CachedNotice's stale branch (line 222): a
// missing cache is stale, so it kicks off a background refresh against a local server
// (never real GitHub) and returns "" immediately. We wait for the cache to land to
// confirm the spawned refresh actually ran.
func TestCachedNoticeStaleSpawnsRefresh(t *testing.T) {
	t.Setenv("ROGERAI_NO_UPDATE_CHECK", "")
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	fakeReleaseServer(t, release{Tag: "v5.0.0"})

	// No cache file exists yet -> stale -> async refresh; immediate return is "".
	if n := CachedNotice("4.0.0"); n != "" {
		t.Fatalf("a missing/stale cache must return %q immediately, got %q", "", n)
	}
	// The spawned goroutine should write the cache; poll until it does.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(cachePath()); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("stale CachedNotice did not spawn a refresh that wrote the cache")
}

// TestRefreshCacheLatestError covers refreshCache's early return when latest() fails.
// No cache file should be written.
func TestRefreshCacheLatestError(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer bad.Close()
	orig := latestReleaseURL
	latestReleaseURL = func() string { return bad.URL }
	defer func() { latestReleaseURL = orig }()

	refreshCache("1.0.0")
	if _, err := os.Stat(cachePath()); !os.IsNotExist(err) {
		t.Fatalf("refreshCache must not write a cache when latest() fails (stat err=%v)", err)
	}
}

// TestUpgradeLatestError covers Upgrade's "could not reach GitHub" branch.
func TestUpgradeLatestError(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusBadGateway)
	}))
	defer bad.Close()
	orig := latestReleaseURL
	latestReleaseURL = func() string { return bad.URL }
	defer func() { latestReleaseURL = orig }()

	err := Upgrade("1.0.0", io.Discard)
	if err == nil || !strings.Contains(err.Error(), "could not reach GitHub") {
		t.Fatalf("Upgrade with an unreachable releases API = %v, want a 'could not reach GitHub' error", err)
	}
}

// TestUpgradeEmptyTag covers Upgrade's empty-tag branch: a release with no tag_name
// yields "no published release found".
func TestUpgradeEmptyTag(t *testing.T) {
	fakeReleaseServer(t, release{Tag: ""})
	err := Upgrade("1.0.0", io.Discard)
	if err == nil || !strings.Contains(err.Error(), "no published release") {
		t.Fatalf("Upgrade with an empty tag = %v, want a 'no published release' error", err)
	}
}

// TestUpgradeExecutablePathError covers Upgrade's executablePath() error branch: the
// release is newer and has this platform's asset, but resolving self fails.
func TestUpgradeExecutablePathError(t *testing.T) {
	name := assetName()
	assetSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("bytes"))
	}))
	defer assetSrv.Close()
	fakeReleaseServer(t, release{Tag: "v9.9.9", Assets: []struct {
		AN  string `json:"name"`
		URL string `json:"browser_download_url"`
	}{assetTuple(name, assetSrv.URL)}})

	origExec := executablePath
	executablePath = func() (string, error) { return "", fmt.Errorf("cannot resolve self") }
	defer func() { executablePath = origExec }()

	err := Upgrade("1.0.0", io.Discard)
	if err == nil || !strings.Contains(err.Error(), "cannot resolve self") {
		t.Fatalf("Upgrade = %v, want the executablePath error propagated", err)
	}
}

// TestInstallAssetCreateTempError covers installAsset's CreateTemp failure: when self's
// directory does not exist, a temp file cannot be created there.
func TestInstallAssetCreateTempError(t *testing.T) {
	self := filepath.Join(t.TempDir(), "no-such-subdir", "roger")
	err := installAsset(self, "http://unused", assetName(), "9.9.9", release{}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "cannot write to") {
		t.Fatalf("installAsset into a missing dir = %v, want a 'cannot write to' error", err)
	}
}

// TestInstallAssetDownloadError covers installAsset's downloadTo-error branch: the asset
// URL returns a non-200, so the download fails and no replace happens.
func TestInstallAssetDownloadError(t *testing.T) {
	miss := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gone", http.StatusNotFound)
	}))
	defer miss.Close()
	self := filepath.Join(t.TempDir(), "roger")
	if err := os.WriteFile(self, []byte("keep"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := installAsset(self, miss.URL, assetName(), "9.9.9", release{}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "download status 404") {
		t.Fatalf("installAsset with a 404 asset = %v, want a 'download status 404' error", err)
	}
	if b, _ := os.ReadFile(self); string(b) != "keep" {
		t.Error("a failed download must leave the original binary intact")
	}
}

// TestInstallAssetReplaceError covers installAsset's "atomic replace failed" branch: by
// making self itself a directory, the final os.Rename(tmp, self) cannot succeed.
func TestInstallAssetReplaceError(t *testing.T) {
	payload := []byte("fresh bytes")
	assetSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer assetSrv.Close()

	dir := t.TempDir()
	self := filepath.Join(dir, "roger-as-dir")
	if err := os.Mkdir(self, 0o755); err != nil { // self is a directory: rename-over fails
		t.Fatal(err)
	}
	err := installAsset(self, assetSrv.URL, assetName(), "9.9.9", release{}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "atomic replace failed") {
		t.Fatalf("installAsset replacing a directory = %v, want an 'atomic replace failed' error", err)
	}
}

// TestReplaceSelfWindowsRenameSelfError covers replaceSelf's Windows branch where
// renaming the (absent) running image to .old fails before any swap.
func TestReplaceSelfWindowsRenameSelfError(t *testing.T) {
	origWin := isWindows
	isWindows = true
	defer func() { isWindows = origWin }()

	dir := t.TempDir()
	self := filepath.Join(dir, "absent.exe") // does not exist -> rename self->old fails
	tmp := filepath.Join(dir, "tmp.exe")
	if err := os.WriteFile(tmp, []byte("fresh"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := replaceSelf(self, tmp); err == nil {
		t.Fatal("replaceSelf (windows) must fail when the running image cannot be renamed aside")
	}
}

// TestReplaceSelfWindowsRollback covers replaceSelf's Windows rollback branch: self is
// renamed to .old, then moving the new binary in fails (tmp is absent), so self.old is
// rolled back to self and the original survives.
func TestReplaceSelfWindowsRollback(t *testing.T) {
	origWin := isWindows
	isWindows = true
	defer func() { isWindows = origWin }()

	dir := t.TempDir()
	self := filepath.Join(dir, "roger.exe")
	if err := os.WriteFile(self, []byte("original"), 0o755); err != nil {
		t.Fatal(err)
	}
	tmp := filepath.Join(dir, "does-not-exist.exe") // rename(tmp, self) will fail
	if err := replaceSelf(self, tmp); err == nil {
		t.Fatal("replaceSelf (windows) must fail when the fresh binary is missing")
	}
	if b, _ := os.ReadFile(self); string(b) != "original" {
		t.Fatalf("rollback should restore self to the original, got %q", string(b))
	}
	if _, err := os.Stat(self + sidecarSuffix); !os.IsNotExist(err) {
		t.Error("rollback should leave no .old sidecar behind")
	}
}

// TestCleanupOldExecError covers CleanupOld's executablePath-error early return on the
// Windows path.
func TestCleanupOldExecError(t *testing.T) {
	origWin := isWindows
	isWindows = true
	defer func() { isWindows = origWin }()
	origExec := executablePath
	executablePath = func() (string, error) { return "", fmt.Errorf("nope") }
	defer func() { executablePath = origExec }()

	CleanupOld() // must simply return without panicking
}

// TestDownloadToCopyError covers downloadTo's io.Copy-error branch: writing to a closed
// file fails even though the HTTP response is a healthy 200.
func TestDownloadToCopyError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("some payload bytes"))
	}))
	defer srv.Close()
	f, err := os.CreateTemp(t.TempDir(), "closed-*")
	if err != nil {
		t.Fatal(err)
	}
	f.Close() // closed before the copy -> writes fail
	if _, err := downloadTo(f, srv.URL); err == nil {
		t.Fatal("downloadTo writing to a closed file should error")
	}
}

// TestExpectedSumHTTPError covers expectedSum's httpGet-error branch: the SHA256SUMS
// asset URL cannot be fetched.
func TestExpectedSumHTTPError(t *testing.T) {
	r := release{Assets: []struct {
		AN  string `json:"name"`
		URL string `json:"browser_download_url"`
	}{assetTuple("SHA256SUMS", "http://exa\x00mple/sums")}}
	if _, _, err := expectedSum(r, "roger-linux-amd64"); err == nil {
		t.Fatal("expectedSum should surface a transport error fetching SHA256SUMS")
	}
}

// TestExpectedSumChecksumsTxtFallback covers expectedSum's second-name lookup
// (checksums.txt) when SHA256SUMS is absent, and a successful star-prefixed match.
func TestExpectedSumChecksumsTxtFallback(t *testing.T) {
	payload := []byte("the binary")
	want := sha256.Sum256(payload)
	wantHex := hex.EncodeToString(want[:])
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s *roger-linux-amd64\n", wantHex)
	}))
	defer srv.Close()
	r := release{Assets: []struct {
		AN  string `json:"name"`
		URL string `json:"browser_download_url"`
	}{assetTuple("checksums.txt", srv.URL)}}

	got, ok, err := expectedSum(r, "roger-linux-amd64")
	if err != nil || !ok || got != wantHex {
		t.Fatalf("expectedSum via checksums.txt = %q/%v/%v, want %s/true", got, ok, err, wantHex)
	}
}

// TestExpectedSumReadError covers expectedSum's io.ReadAll-error branch: a handler that
// promises more bytes (Content-Length) than it sends and then hijacks+closes the
// connection makes the body read fail with an unexpected EOF.
func TestExpectedSumReadError(t *testing.T) {
	var notHijackable atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			notHijackable.Store(true)
			return
		}
		conn, bufw, err := hj.Hijack()
		if err != nil {
			return
		}
		// Promise 100 bytes, send only a few, then slam the connection shut.
		_, _ = bufw.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 100\r\n\r\nshort")
		_ = bufw.Flush()
		_ = conn.Close()
	}))
	defer srv.Close()
	r := release{Assets: []struct {
		AN  string `json:"name"`
		URL string `json:"browser_download_url"`
	}{assetTuple("SHA256SUMS", srv.URL)}}
	_, _, err := expectedSum(r, "roger-linux-amd64")
	// A platform whose httptest ResponseWriter is not a Hijacker can't produce the
	// truncated-body read error; skip from the TEST goroutine (t.Skip must never run
	// from the handler goroutine, where it only Goexits the handler).
	if notHijackable.Load() {
		t.Skip("response writer is not a Hijacker on this platform")
	}
	if err == nil {
		t.Fatal("expectedSum should error when the SHA256SUMS body is truncated")
	}
}
