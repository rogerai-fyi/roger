package tui

// marquee_bdd_test.go - the godog harness for features/tui/marquee.feature (the
// selected-row marquee). The steps drive the REAL helpers (padMarquee / cutMarquee /
// marqueePhase) and the REAL model (New + Update + View) - no mocks, no fakes, and no
// wall clock: time is the frame counter the TUI already runs on, advanced by hand.

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/cucumber/godog"
	"github.com/rivo/uniseg"
)

// the two names the model-level scenarios use: one that cannot fit any column the
// band table offers, and one that fits every one of them.
const (
	marqLongName  = "meta-llama/llama-3.1-70b-instruct-turbo-preview"
	marqShortName = "gpt-oss-20b"
)

type marqueeBDD struct {
	t *testing.T

	// the pure-helper scenarios
	name string
	w    int
	cut  bool // the truncVisible (hard-cut, no marker) flavour
	span int  // "a marquee with N columns to travel"

	// the model-level scenarios
	m tea.Model
}

func (b *marqueeBDD) reset() { *b = marqueeBDD{t: b.t} }

// cell renders the cell under test at off, in whichever flavour the scenario declared.
func (b *marqueeBDD) cell(off int) string {
	if b.cut {
		return cutMarquee(b.name, b.w, off)
	}
	return padMarquee(b.name, b.w, off)
}

// static is the cell exactly as the app renders it TODAY, with no marquee at all.
func (b *marqueeBDD) static() string {
	if b.cut {
		return truncVisible(b.name, b.w)
	}
	return pad(b.name, b.w)
}

func (b *marqueeBDD) travel() int { return marqueeTravel(b.name, b.w) }

// mm is the model-level scenario's model, as a concrete model.
func (b *marqueeBDD) mm() model {
	mm, ok := b.m.(model)
	if !ok {
		b.t.Fatalf("scenario has no model")
	}
	return mm
}

func (b *marqueeBDD) setModel(mm model) { b.m = mm }

// unescapeU turns the \uXXXX escapes a .feature file cannot express natively into real
// runes, so a combining-mark name can be written literally in the spec.
func unescapeU(s string) string {
	for {
		i := strings.Index(s, `\u`)
		if i < 0 || i+6 > len(s) {
			return s
		}
		n, err := strconv.ParseInt(s[i+2:i+6], 16, 32)
		if err != nil {
			return s
		}
		s = s[:i] + string(rune(n)) + s[i+6:]
	}
}

// --- Given: the pure cell -----------------------------------------------------------------

func (b *marqueeBDD) nameInCell(name string, w int) error {
	b.name, b.w, b.cut = unescapeU(name), w, false
	return nil
}

func (b *marqueeBDD) nameInCutCell(name string, w int) error {
	b.name, b.w, b.cut = unescapeU(name), w, true
	return nil
}

func (b *marqueeBDD) noColor() error       { b.t.Setenv("NO_COLOR", "1"); return nil }
func (b *marqueeBDD) asciiSet() error      { b.t.Setenv("ROGERAI_ASCII", "1"); return nil }
func (b *marqueeBDD) travelOf(n int) error { b.span = n; return nil }

// --- Given: the model ---------------------------------------------------------------------

// browseOn builds a logged-in BROWSE model on one long + one short band, wide enough for
// the full grid, with the cursor on the named model.
func (b *marqueeBDD) browseOn(want string) error {
	var m tea.Model = New("http://broker.local", "tester")
	m, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m, _ = m.Update(offersMsg([]offer{
		{NodeID: "a", Model: marqLongName, PriceIn: 0.10, PriceOut: 0.40, Online: true, Signal: 70},
		{NodeID: "b", Model: marqShortName, PriceIn: 0.20, PriceOut: 0.50, Online: true, Signal: 60},
	}))
	m, _ = m.Update(balanceMsg{balance: 100, loggedIn: true})
	mm := m.(model)
	for i, bd := range mm.visibleBands() {
		if bd.model == want {
			mm.cursor = i
		}
	}
	mm.syncSelected()
	mm.status = "" // an ambient toast would keep the clock animating on its own
	mm.syncMarquee()
	b.m = mm
	return nil
}

func (b *marqueeBDD) longBandSelected() error  { return b.browseOn(marqLongName) }
func (b *marqueeBDD) shortBandSelected() error { return b.browseOn(marqShortName) }

