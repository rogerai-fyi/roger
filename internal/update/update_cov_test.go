package update

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestHTTPGetAndFindAsset covers httpGet (real GET over the wire to a local server)
// and findAsset (asset-URL lookup by name, hit + miss).
func TestHTTPGetAndFindAsset(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != "rogerai-cli" {
			t.Errorf("missing User-Agent header")
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()
	resp, err := httpGet(srv.URL)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("httpGet = %v / %v", resp, err)
	}
	resp.Body.Close()

	var r release
	r.Assets = append(r.Assets, struct {
		AN  string `json:"name"`
		URL string `json:"browser_download_url"`
	}{AN: "roger-linux-amd64", URL: "http://x/a"})
	if url, ok := r.findAsset("roger-linux-amd64"); !ok || url != "http://x/a" {
		t.Errorf("findAsset hit = %q/%v", url, ok)
	}
	if _, ok := r.findAsset("absent"); ok {
		t.Errorf("findAsset(absent) should miss")
	}
}

// TestDownloadToAndExpectedSum covers downloadTo (stream + sha256) and expectedSum
// (parse a SHA256SUMS asset; hit, miss-by-name, and no-asset).
func TestDownloadToAndExpectedSum(t *testing.T) {
	payload := []byte("the binary bytes")
	want := sha256.Sum256(payload)
	wantHex := hex.EncodeToString(want[:])

	assetSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer assetSrv.Close()

	// downloadTo streams to a temp file and returns the hex sha256.
	f, _ := os.CreateTemp(t.TempDir(), "dl-*")
	sum, err := downloadTo(f, assetSrv.URL)
	f.Close()
	if err != nil || sum != wantHex {
		t.Fatalf("downloadTo sum = %q (err %v), want %s", sum, err, wantHex)
	}

	// expectedSum: a SHA256SUMS asset listing the binary.
	sumsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s  roger-linux-amd64\n%s *other\n", wantHex, "deadbeef")
	}))
	defer sumsSrv.Close()
	r := release{Assets: []struct {
		AN  string `json:"name"`
		URL string `json:"browser_download_url"`
	}{{AN: "SHA256SUMS", URL: sumsSrv.URL}}}

	got, ok, err := expectedSum(r, "roger-linux-amd64")
	if err != nil || !ok || got != wantHex {
		t.Fatalf("expectedSum = %q/%v/%v, want %s/true", got, ok, err, wantHex)
	}
	if _, ok, _ := expectedSum(r, "not-listed"); ok {
		t.Errorf("expectedSum for an unlisted name should be ok=false")
	}
	if _, ok, _ := expectedSum(release{}, "roger-linux-amd64"); ok {
		t.Errorf("expectedSum with no SHA256SUMS asset should be ok=false")
	}

	// downloadTo on a 404 surfaces an error.
	missSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gone", http.StatusNotFound)
	}))
	defer missSrv.Close()
	f2, _ := os.CreateTemp(t.TempDir(), "dl2-*")
	if _, err := downloadTo(f2, missSrv.URL); err == nil {
		t.Error("downloadTo on 404 should error")
	}
	f2.Close()
}

// fakeReleaseServer points latestReleaseURL at a local server returning the given
// release JSON, and restores the original after the test.
func fakeReleaseServer(t *testing.T, rel release) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(rel)
	}))
	t.Cleanup(srv.Close)
	orig := latestReleaseURL
	latestReleaseURL = func() string { return srv.URL }
	t.Cleanup(func() { latestReleaseURL = orig })
}

// TestCheckViaInjectedURL covers latest()+Check() against a local release server: a
// newer published tag is Available; an equal tag is not.
func TestCheckViaInjectedURL(t *testing.T) {
	fakeReleaseServer(t, release{Tag: "v9.9.9"})
	res, err := Check("0.1.0")
	if err != nil || !res.Available || res.Latest != "9.9.9" {
		t.Fatalf("Check = %+v (err %v), want Available 9.9.9", res, err)
	}
	if res.Notice() == "" {
		t.Error("an available update should render a notice")
	}

	fakeReleaseServer(t, release{Tag: "v0.1.0"})
	res2, _ := Check("0.1.0")
	if res2.Available {
		t.Errorf("equal version should not be Available: %+v", res2)
	}
	if res2.Notice() != "" {
		t.Error("up-to-date should render no notice")
	}
}

// TestUpgradeEarlyPaths covers Upgrade's no-network-mutation branches: already-latest
// (no-op) and a release missing this platform's asset (error). It never reaches the
// binary-replacing tail.
func TestUpgradeEarlyPaths(t *testing.T) {
	// Already latest -> no-op, nil error.
	fakeReleaseServer(t, release{Tag: "v2.0.0"})
	var buf strings.Builder
	if err := Upgrade("2.0.0", &buf); err != nil {
		t.Fatalf("Upgrade(already latest) = %v, want nil", err)
	}
	if !strings.Contains(buf.String(), "already") {
		t.Errorf("already-latest message missing: %q", buf.String())
	}

	// Newer release but no asset for this platform -> error.
	fakeReleaseServer(t, release{Tag: "v9.9.9"})
	if err := Upgrade("1.0.0", io.Discard); err == nil {
		t.Error("Upgrade with no platform asset should error")
	}
}

