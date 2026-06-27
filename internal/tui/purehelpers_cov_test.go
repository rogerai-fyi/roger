package tui

import (
	"os"
	"strings"
	"testing"
)

// TestFreqLabelShortBranches covers all three branches of freqLabelShort: empty ->
// "private", a "<freq> · <station>" composite trimmed to the freq part, and a separator-
// free string passed through (trimmed).
func TestFreqLabelShortBranches(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "private"},
		{"107.9 MHz · gpt-oss-20b", "107.9 MHz"},
		{"  plain-label  ", "plain-label"},
	}
	for _, c := range cases {
		if got := freqLabelShort(c.in); got != c.want {
			t.Errorf("freqLabelShort(%q)=%q want %q", c.in, got, c.want)
		}
	}
}

// TestRangeStrBranches covers rangeStr: an offline band is a bare "-", a single-station
// (or flat min==max) band shows one price, and a real spread renders "lo ~ hi".
func TestRangeStrBranches(t *testing.T) {
	off := rangeStr(band{online: false, minOut: 0.3, maxOut: 0.9})
	if off != "-" {
		t.Errorf("offline band rangeStr=%q want %q", off, "-")
	}
	single := rangeStr(band{online: true, stations: 1, minOut: 0.3, maxOut: 0.3})
	if single != money(0.3) {
		t.Errorf("single-station rangeStr=%q want %q", single, money(0.3))
	}
	flat := rangeStr(band{online: true, stations: 3, minOut: 0.3, maxOut: 0.3})
	if flat != money(0.3) {
		t.Errorf("flat-spread rangeStr=%q want %q", flat, money(0.3))
	}
	spread := rangeStr(band{online: true, stations: 2, minOut: 0.3, maxOut: 0.9})
	if want := money(0.3) + " ~ " + money(0.9); spread != want {
		t.Errorf("spread rangeStr=%q want %q", spread, want)
	}
}

// TestFmtCtxBranches covers fmtCtx: non-positive -> "-", >=1000 rounds to "<n>k", and a
// small window renders the bare integer.
func TestFmtCtxBranches(t *testing.T) {
	cases := []struct {
		in   int
		want string
	}{
		{0, "-"},
		{-5, "-"},
		{512, "512"},
		{1000, "1k"},
		{32768, "33k"}, // (32768+500)/1000 = 33
		{131072, "131k"},
	}
	for _, c := range cases {
		if got := fmtCtx(c.in); got != c.want {
			t.Errorf("fmtCtx(%d)=%q want %q", c.in, got, c.want)
		}
	}
}

// TestTrimZeroBranches: zero collapses to blank (the "no cap" affordance), a non-zero
// renders as %g.
func TestTrimZeroBranches(t *testing.T) {
	if got := trimZero(0); got != "" {
		t.Errorf("trimZero(0)=%q want blank", got)
	}
	if got := trimZero(2.5); got != "2.5" {
		t.Errorf("trimZero(2.5)=%q want %q", got, "2.5")
	}
}

// TestHwLabelOrBranches: a recognizable hw class returns its bucket label, an
// unknown/empty value falls back to the dim "-".
func TestHwLabelOrBranches(t *testing.T) {
	if got := hwLabelOr("RTX 4090"); got != "single-gpu" {
		t.Errorf("hwLabelOr(rtx)=%q want single-gpu", got)
	}
	if got := hwLabelOr(""); got != "-" {
		t.Errorf("hwLabelOr(empty)=%q want -", got)
	}
	if got := hwLabelOr("totally-bogus"); got != "-" {
		t.Errorf("hwLabelOr(unknown)=%q want -", got)
	}
}