func (b *marqueeBDD) noBands() error {
	var m tea.Model = New("http://broker.local", "tester")
	m, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m, _ = m.Update(offersMsg(nil))
	mm := m.(model)
	mm.status = ""
	mm.syncMarquee()
	b.m = mm
	return nil
}

// shareOn builds a logged-in SHARE model with the cursor on the named local model.
func (b *marqueeBDD) shareOn(want string) error {
	mm := New("http://broker.local", "tester")
	mm.width, mm.height = 120, 30
	mm.mode = modeShare
	mm.setShareRows([]shareRow{
		{model: marqLongName, ctx: 32768},
		{model: marqShortName, ctx: 32768},
	})
	for i, r := range mm.shareRows {
		if r.model == want {
			mm.shareCursor = i
		}
	}
	mm.status = ""
	mm.syncMarquee()
	b.m = mm
	return nil
}

func (b *marqueeBDD) longShareSelected() error  { return b.shareOn(marqLongName) }
func (b *marqueeBDD) shortShareSelected() error { return b.shareOn(marqShortName) }

func (b *marqueeBDD) onChannel() error {
	mm := b.mm()
	mm.mode = modeChat
	mm.connected = &offer{NodeID: "a", Model: marqLongName, Online: true}
	mm.syncMarquee()
	b.setModel(mm)
	return nil
}

func (b *marqueeBDD) typingFilter() error {
	mm := b.mm()
	mm.mode = modeCommand
	mm.syncMarquee()
	b.setModel(mm)
	return nil
}

func (b *marqueeBDD) renamingStation() error {
	mm := b.mm()
	mm.renaming = true
	mm.syncMarquee()
	b.setModel(mm)
	return nil
}

func (b *marqueeBDD) windowshadeDown() error {
	mm := b.mm()
	mm.compact = true
	mm.frame = mm.marqFrame + marqueeHoldFrames + marqueeStepFrames*3
	b.setModel(mm)
	return nil
}

func (b *marqueeBDD) terminalWide(w int) error {
	m, _ := b.m.Update(tea.WindowSizeMsg{Width: w, Height: 30})
	b.m = m
	return nil
}

// scrolledOff walks the frame counter far enough past the start hold that the cell is
// demonstrably off column zero.
func (b *marqueeBDD) scrolledOff() error {
	mm := b.mm()
	mm.frame = mm.marqFrame + marqueeHoldFrames + marqueeStepFrames*3
	b.setModel(mm)
	if mm.marqueeOff() == 0 {
		return fmt.Errorf("the marquee is still at frame zero after %d frames", mm.frame-mm.marqFrame)
	}
	return nil
}

// --- When ---------------------------------------------------------------------------------

func (b *marqueeBDD) moveDown() error {
	m, _ := b.m.Update(tea.KeyMsg{Type: tea.KeyDown})
	b.m = m
	return nil
}

func (b *marqueeBDD) frameAdvances() error {
	mm := b.mm()
	m, _ := mm.Update(tickMsg{gen: mm.tickGen})
	b.m = m
	if b.mm().frame <= mm.frame {
		return fmt.Errorf("the frame clock did not advance (%d -> %d)", mm.frame, b.mm().frame)
	}
	return nil
}

// --- Then: the pure cell ------------------------------------------------------------------

func (b *marqueeBDD) frameZeroIsStatic() error {
	if got, want := b.cell(0), b.static(); got != want {
		return fmt.Errorf("frame 0 is NOT today's render:\n got %q\nwant %q", got, want)
	}
	return nil
}

func (b *marqueeBDD) frameZeroEndsEllipsis() error {
	if !strings.HasSuffix(b.cell(0), "…") {
		return fmt.Errorf("frame 0 %q does not end in an ellipsis", b.cell(0))
	}
	return nil
}

func (b *marqueeBDD) frameZeroNoEllipsis() error {
	if strings.Contains(b.cell(0), "…") {
		return fmt.Errorf("frame 0 %q carries an ellipsis", b.cell(0))
	}
	return nil
}

func (b *marqueeBDD) frameZeroPaddedTo(w int) error {
	c := b.cell(0)
	if lipgloss.Width(c) != w {
		return fmt.Errorf("frame 0 %q is %d columns, want %d", c, lipgloss.Width(c), w)
	}
	if !strings.HasSuffix(c, " ") {
		return fmt.Errorf("frame 0 %q is not space-padded", c)
	}
	return nil
}