// TestInstallAssetAndReplaceSelf covers the download->verify->atomic-replace tail
// against a temp target (never the test binary), plus a checksum-mismatch refusal and
// replaceSelf's direct rename.
func TestInstallAssetAndReplaceSelf(t *testing.T) {
	payload := []byte("new binary v9")
	sum := sha256.Sum256(payload)
	sumHex := hex.EncodeToString(sum[:])

	assetSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer assetSrv.Close()
	name := assetName()
	sumsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s  %s\n", sumHex, name)
	}))
	defer sumsSrv.Close()
	rel := release{Assets: []struct {
		AN  string `json:"name"`
		URL string `json:"browser_download_url"`
	}{{AN: "SHA256SUMS", URL: sumsSrv.URL}}}

	// Happy path: install over a temp "self".
	self := filepath.Join(t.TempDir(), "roger-self")
	if err := os.WriteFile(self, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	var buf strings.Builder
	if err := installAsset(self, assetSrv.URL, name, "9.9.9", rel, &buf); err != nil {
		t.Fatalf("installAsset = %v, want nil", err)
	}
	got, _ := os.ReadFile(self)
	if string(got) != string(payload) {
		t.Errorf("self not replaced with the new binary bytes")
	}
	if !strings.Contains(buf.String(), "checksum verified") {
		t.Errorf("expected checksum verification message: %q", buf.String())
	}

	// Checksum mismatch: refuse to install (self untouched).
	badSums := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s  %s\n", "deadbeefdeadbeef", name)
	}))
	defer badSums.Close()
	relBad := release{Assets: []struct {
		AN  string `json:"name"`
		URL string `json:"browser_download_url"`
	}{{AN: "SHA256SUMS", URL: badSums.URL}}}
	self2 := filepath.Join(t.TempDir(), "roger-self2")
	_ = os.WriteFile(self2, []byte("keep"), 0o755)
	if err := installAsset(self2, assetSrv.URL, name, "9.9.9", relBad, io.Discard); err == nil {
		t.Error("installAsset with a mismatched checksum must refuse")
	}
	if b, _ := os.ReadFile(self2); string(b) != "keep" {
		t.Error("a refused install must leave the original binary intact")
	}

	// replaceSelf direct (non-Windows: a plain atomic rename).
	if runtime.GOOS != "windows" {
		dst := filepath.Join(t.TempDir(), "dst")
		src := filepath.Join(t.TempDir(), "src")
		_ = os.WriteFile(dst, []byte("old"), 0o755)
		_ = os.WriteFile(src, []byte("fresh"), 0o755)
		if err := replaceSelf(dst, src); err != nil {
			t.Fatalf("replaceSelf = %v", err)
		}
		if b, _ := os.ReadFile(dst); string(b) != "fresh" {
			t.Error("replaceSelf should move src over dst")
		}
	}
}

// TestUpgradeFullSuccess drives Upgrade end-to-end against a temp "self" (via the
// executablePath seam, so the test binary is never touched): a newer release with this
// platform's asset + a matching SHA256SUMS is downloaded, verified, and installed.
func TestUpgradeFullSuccess(t *testing.T) {
	payload := []byte("rogerai v9.9.9 binary")
	sum := sha256.Sum256(payload)
	name := assetName()

	assetSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer assetSrv.Close()
	sumsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(sum[:]), name)
	}))
	defer sumsSrv.Close()

	fakeReleaseServer(t, release{Tag: "v9.9.9", Assets: []struct {
		AN  string `json:"name"`
		URL string `json:"browser_download_url"`
	}{
		{AN: name, URL: assetSrv.URL},
		{AN: "SHA256SUMS", URL: sumsSrv.URL},
	}})

	self := filepath.Join(t.TempDir(), "roger")
	_ = os.WriteFile(self, []byte("old binary"), 0o755)
	origExec := executablePath
	executablePath = func() (string, error) { return self, nil }
	t.Cleanup(func() { executablePath = origExec })

	var buf strings.Builder
	if err := Upgrade("1.0.0", &buf); err != nil {
		t.Fatalf("Upgrade = %v, want nil", err)
	}
	if b, _ := os.ReadFile(self); string(b) != string(payload) {
		t.Error("Upgrade should have installed the new binary over self")
	}
	if !strings.Contains(buf.String(), "now v9.9.9") {
		t.Errorf("Upgrade success message missing: %q", buf.String())
	}
}