// TestModelOrBranches: a nil *offer reads as "", a real one yields its Model.
func TestModelOrBranches(t *testing.T) {
	var nilO *offer
	if got := nilO.modelOr(); got != "" {
		t.Errorf("nil offer modelOr=%q want blank", got)
	}
	o := &offer{Model: "gpt-oss-20b"}
	if got := o.modelOr(); got != "gpt-oss-20b" {
		t.Errorf("offer modelOr=%q want gpt-oss-20b", got)
	}
}

// TestPreviewableToolBranches: the read-only + run_shell tools preview, write_file and
// anything unknown do not.
func TestPreviewableToolBranches(t *testing.T) {
	for _, tool := range []string{"list_dir", "read_file", "web_fetch", "run_shell"} {
		if !previewableTool(tool) {
			t.Errorf("%s should be previewable", tool)
		}
	}
	for _, tool := range []string{"write_file", "", "unknown_tool"} {
		if previewableTool(tool) {
			t.Errorf("%s should NOT be previewable", tool)
		}
	}
}

// TestWrapPlainBranches covers wrapPlain: n<1 collapses newlines to spaces on one line,
// CRLF normalizes, a blank line is preserved, and an over-long run hard-breaks at n.
func TestWrapPlainBranches(t *testing.T) {
	// n < 1 -> single line, newlines flattened to spaces.
	one := wrapPlain("a\nb\nc", 0)
	if len(one) != 1 || one[0] != "a b c" {
		t.Errorf("wrapPlain n<1 = %q want [\"a b c\"]", one)
	}
	// CRLF + blank line + a long run that must hard-break at width 4.
	got := wrapPlain("hi\r\n\r\nabcdefghij", 4)
	want := []string{"hi", "", "abcd", "efgh", "ij"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("wrapPlain hard-break = %q want %q", got, want)
	}
}

// TestResultHintBranches: empty/whitespace -> no hint, real content -> a byte count.
func TestResultHintBranches(t *testing.T) {
	if got := resultHint("   \n  "); got != "" {
		t.Errorf("resultHint(blank)=%q want empty", got)
	}
	if got := resultHint("hello"); got != " · 5 bytes" {
		t.Errorf("resultHint(hello)=%q want \" · 5 bytes\"", got)
	}
}

// TestSharePriceCellBranches: a FREE-prefixed cell renders live (green), any priced cell
// renders ember. Asserted on the ANSI-stripped text plus a style difference so it's not
// tautological.
func TestSharePriceCellBranches(t *testing.T) {
	free := sharePriceCell("FREE today")
	paid := sharePriceCell("$0.30")
	if stripANSI(free) != "FREE today" || stripANSI(paid) != "$0.30" {
		t.Fatalf("sharePriceCell text mangled: free=%q paid=%q", stripANSI(free), stripANSI(paid))
	}
	// The two must be styled differently (live vs ember); under default color they differ.
	if free == paid {
		t.Errorf("FREE and priced cells rendered identically: %q", free)
	}
	if free != stLive.Render("FREE today") {
		t.Errorf("FREE cell not live-styled: %q", free)
	}
}

// TestIsCharDeviceBranches: nil -> false, a regular file -> false, and /dev/null (a real
// character device on this platform) -> true. The Stat-error path is the closed-fd case.
func TestIsCharDeviceBranches(t *testing.T) {
	if isCharDevice(nil) {
		t.Error("nil should not be a char device")
	}
	tmp, err := os.CreateTemp(t.TempDir(), "reg")
	if err != nil {
		t.Fatal(err)
	}
	defer tmp.Close()
	if isCharDevice(tmp) {
		t.Error("a regular temp file should not be a char device")
	}
	dn, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer dn.Close()
	if !isCharDevice(dn) {
		t.Errorf("%s should be a character device", os.DevNull)
	}
	// A closed file Stats with an error -> false (the headless/closed-stream case).
	closed, err := os.CreateTemp(t.TempDir(), "closed")
	if err != nil {
		t.Fatal(err)
	}
	closed.Close()
	if isCharDevice(closed) {
		t.Error("a closed file should not be a char device")
	}
}