func (b *marqueeBDD) everyOffsetIsStatic(lo, hi int) error {
	for off := lo; off <= hi; off++ {
		if got, want := b.cell(off), b.static(); got != want {
			return fmt.Errorf("offset %d moved: got %q want %q", off, got, want)
		}
	}
	return nil
}

func (b *marqueeBDD) travelIs(n int) error {
	if b.travel() != n {
		return fmt.Errorf("travel is %d, want %d", b.travel(), n)
	}
	return nil
}

func (b *marqueeBDD) offsetRenders(off int, want string) error {
	if got := b.cell(off); got != want {
		return fmt.Errorf("offset %d: got %q want %q", off, got, want)
	}
	return nil
}

func (b *marqueeBDD) everyOffsetIsExactly(lo, hi, cols int) error {
	for off := lo; off <= hi; off++ {
		if got := lipgloss.Width(b.cell(off)); got != cols {
			return fmt.Errorf("offset %d rendered %d columns (%q), want %d", off, got, b.cell(off), cols)
		}
	}
	return nil
}

func (b *marqueeBDD) lastOffsetRendersTrimmed(tail string, cols int) error {
	want := pad(tail, cols)
	if len([]rune(tail)) >= cols {
		want = string([]rune(tail)[len([]rune(tail))-cols:])
	}
	if got := b.cell(b.travel()); got != want {
		return fmt.Errorf("last offset (%d): got %q want %q", b.travel(), got, want)
	}
	return nil
}

func (b *marqueeBDD) lastOffsetNoEllipsis() error {
	if strings.HasSuffix(b.cell(b.travel()), "…") {
		return fmt.Errorf("the last offset %q still ends in an ellipsis", b.cell(b.travel()))
	}
	return nil
}

func (b *marqueeBDD) offsetsClampToLast(lo, hi int) error {
	last := b.cell(b.travel())
	for off := lo; off <= hi; off++ {
		if got := b.cell(off); got != last {
			return fmt.Errorf("offset %d ran past the tail: got %q want %q", off, got, last)
		}
	}
	return nil
}

func (b *marqueeBDD) noOffsetSplitsRune(lo, hi int) error {
	for off := lo; off <= hi; off++ {
		c := b.cell(off)
		if !isValidUTF8(c) {
			return fmt.Errorf("offset %d split a rune: %q", off, c)
		}
	}
	return nil
}

func (b *marqueeBDD) noOffsetSplitsCluster(lo, hi int) error {
	full := clusters(b.name)
	for off := lo; off <= hi; off++ {
		c := strings.TrimRight(strings.TrimSuffix(b.cell(off), "…"), " ")
		for _, g := range clusters(c) {
			if g == " " || g == "…" {
				continue
			}
			if !containsCluster(full, g) {
				return fmt.Errorf("offset %d split a grapheme cluster: %q is not a cluster of %q (cell %q)", off, g, b.name, b.cell(off))
			}
		}
	}
	return nil
}

func (b *marqueeBDD) noOffsetHasANSI(lo, hi int) error {
	for off := lo; off <= hi; off++ {
		if strings.Contains(b.cell(off), "\x1b") {
			return fmt.Errorf("offset %d carries an ANSI escape: %q", off, b.cell(off))
		}
	}
	return nil
}

func (b *marqueeBDD) noOffsetEndsEllipsis(lo, hi int) error {
	for off := lo; off <= hi; off++ {
		if strings.Contains(b.cell(off), "…") {
			return fmt.Errorf("offset %d carries an ellipsis: %q", off, b.cell(off))
		}
	}
	return nil
}

func (b *marqueeBDD) offsetDiffersFromZero(off int) error {
	if b.cell(off) == b.cell(0) {
		return fmt.Errorf("offset %d is identical to frame 0: %q", off, b.cell(0))
	}
	return nil
}

// --- Then: the rhythm ---------------------------------------------------------------------

func (b *marqueeBDD) offsetIsAtEvery(want, lo, hi int) error {
	for e := lo; e <= hi; e++ {
		if got := marqueePhase(e, b.span); got != want {
			return fmt.Errorf("elapsed %d: offset %d, want %d", e, got, want)
		}
	}
	return nil
}

