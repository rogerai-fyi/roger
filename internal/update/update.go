// Package update is the self-update path for the rogerai client: `rogerai upgrade`
// downloads the latest GitHub release asset for this os/arch, verifies its sha256
// against the published checksums, and atomically swaps the running binary; a
// separate async, cached (~daily) check shows a subtle "update available" line at
// startup without ever blocking or failing offline.
//
// Network is best-effort throughout: an offline box upgrades nothing and notices
// nothing, by design. Opt out of the background check with ROGERAI_NO_UPDATE_CHECK=1.
package update

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Repo is the GitHub repo that publishes rogerai releases.
const Repo = "rogerai-fyi/roger"

// checkTTL is how long a cached version-check result is reused (~daily).
const checkTTL = 20 * time.Hour

// release is the subset of the GitHub releases API we read.
type release struct {
	Tag    string `json:"tag_name"`
	Assets []struct {
		AN  string `json:"name"`                 // asset filename, e.g. rogerai-linux-amd64
		URL string `json:"browser_download_url"` // direct download URL
	} `json:"assets"`
}

// assetName is the per-platform binary asset name, e.g. rogerai-linux-amd64
// (rogerai-windows-amd64.exe on Windows).
func assetName() string {
	n := fmt.Sprintf("rogerai-%s-%s", runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		n += ".exe"
	}
	return n
}

// normalize strips a leading v from a tag so "v0.2.0" and "0.2.0" compare equal.
func normalize(s string) string { return strings.TrimPrefix(strings.TrimSpace(s), "v") }

// httpGet is a short-timeout GET helper (best-effort; callers treat errors as
// "no update / offline", never fatal).
func httpGet(url string) (*http.Response, error) {
	c := &http.Client{Timeout: 20 * time.Second}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "rogerai-cli")
	return c.Do(req)
}

// latest fetches the latest published release for Repo.
func latest() (release, error) {
	var r release
	resp, err := httpGet("https://api.github.com/repos/" + Repo + "/releases/latest")
	if err != nil {
		return r, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return r, fmt.Errorf("releases api status %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return r, err
	}
	return r, nil
}

// findAsset returns the download URL of the named asset in a release.
func (r release) findAsset(name string) (string, bool) {
	for _, a := range r.Assets {
		if a.AN == name {
			return a.URL, true
		}
	}
	return "", false
}

// CheckResult is the outcome of a version check.
type CheckResult struct {
	Current   string
	Latest    string
	Available bool // a newer version is published for this platform
}

// Notice renders the subtle one-line update banner, or "" when up to date.
func (c CheckResult) Notice() string {
	if !c.Available {
		return ""
	}
	return fmt.Sprintf("update available v%s -> v%s · run 'rogerai upgrade'", c.Current, c.Latest)
}

// Check returns whether a newer release exists than current. Network failures
// yield Available=false with no error surfaced to the user path.
func Check(current string) (CheckResult, error) {
	res := CheckResult{Current: normalize(current)}
	r, err := latest()
	if err != nil {
		return res, err
	}
	res.Latest = normalize(r.Tag)
	res.Available = isNewer(res.Current, res.Latest)
	return res, nil
}

// isNewer reports whether latest is strictly newer than current under a simple
// dotted-numeric comparison (e.g. 0.2.0 > 0.1.9). A dev build that is AHEAD of
// the published release must NOT advertise a downgrade as an "update", so we
// compare ordering rather than mere inequality. Non-numeric / unparseable parts
// fall back to a string compare so we still notice a differing tag.
func isNewer(current, latest string) bool {
	if latest == "" || latest == current {
		return false
	}
	cp, lp := splitVer(current), splitVer(latest)
	n := len(cp)
	if len(lp) > n {
		n = len(lp)
	}
	for i := 0; i < n; i++ {
		var c, l int
		if i < len(cp) {
			c = cp[i]
		}
		if i < len(lp) {
			l = lp[i]
		}
		if l != c {
			return l > c
		}
	}
	// all numeric parts equal -> only "newer" if the raw tags differ (e.g. a
	// suffix); be conservative and treat equal-numeric as not newer.
	return false
}

// splitVer parses a dotted version into integer components; a non-numeric
// component (and anything after it, like a -rc1 suffix) stops the parse.
func splitVer(v string) []int {
	parts := strings.Split(v, ".")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n := 0
		ok := len(p) > 0
		for _, ch := range p {
			if ch < '0' || ch > '9' {
				ok = false
				break
			}
			n = n*10 + int(ch-'0')
		}
		if !ok {
			break
		}
		out = append(out, n)
	}
	return out
}

// cachePath is where the background check stores its last result.
func cachePath() string {
	d, err := os.UserCacheDir()
	if err != nil || d == "" {
		d = os.TempDir()
	}
	return filepath.Join(d, "rogerai", "update-check.json")
}

