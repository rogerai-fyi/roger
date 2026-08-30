package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The price ceiling is GLOBAL. registerPriceCeiling in cmd/rogerai-broker/pricesafety.go
// carries a comment saying so and warning that copy must not offer --private as the way
// around it: --private hides a station from the public market, it does not raise the cap.
//
// The TUI told operators the opposite - "lower it, or share PRIVATE" - so an operator who
// followed the advice hit the identical rejection at broker register, one step later and
// with less explanation. /pricing.html now denies it in public, which made the
// contradiction a user-visible one.
//
// Behavior first, then a source sweep: the sweep is what stops the sentence coming back in
// the next surface that needs a ceiling message.

func TestValidateEditorCeilingCopyDoesNotOfferPrivate(t *testing.T) {
	for _, tc := range []struct {
		name    string
		in, out string
	}{
		{"output over ceiling", "0", "150"},
		{"input over ceiling", "80", "0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &model{edPriceIn: tc.in, edPriceOut: tc.out}
			_, _, msg := m.validateEditor()
			if msg == "" {
				t.Fatalf("validateEditor accepted an over-ceiling price (%s in / %s out)", tc.in, tc.out)
			}
			// "private" appears legitimately in "public or private", so the thing to
			// forbid is private offered as the REMEDY, not the word.
			for _, escape := range []string{"or share private", "share private", "use --private", "go private"} {
				if strings.Contains(strings.ToLower(msg), escape) {
					t.Errorf("the rejection offers %q as an escape from a global ceiling:\n  %s", escape, msg)
				}
			}
			if !strings.Contains(msg, "public or private") {
				t.Errorf("the rejection does not say the ceiling binds every band:\n  %s", msg)
			}
		})
	}
}

// Every ceiling rejection in the package, not only the two in the share editor. The
// voice booth grew its own copy of the sentence, which is exactly how a corrected
// message comes back.
func TestNoCeilingMessageOffersPrivateAsAnEscape(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	checked := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(filepath.Clean(name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for i, line := range strings.Split(string(src), "\n") {
			if !strings.Contains(line, "ceiling") || strings.Contains(strings.TrimSpace(line), "//") {
				continue
			}
			checked++
			if strings.Contains(line, "share PRIVATE") {
				t.Errorf("%s:%d offers --private as a price-ceiling escape:\n  %s",
					name, i+1, strings.TrimSpace(line))
			}
		}
	}
	if checked == 0 {
		t.Fatal("swept no ceiling copy at all - the sweep is not looking where the messages are")
	}
}