func (b *marqueeBDD) holdsAtStart() error {
	for e := 0; e < marqueeHoldFrames; e++ {
		if got := marqueePhase(e, b.span); got != 0 {
			return fmt.Errorf("elapsed %d (inside the start hold): offset %d, want 0", e, got)
		}
	}
	if got := marqueePhase(marqueeHoldFrames, b.span); got == 0 {
		return fmt.Errorf("the start hold never ends: offset still 0 at elapsed %d", marqueeHoldFrames)
	}
	return nil
}

func (b *marqueeBDD) offsetAfterHold(want int) error {
	if got := marqueePhase(marqueeHoldFrames, b.span); got != want {
		return fmt.Errorf("first frame after the hold: offset %d, want %d", got, want)
	}
	return nil
}

func (b *marqueeBDD) oneColumnPerStep() error {
	for col := 1; col <= b.span; col++ {
		lo := marqueeHoldFrames + (col-1)*marqueeStepFrames
		for e := lo; e < lo+marqueeStepFrames; e++ {
			if got := marqueePhase(e, b.span); got != col {
				return fmt.Errorf("elapsed %d: offset %d, want column %d", e, got, col)
			}
		}
	}
	return nil
}

func (b *marqueeBDD) holdsAtEnd(want int) error {
	lo := marqueeHoldFrames + b.span*marqueeStepFrames
	for e := lo; e < lo+marqueeEndFrames; e++ {
		if got := marqueePhase(e, b.span); got != want {
			return fmt.Errorf("elapsed %d (inside the end hold): offset %d, want %d", e, got, want)
		}
	}
	return nil
}

func (b *marqueeBDD) wrapsToStart() error {
	cyc := marqueeCycle(b.span)
	if got := marqueePhase(cyc, b.span); got != 0 {
		return fmt.Errorf("one frame after the cycle (elapsed %d): offset %d, want 0", cyc, got)
	}
	return nil
}

func (b *marqueeBDD) cycleRepeats() error {
	cyc := marqueeCycle(b.span)
	for e := 0; e < cyc*2; e++ {
		if a, z := marqueePhase(e, b.span), marqueePhase(e+cyc, b.span); a != z {
			return fmt.Errorf("elapsed %d offset %d != elapsed %d offset %d (cycle %d)", e, a, e+cyc, z, cyc)
		}
	}
	return nil
}

func (b *marqueeBDD) offsetAtElapsed(want, e int) error {
	if got := marqueePhase(e, b.span); got != want {
		return fmt.Errorf("elapsed %d: offset %d, want %d", e, got, want)
	}
	return nil
}

// --- Then: the model ----------------------------------------------------------------------

func (b *marqueeBDD) marqueeRunning() error {
	if !b.mm().marqueeRunning() {
		return fmt.Errorf("the marquee is not running (travel %d)", b.mm().marqueeTravel())
	}
	return nil
}

func (b *marqueeBDD) marqueeNotRunning() error {
	if b.mm().marqueeRunning() {
		return fmt.Errorf("the marquee is running when it should be still (travel %d)", b.mm().marqueeTravel())
	}
	return nil
}

func (b *marqueeBDD) marqueeOffsetIs(want int) error {
	if got := b.mm().marqueeOff(); got != want {
		return fmt.Errorf("marquee offset %d, want %d", got, want)
	}
	return nil
}

func (b *marqueeBDD) marqueeOffsetIsNot(bad int) error {
	if got := b.mm().marqueeOff(); got == bad {
		return fmt.Errorf("marquee offset is %d and should not be", got)
	}
	return nil
}

// atFrame renders the ANSI-stripped view with the frame counter parked at marqFrame+e.
func (b *marqueeBDD) atFrame(e int) []string {
	mm := b.mm()
	mm.frame = mm.marqFrame + e
	return strings.Split(stripANSI(mm.View()), "\n")
}

// rowWith finds the one rendered line carrying the marker (the selection carat).
func rowWith(lines []string, want string) (string, bool) {
	for _, ln := range lines {
		if strings.Contains(ln, want) {
			return ln, true
		}
	}
	return "", false
}

func (b *marqueeBDD) selectedRowChanges() error {
	a := b.atFrame(0)
	z := b.atFrame(marqueeHoldFrames + marqueeStepFrames*3)
	if strings.Join(a, "\n") == strings.Join(z, "\n") {
		return fmt.Errorf("the view never changed as the frame advanced:\n%s", strings.Join(a, "\n"))
	}
	return nil
}