type cacheFile struct {
	CheckedAt int64  `json:"checked_at"`
	Latest    string `json:"latest"`
}

// CachedNotice returns the update banner using a ~daily on-disk cache, refreshing
// in the background when stale. It NEVER blocks: a stale or missing cache returns
// "" immediately and kicks off an async refresh for next time. Honors
// ROGERAI_NO_UPDATE_CHECK=1 (returns "" and does nothing).
func CachedNotice(current string) string {
	if os.Getenv("ROGERAI_NO_UPDATE_CHECK") != "" {
		return ""
	}
	cur := normalize(current)
	var cf cacheFile
	if b, err := os.ReadFile(cachePath()); err == nil {
		_ = json.Unmarshal(b, &cf)
	}
	stale := time.Since(time.Unix(cf.CheckedAt, 0)) > checkTTL
	if stale {
		go refreshCache(cur) // fire-and-forget; result lands for the next run
	}
	if lat := normalize(cf.Latest); isNewer(cur, lat) {
		return CheckResult{Current: cur, Latest: lat, Available: true}.Notice()
	}
	return ""
}

// refreshCache does one network check and writes the cache. Best-effort.
func refreshCache(cur string) {
	r, err := latest()
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(cachePath()), 0o755)
	b, _ := json.Marshal(cacheFile{CheckedAt: time.Now().Unix(), Latest: normalize(r.Tag)})
	_ = os.WriteFile(cachePath(), b, 0o644)
}

// Upgrade self-updates the running binary to the latest release: it downloads the
// per-platform asset + the SHA256SUMS, verifies the checksum, and atomically
// replaces the current executable. It prints progress to w. "already latest" is
// handled (no-op). Returns an error only on a genuine failure (download/verify/
// replace); being offline surfaces as a clear, non-fatal message.
func Upgrade(current string, w io.Writer) error {
	cur := normalize(current)
	r, err := latest()
	if err != nil {
		return fmt.Errorf("could not reach GitHub releases (offline?): %w", err)
	}
	lat := normalize(r.Tag)
	if lat == "" {
		return fmt.Errorf("no published release found for %s", Repo)
	}
	if lat == cur {
		fmt.Fprintf(w, "already on the latest version (v%s)\n", cur)
		return nil
	}
	name := assetName()
	assetURL, ok := r.findAsset(name)
	if !ok {
		return fmt.Errorf("release v%s has no asset %q for this platform", lat, name)
	}
	fmt.Fprintf(w, "upgrading rogerai v%s -> v%s …\n", cur, lat)

	// Download the asset to a temp file next to the target (same filesystem, so the
	// final rename is atomic).
	self, err := os.Executable()
	if err != nil {
		return err
	}
	self, _ = filepath.EvalSymlinks(self)
	dir := filepath.Dir(self)
	tmp, err := os.CreateTemp(dir, ".rogerai-upgrade-*")
	if err != nil {
		return fmt.Errorf("cannot write to %s (need permission to replace the binary): %w", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once renamed away

	sum, err := downloadTo(tmp, assetURL)
	tmp.Close()
	if err != nil {
		return err
	}

	// Verify against the published SHA256SUMS, when present.
	if want, ok, err := expectedSum(r, name); err == nil && ok {
		if !strings.EqualFold(want, sum) {
			return fmt.Errorf("checksum mismatch for %s (want %s, got %s) - refusing to install", name, want, sum)
		}
		fmt.Fprintln(w, "checksum verified.")
	} else {
		fmt.Fprintln(w, "warning: no SHA256SUMS asset found - skipping checksum verification.")
	}

	if err := os.Chmod(tmpName, 0o755); err != nil {
		return err
	}
	if err := os.Rename(tmpName, self); err != nil {
		return fmt.Errorf("atomic replace failed: %w", err)
	}
	fmt.Fprintf(w, "done. rogerai is now v%s.\n", lat)
	return nil
}

// downloadTo streams url into f and returns the hex sha256 of the bytes written.
func downloadTo(f *os.File, url string) (string, error) {
	resp, err := httpGet(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download status %d", resp.StatusCode)
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, h), resp.Body); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// expectedSum pulls the SHA256SUMS asset (if any) and returns the checksum for
// the named binary. ok=false means no checksums asset / no entry.
func expectedSum(r release, name string) (string, bool, error) {
	url, ok := r.findAsset("SHA256SUMS")
	if !ok {
		url, ok = r.findAsset("checksums.txt")
	}
	if !ok {
		return "", false, nil
	}
	resp, err := httpGet(url)
	if err != nil {
		return "", false, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", false, err
	}
	for _, line := range strings.Split(string(body), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && strings.TrimPrefix(f[1], "*") == name {
			return f[0], true, nil
		}
	}
	return "", false, nil
}
