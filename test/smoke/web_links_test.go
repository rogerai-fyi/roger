// Package smoke holds self-contained release integration tests that do NOT
// belong to any product package. They exercise the built artifacts (web/dist)
// the way scripts/smoke.sh does, but in pure Go so they also run under
// `go test ./...` (the regression suite) and gate CI.
//
// DO serves NO clean URLs: every page is "<name>.html" and every internal link
// must point at a file that exists in web/dist. A link to "/dashboard" (when only
// "/dashboard.html" exists) is a production 404. This test crawls every internal
// <a href> in the committed dist tree and fails if any link does not resolve to a
// real file, an in-page "#anchor", or an external URL. It also asserts the core
// set of pages is present.
//
// Run a fresh web build first (`make site`) so dist reflects web/src.
package smoke

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// distDir walks up from the test's working directory to the repo root and
// returns <root>/web/dist. The test package lives at test/smoke, so the repo
// root is two levels up.
func distDir(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// climb until we find go.mod (repo root), then append web/dist.
	dir := wd
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, "web", "dist")
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("could not locate repo root (go.mod) from %s", wd)
	return ""
}

var hrefRe = regexp.MustCompile(`href="([^"]+)"`)

// corePages must exist in every release. If a page is removed on purpose, update
// this list deliberately (that is the point of a regression gate).
var corePages = []string{
	"index.html", "manual.html", "login.html",
	"privacy.html", "tos.html", "security.html",
	"dashboard.html", "console.html", "billing.html",
	"usage.html", "account.html", "payouts.html",
	"404.html",
}

func TestDistCorePagesExist(t *testing.T) {
	dist := distDir(t)
	if _, err := os.Stat(dist); err != nil {
		t.Skipf("web/dist not built (%v); run `make site` first", err)
	}
	for _, p := range corePages {
		if _, err := os.Stat(filepath.Join(dist, p)); err != nil {
			t.Errorf("missing core page %s in web/dist (run `make site`?): %v", p, err)
		}
	}
}

// TestDistInternalLinksResolve crawls every <a href> across the built pages and
// asserts each internal link resolves to a file that exists. This is the
// clean-URL-404 guard: it catches links like "/dashboard" that 404 in prod
// because only "/dashboard.html" is served.
func TestDistInternalLinksResolve(t *testing.T) {
	dist := distDir(t)
	if _, err := os.Stat(dist); err != nil {
		t.Skipf("web/dist not built (%v); run `make site` first", err)
	}

	pages, err := filepath.Glob(filepath.Join(dist, "*.html"))
	if err != nil {
		t.Fatalf("glob dist: %v", err)
	}
	if len(pages) == 0 {
		t.Fatalf("no .html pages found in %s; run `make site`", dist)
	}

	type broken struct {
		page string
		href string
	}
	var bad []broken
	checked := 0

	for _, page := range pages {
		b, err := os.ReadFile(page)
		if err != nil {
			t.Fatalf("read %s: %v", page, err)
		}
		for _, m := range hrefRe.FindAllStringSubmatch(string(b), -1) {
			href := m[1]
			// skip external + in-page anchors + non-http schemes.
			switch {
			case strings.HasPrefix(href, "http://"),
				strings.HasPrefix(href, "https://"),
				strings.HasPrefix(href, "//"),
				strings.HasPrefix(href, "mailto:"),
				strings.HasPrefix(href, "tel:"),
				strings.HasPrefix(href, "javascript:"),
				strings.HasPrefix(href, "#"):
				continue
			}
			// strip #fragment and ?query.
			path := href
			if i := strings.IndexByte(path, '#'); i >= 0 {
				path = path[:i]
			}
			if i := strings.IndexByte(path, '?'); i >= 0 {
				path = path[:i]
			}
			if path == "" {
				continue
			}
			// resolve to a file under dist. All pages are top-level, so a
			// page-relative link resolves against dist too. A trailing "/" means
			// dir/index.html.
			rel := strings.TrimPrefix(path, "/")
			if strings.HasSuffix(rel, "/") || rel == "" {
				rel += "index.html"
			}
			target := filepath.Join(dist, rel)
			checked++
			if _, err := os.Stat(target); err != nil {
				bad = append(bad, broken{page: filepath.Base(page), href: href})
			}
		}
	}

	if checked == 0 {
		t.Fatal("no internal links checked; the crawl regex likely broke")
	}
	for _, b := range bad {
		t.Errorf("broken internal link in %s: %q does not resolve to a file in web/dist (clean-URL 404 risk; DO serves no clean URLs)", b.page, b.href)
	}
	t.Logf("crawled %d internal links across %d pages, %d broken", checked, len(pages), len(bad))
}