func (b *marqueeBDD) unselectedRowsStill() error {
	a := b.atFrame(0)
	z := b.atFrame(marqueeHoldFrames + marqueeStepFrames*3)
	if len(a) != len(z) {
		return fmt.Errorf("the view changed shape: %d lines -> %d", len(a), len(z))
	}
	changed := 0
	for i := range a {
		if a[i] != z[i] {
			changed++
			if !strings.Contains(a[i], marqShortName[:8]) {
				continue
			}
			return fmt.Errorf("an unselected row moved:\n %q\n %q", a[i], z[i])
		}
	}
	if changed == 0 {
		return fmt.Errorf("nothing moved at all - the selected row should have")
	}
	if changed > 1 {
		return fmt.Errorf("%d lines moved; only the selected row may", changed)
	}
	return nil
}

func (b *marqueeBDD) wholeViewStill() error {
	a := strings.Join(b.atFrame(0), "\n")
	for _, e := range []int{1, marqueeHoldFrames, marqueeHoldFrames + marqueeStepFrames*5, 200} {
		if z := strings.Join(b.atFrame(e), "\n"); z != a {
			return fmt.Errorf("the view moved at elapsed %d though nothing should animate", e)
		}
	}
	return nil
}

func (b *marqueeBDD) clockAnimating() error {
	if !b.mm().animating(false) {
		return fmt.Errorf("the frame clock is frozen; a running marquee must keep it alive")
	}
	return nil
}

func (b *marqueeBDD) clockNotAnimating() error {
	if b.mm().animating(false) {
		return fmt.Errorf("the frame clock is running though nothing is animating")
	}
	return nil
}

func (b *marqueeBDD) noLineOverflows() error {
	mm := b.mm()
	w := mm.width
	for e := 0; e <= marqueeHoldFrames+marqueeStepFrames*40+marqueeEndFrames; e += 3 {
		for i, ln := range b.atFrame(e) {
			if lipgloss.Width(ln) > w {
				return fmt.Errorf("elapsed %d line %d is %d columns > %d: %q", e, i, lipgloss.Width(ln), w, ln)
			}
		}
	}
	return nil
}

// --- small text helpers used by the assertions --------------------------------------------

func isValidUTF8(s string) bool {
	for _, r := range s {
		if r == '�' {
			return false
		}
	}
	return true
}

func clusters(s string) []string {
	var out []string
	g := uniseg.NewGraphemes(s)
	for g.Next() {
		out = append(out, g.Str())
	}
	return out
}

func containsCluster(all []string, g string) bool {
	for _, c := range all {
		if c == g {
			return true
		}
	}
	return false
}

