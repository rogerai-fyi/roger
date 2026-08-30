package tui

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The price ceiling is GLOBAL. registerPriceCeiling in cmd/rogerai-broker/pricesafety.go
// carries a comment saying so and warning that copy must not offer --private as the way
// around it: --private hides a station from the public market, it does not raise the cap.
// tunnel.go gates every register unconditionally, and features/bands pins the 400 on an
// over-ceiling --private, so an operator who takes the advice hits a hard rejection one
// step later with less explanation.
//
// The sentence had spread to four surfaces - the share editor, the voice booth, the web
// Console, and the manual - and correcting the two I happened to be looking at is what
// let the other two survive a round. So the sweep walks the whole repository rather than
// this package, and matches a family of phrasings rather than the one literal that
// existed when it was written.

// escapePhrases are ways of offering "go private" as the remedy for a price over the
// ceiling. The word "private" alone is legitimate - the corrected copy says the ceiling
// applies to every band, public or private - so what is forbidden is private as the FIX.
var escapePhrases = []string{
	"or share private",
	"share private",
	"share on a private band instead",
	"use --private",
	"go private",
	"they share --private",
	"private band instead",
}

func TestValidateEditorCeilingCopyDoesNotOfferPrivate(t *testing.T) {
	for _, tc := range []struct {
		name    string
		in, out string
		windows []SchedWindow
	}{
		{name: "output over ceiling", in: "0", out: "150"},
		{name: "input over ceiling", in: "80", out: "0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &model{edPriceIn: tc.in, edPriceOut: tc.out, edWindows: tc.windows}
			_, _, msg := m.validateEditor()
			if msg == "" {
				t.Fatalf("validateEditor accepted an over-ceiling price (%s in / %s out)", tc.in, tc.out)
			}
			assertNoEscape(t, msg)
			if !strings.Contains(msg, "public or private") {
				t.Errorf("the rejection does not say the ceiling binds every band:\n  %s", msg)
			}
		})
	}
}

// The per-window ceilings are a separate branch three lines below the base-price ones,
// and were left behind the first time this copy was corrected.
func TestValidateEditorWindowCeilingCopyMatches(t *testing.T) {
	for _, tc := range []struct {
		name string
		win  SchedWindow
	}{
		{"window output over ceiling", SchedWindow{Start: "09:00", End: "17:00", Out: 150}},
		{"window input over ceiling", SchedWindow{Start: "09:00", End: "17:00", In: 80}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &model{edPriceIn: "0", edPriceOut: "0", edWindows: []SchedWindow{tc.win}}
			_, _, msg := m.validateEditor()
			if msg == "" {
				t.Fatalf("validateEditor accepted an over-ceiling window price")
			}
			assertNoEscape(t, msg)
			if !strings.Contains(msg, "public or private") {
				t.Errorf("the window rejection does not say the ceiling binds every band:\n  %s", msg)
			}
		})
	}
}

// offersEscape reports the first escape phrase a line of copy contains. Markup is
// stripped first: the manual's version of the sentence read "they share
// <code>--private</code> instead", which no raw substring match would have found.
func offersEscape(line string) string {
	plain := strings.ToLower(stripMarkup(line))
	for _, escape := range escapePhrases {
		if strings.Contains(plain, escape) {
			return escape
		}
	}
	return ""
}

func stripMarkup(s string) string {
	var b strings.Builder
	depth := 0
	for _, r := range s {
		switch {
		case r == '<':
			depth++
		case r == '>' && depth > 0:
			depth--
			b.WriteRune(' ')
		case depth == 0:
			b.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

func assertNoEscape(t *testing.T, msg string) {
	t.Helper()
	if escape := offersEscape(msg); escape != "" {
		t.Errorf("the rejection offers %q as an escape from a global ceiling:\n  %s", escape, msg)
	}
}

// A sweep that cannot catch the strings that actually shipped is decoration. These are
// the four verbatim lines the audit found, one per surface.
func TestCeilingSweepCatchesTheStringsThatShipped(t *testing.T) {
	for _, shipped := range []string{
		`return 0, 0, fmt.Sprintf("output price $%.2f/1M is over the $%.0f/1M public ceiling - lower it, or share PRIVATE", out, editorMaxPriceOut)`,
		`m.vbErr = fmt.Sprintf("price $%s/1k chars is over the $%.0f/1M public ceiling - lower it, or share PRIVATE", trimFloat(perK), editorMaxPriceIn)`,
		`ceil.textContent = "Public ceiling: $" + priceStr(CEIL.in) + " in / $" + priceStr(CEIL.out) + " out per 1M tokens. Need to charge more? Share on a private band instead.";`,
		`<span class="man-plate__k">operator ceiling</span><span class="man-plate__v">the broker refuses to list a public node priced above <code>$100 / 1M</code> out - only a typo trips it; if an operator truly wants an unreachable price they share <code>--private</code> instead (§5)</span>`,
	} {
		if offersEscape(shipped) == "" {
			t.Errorf("the sweep would not have caught this line:\n  %s", shipped)
		}
	}
}

// TestNoSurfaceOffersPrivateAsACeilingEscape sweeps every source surface a person can
// read - Go, JS, HTML - not just this package. Four surfaces carried the sentence and
// two of them were only found by an auditor, so a package-scoped sweep is the wrong
// shape for this rule.
func TestNoSurfaceOffersPrivateAsACeilingEscape(t *testing.T) {
	root := repoRoot(t)
	skipDir := map[string]bool{
		".git": true, "dist": true, "node_modules": true, "vendor": true, "testdata": true,
	}
	checked := 0
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // an unreadable corner is not this test's business
		}
		if d.IsDir() {
			if skipDir[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		switch filepath.Ext(d.Name()) {
		case ".go", ".js", ".html":
		default:
			return nil
		}
		if strings.HasSuffix(d.Name(), "_test.go") {
			return nil // the specs quote the forbidden phrasings on purpose
		}
		body, err := os.ReadFile(p) //nolint:gosec // walking our own tree
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		for i, line := range strings.Split(string(body), "\n") {
			trimmed := strings.TrimSpace(line)
			// Comments explaining the rule necessarily name the thing they forbid.
			if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "#") ||
				strings.HasPrefix(trimmed, "*") || strings.HasPrefix(trimmed, "<!--") {
				continue
			}
			if !strings.Contains(strings.ToLower(line), "ceiling") {
				continue
			}
			checked++
			if escape := offersEscape(line); escape != "" {
				t.Errorf("%s:%d offers %q as a price-ceiling escape:\n  %s",
					rel, i+1, escape, trimmed)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if checked < 5 {
		t.Fatalf("swept only %d lines of ceiling copy - the sweep is not looking where the messages are", checked)
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the working directory")
		}
		dir = parent
	}
}