// TestWindowsReplaceAndCleanup forces the isWindows branch (on any host) to cover the
// locked-binary rename dance and the startup sidecar cleanup.
func TestWindowsReplaceAndCleanup(t *testing.T) {
	origWin := isWindows
	isWindows = true
	t.Cleanup(func() { isWindows = origWin })

	// replaceSelf's Windows path: self -> self.old, tmp -> self.
	dir := t.TempDir()
	self := filepath.Join(dir, "roger.exe")
	tmp := filepath.Join(dir, "tmp.exe")
	_ = os.WriteFile(self, []byte("old"), 0o755)
	_ = os.WriteFile(tmp, []byte("fresh"), 0o755)
	if err := replaceSelf(self, tmp); err != nil {
		t.Fatalf("replaceSelf (windows) = %v", err)
	}
	if b, _ := os.ReadFile(self); string(b) != "fresh" {
		t.Error("windows replaceSelf should swap in the fresh binary")
	}

	// CleanupOld's Windows path: removes self.old (harmless if absent).
	origExec := executablePath
	executablePath = func() (string, error) { return self, nil }
	t.Cleanup(func() { executablePath = origExec })
	_ = os.WriteFile(self+".old", []byte("stale"), 0o644)
	CleanupOld()
	if _, err := os.Stat(self + ".old"); !os.IsNotExist(err) {
		t.Error("CleanupOld should remove the .old sidecar")
	}
}

// TestLatestErrorPaths covers latest()'s failure branches: a non-200 status and an
// undecodable body both surface as errors (Check returns Available=false).
func TestLatestErrorPaths(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer bad.Close()
	orig := latestReleaseURL
	latestReleaseURL = func() string { return bad.URL }
	defer func() { latestReleaseURL = orig }()
	if res, err := Check("1.0.0"); err == nil || res.Available {
		t.Errorf("Check on a 500 = %+v/%v, want error + not available", res, err)
	}

	garbage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("{not json"))
	}))
	defer garbage.Close()
	latestReleaseURL = func() string { return garbage.URL }
	if _, err := Check("1.0.0"); err == nil {
		t.Error("Check on undecodable JSON should error")
	}

	// Bad URL -> httpGet/downloadTo transport error.
	if _, err := downloadTo(nil, "http://127.0.0.1:0/nope"); err == nil {
		t.Error("downloadTo on an unreachable URL should error")
	}
}

// TestInstallAssetNoChecksums covers the install path when the release ships no
// SHA256SUMS asset: it installs with a "skipping checksum verification" warning.
func TestInstallAssetNoChecksums(t *testing.T) {
	payload := []byte("unverified binary")
	assetSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer assetSrv.Close()

	self := filepath.Join(t.TempDir(), "roger")
	_ = os.WriteFile(self, []byte("old"), 0o755)
	var buf strings.Builder
	if err := installAsset(self, assetSrv.URL, assetName(), "9.9.9", release{}, &buf); err != nil {
		t.Fatalf("installAsset (no sums) = %v, want nil", err)
	}
	if !strings.Contains(buf.String(), "skipping checksum") {
		t.Errorf("expected the no-checksum warning, got %q", buf.String())
	}
	if b, _ := os.ReadFile(self); string(b) != string(payload) {
		t.Error("install should proceed without checksums")
	}
}

// TestRefreshCache covers the background cache writer: it fetches the latest release
// (injected URL) and writes the cache file.
func TestRefreshCache(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	fakeReleaseServer(t, release{Tag: "v3.2.1"})
	refreshCache("1.0.0")
	b, err := os.ReadFile(cachePath())
	if err != nil {
		t.Fatalf("cache not written: %v", err)
	}
	var cf cacheFile
	_ = json.Unmarshal(b, &cf)
	if cf.Latest != "3.2.1" || cf.CheckedAt == 0 {
		t.Errorf("cache = %+v, want latest 3.2.1 + a timestamp", cf)
	}
}

// TestCachedNoticeAndCleanup covers the cache-backed notice (opt-out, fresh-cache-newer,
// fresh-cache-uptodate) and CleanupOld's non-Windows no-op.
func TestCachedNoticeAndCleanup(t *testing.T) {
	// Opt-out short-circuits.
	t.Setenv("ROGERAI_NO_UPDATE_CHECK", "1")
	if CachedNotice("1.0.0") != "" {
		t.Error("ROGERAI_NO_UPDATE_CHECK should suppress the notice")
	}
	t.Setenv("ROGERAI_NO_UPDATE_CHECK", "")

	// Point the cache at a temp dir and write a FRESH cache with a newer version.
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	writeCache := func(latest string) {
		b, _ := json.Marshal(cacheFile{CheckedAt: time.Now().Unix(), Latest: latest})
		_ = os.MkdirAll(filepath.Dir(cachePath()), 0o755)
		_ = os.WriteFile(cachePath(), b, 0o644)
	}
	writeCache("9.9.9")
	if n := CachedNotice("1.0.0"); n == "" {
		t.Error("a fresh cache with a newer version should render a notice")
	}
	writeCache("1.0.0")
	if n := CachedNotice("1.0.0"); n != "" {
		t.Errorf("a fresh up-to-date cache should render no notice, got %q", n)
	}

	// CleanupOld is a no-op on non-Windows (must not error/panic).
	CleanupOld()
	if runtime.GOOS != "windows" {
		// nothing to assert beyond "did not panic"
		_ = assetName()
	}
}