func TestMarqueeBDD(t *testing.T) {
	st := &marqueeBDD{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset()
				return ctx, nil
			})
			sc.Step(`^the name "([^"]*)" in a (-?\d+)-column cell$`, st.nameInCell)
			sc.Step(`^the name "([^"]*)" in a (-?\d+)-column hard-cut cell$`, st.nameInCutCell)
			sc.Step(`^NO_COLOR is set$`, st.noColor)
			sc.Step(`^ROGERAI_ASCII is set$`, st.asciiSet)
			sc.Step(`^a marquee with (\d+) columns to travel$`, st.travelOf)

			sc.Step(`^a band list with a long-named band selected$`, st.longBandSelected)
			sc.Step(`^a band list with a short-named band selected$`, st.shortBandSelected)
			sc.Step(`^a band list with no bands$`, st.noBands)
			sc.Step(`^a SHARE table with a long-named model selected$`, st.longShareSelected)
			sc.Step(`^a SHARE table with a short-named model selected$`, st.shortShareSelected)
			sc.Step(`^the operator is on the CHANNEL screen$`, st.onChannel)
			sc.Step(`^the operator is typing a filter$`, st.typingFilter)
			sc.Step(`^the operator is renaming the station$`, st.renamingStation)
			sc.Step(`^the windowshade is down$`, st.windowshadeDown)
			sc.Step(`^the terminal is (\d+) columns wide$`, st.terminalWide)
			sc.Step(`^the marquee has scrolled off frame zero$`, st.scrolledOff)

			sc.Step(`^the operator moves the selection down one row$`, st.moveDown)
			sc.Step(`^the frame advances without moving the selection$`, st.frameAdvances)

			sc.Step(`^frame 0 is byte-identical to the static padded cell$`, st.frameZeroIsStatic)
			sc.Step(`^frame 0 is byte-identical to the static hard-cut cell$`, st.frameZeroIsStatic)
			sc.Step(`^frame 0 ends in an ellipsis$`, st.frameZeroEndsEllipsis)
			sc.Step(`^frame 0 does not end in an ellipsis$`, st.frameZeroNoEllipsis)
			sc.Step(`^frame 0 is padded with trailing spaces to (\d+) columns$`, st.frameZeroPaddedTo)
			sc.Step(`^every offset from (-?\d+) to (-?\d+) renders the static padded cell$`, st.everyOffsetIsStatic)
			sc.Step(`^the cell has (\d+) columns to travel$`, st.travelIs)
			sc.Step(`^offset (\d+) renders "([^"]*)"$`, st.offsetRenders)
			sc.Step(`^every offset from (-?\d+) to (-?\d+) renders exactly (\d+) columns$`, st.everyOffsetIsExactly)
			sc.Step(`^the last offset renders "([^"]*)" trimmed to (\d+) columns$`, st.lastOffsetRendersTrimmed)
			sc.Step(`^the last offset does not end in an ellipsis$`, st.lastOffsetNoEllipsis)
			sc.Step(`^every offset from (-?\d+) to (-?\d+) renders the same cell as the last offset$`, st.offsetsClampToLast)
			sc.Step(`^no offset from (-?\d+) to (-?\d+) splits a rune$`, st.noOffsetSplitsRune)
			sc.Step(`^no offset from (-?\d+) to (-?\d+) splits a grapheme cluster$`, st.noOffsetSplitsCluster)
			sc.Step(`^no offset from (-?\d+) to (-?\d+) carries an ANSI escape$`, st.noOffsetHasANSI)
			sc.Step(`^no offset from (-?\d+) to (-?\d+) ends in an ellipsis$`, st.noOffsetEndsEllipsis)
			sc.Step(`^offset (\d+) differs from frame 0$`, st.offsetDiffersFromZero)

			sc.Step(`^the offset is (\d+) at every elapsed frame from (-?\d+) to (-?\d+)$`, st.offsetIsAtEvery)
			sc.Step(`^the offset is 0 for the whole start hold$`, st.holdsAtStart)
			sc.Step(`^the offset is (\d+) on the first frame after the start hold$`, st.offsetAfterHold)
			sc.Step(`^the offset advances by exactly one column per step through the scroll$`, st.oneColumnPerStep)
			sc.Step(`^the offset is (\d+) for the whole end hold$`, st.holdsAtEnd)
			sc.Step(`^the offset is 0 again one frame after the cycle ends$`, st.wrapsToStart)
			sc.Step(`^the offset at every elapsed frame equals the offset one cycle later$`, st.cycleRepeats)
			sc.Step(`^the offset is (\d+) at elapsed frame (-?\d+)$`, st.offsetAtElapsed)

			sc.Step(`^the marquee is running$`, st.marqueeRunning)
			sc.Step(`^the marquee is not running$`, st.marqueeNotRunning)
			sc.Step(`^the marquee offset is (\d+)$`, st.marqueeOffsetIs)
			sc.Step(`^the marquee offset is not (\d+)$`, st.marqueeOffsetIsNot)
			sc.Step(`^the selected band row changes as the frame advances$`, st.selectedRowChanges)
			sc.Step(`^the selected SHARE row changes as the frame advances$`, st.selectedRowChanges)
			sc.Step(`^the unselected band rows are byte-identical as the frame advances$`, st.unselectedRowsStill)
			sc.Step(`^the whole band view is byte-identical as the frame advances$`, st.wholeViewStill)
			sc.Step(`^the frame clock is animating$`, st.clockAnimating)
			sc.Step(`^the frame clock is not animating$`, st.clockNotAnimating)
			sc.Step(`^no rendered line exceeds the terminal width at any offset$`, st.noLineOverflows)
		},
		Options: &godog.Options{Format: "pretty", Paths: []string{"../../features/tui/marquee.feature"}, TestingT: t, Strict: true},
	}
	if suite.Run() != 0 {
		t.Fatal("marquee scenarios failed (see godog output above)")
	}
}
