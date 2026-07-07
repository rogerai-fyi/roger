package tui

// operator_steps_phase3_test.go - godog step definitions for the Guest Operators
// Phase 3 spec set (features/operator/desk_view|desk_strip|band_gate|prelaunch_plate|
// plate_budget|plate_workdir.feature): THE DESK roster + strip on the AGENT landing,
// the agent-ready band gate, and the pre-launch plate. They extend the Phase 2 opBDD
// harness - the REAL bubbletea model, the REAL hardened proxy over a stub billing
// broker, a REAL client.RCBridge - with the only new seam being operatorWorkdir
// (agentRoot in production, the scenario sandbox here). No mocks.

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/cucumber/godog"
	"github.com/muesli/termenv"
	"github.com/rogerai-fyi/roger/internal/operator"
	"github.com/rogerai-fyi/roger/internal/pricetier"
	"github.com/rogerai-fyi/roger/internal/protocol"
)

// --- render access helpers ------------------------------------------------------------------

func (s *opBDD) tuiWidth() int {
	if w := s.model().width; w > 0 {
		return w
	}
	return 120
}

func (s *opBDD) agentViewText() string { return stripANSI(s.model().agentView(s.tuiWidth())) }

func (s *opBDD) rosterRaw() string { return s.model().deskRosterBlock(s.tuiWidth()) }
func (s *opBDD) roster() string    { return stripANSI(s.rosterRaw()) }

func (s *opBDD) rosterLines() []string {
	var out []string
	for _, ln := range strings.Split(s.roster(), "\n") {
		if strings.TrimSpace(ln) != "" {
			out = append(out, ln)
		}
	}
	return out
}

// rosterRowFor finds the roster line whose first cell is the given operator name.
func (s *opBDD) rosterRowFor(name string) (string, error) {
	for _, ln := range s.rosterLines() {
		t := strings.TrimSpace(ln)
		t = strings.TrimPrefix(t, glyphOnAir+" ") // the DJ row leads with the on-air mark
		if strings.HasPrefix(t, name+" ") || t == name {
			return ln, nil
		}
	}
	return "", fmt.Errorf("no roster row for %q in:\n%s", name, s.roster())
}

func (s *opBDD) stripRaw() string { return s.model().deskStripLine(s.tuiWidth()) }
func (s *opBDD) stripLine() string {
	return strings.TrimRight(stripANSI(s.stripRaw()), "\n")
}

func (s *opBDD) plateRaw() string { return s.model().operatorPlateView(s.tuiWidth()) }
func (s *opBDD) plateText() string {
	if s.model().operatorPlate == nil {
		return ""
	}
	return stripANSI(s.plateRaw())
}

// withTrueColor renders f under a forced TrueColor profile (off a TTY every style strips
// to plain) so red/reverse assertions are distinguishable, then restores the profile.
func withTrueColor(f func()) {
	r := lipgloss.DefaultRenderer()
	old := r.ColorProfile()
	r.SetColorProfile(termenv.TrueColor)
	defer r.SetColorProfile(old)
	f()
}

// --- shared Givens ----------------------------------------------------------------------------

func (s *opBDD) detectedGuestsThree(a, b, c string) error {
	for _, n := range []string{a, b, c} {
		if err := s.addDetected(n); err != nil {
			return err
		}
	}
	return nil
}

// detectedGuestUnproven replaces (or adds) the named detection as UNVERIFIED with the
// given probed version - the §8 version-skew honesty seam.
func (s *opBDD) detectedGuestUnproven(name, version string) error {
	g, err := registryGuest(name)
	if err != nil {
		return err
	}
	s.tuiPaths[g.Bin] = "/fake/" + g.Bin
	d := operator.Detection{Guest: g, Path: "/fake/" + g.Bin, Version: version, Unverified: true}
	s.mutate(func(m *model) {
		for i := range m.operatorDetections {
			if m.operatorDetections[i].Guest.Name == name {
				m.operatorDetections[i] = d
				return
			}
		}
		m.operatorDetections = append(m.operatorDetections, d)
	})
	return nil
}

// detectedGuestLaunchable adds a launchable non-registry guest (the P1 pin uses a
// launchable "claude" to prove the expectation line never implies vendor quality).
func (s *opBDD) detectedGuestLaunchable(name string) error {
	s.mutate(func(m *model) {
		m.operatorDetections = append(m.operatorDetections, operator.Detection{
			Guest: operator.Guest{Name: name, Bin: name, Provider: "anthropic",
				InstallHint: "n/a", KnownGood: "2.1.201", Strategy: operator.StrategyEnvFlags},
			Path: "/fake/" + name, Version: "2.1.201",
		})
	})
	return nil
}

func (s *opBDD) noModelTuned() error {
	if s.holder != nil {
		s.holder.Disconnect()
	}
	s.mutate(func(m *model) { m.connected = nil })
	return nil
}

func (s *opBDD) deskScanNotLanded() error {
	if ds := s.model().operatorDetections; len(ds) != 0 {
		return fmt.Errorf("detections unexpectedly present before the scan: %v", ds)
	}
	return nil
}

// rescanDesk delivers a REAL desk scan through the seamed detect env (exactly what the
// async operatorScanCmd produces) so the model folds the fresh PATH truth in.
func (s *opBDD) rescanDesk() { s.update(operatorScanCmd()()) }

func (s *opBDD) allGuestsDisappearRescan() error {
	s.tuiPaths = map[string]string{}
	s.rescanDesk()
	return nil
}

func (s *opBDD) guestDisappearsRescan(name string) error {
	g, err := registryGuest(name)
	if err != nil {
		return err
	}
	delete(s.tuiPaths, g.Bin)
	s.rescanDesk()
	return nil
}

func (s *opBDD) transcriptHasLines() error {
	s.mutate(func(m *model) {
		m.agentLines = append(m.agentLines, stDim.Render("· ")+stDim.Render("earlier chatter"))
	})
	return nil
}

func (s *opBDD) confirmPending() error {
	s.mutate(func(m *model) {
		m.agentPendingConfirm = &agentConfirm{tool: "write_file", args: map[string]any{"path": "x"}, resp: make(chan bool, 1)}
	})
	return nil
}

func (s *opBDD) terminalWidth(w int) error {
	s.mutate(func(m *model) { m.width = w })
	return nil
}

func (s *opBDD) colorDisabled() error {
	// Off a TTY the profile is Ascii already; pin it explicitly for the scenario (the
	// same stripping NO_COLOR applies in production).
	lipgloss.DefaultRenderer().SetColorProfile(termenv.Ascii)
	return nil
}

func (s *opBDD) compactActive() error {
	s.mutate(func(m *model) { m.compact = true })
	return nil
}

func (s *opBDD) compactExpanded() error {
	s.mutate(func(m *model) { m.compact = false })
	return nil
}

func (s *opBDD) userSubmitsPrompt(text string) error {
	s.tm = typeRunes(s.tm, text)
	s.update(keyMsg("enter")) // the turn Cmd is deliberately not executed - no real relay
	return nil
}

func (s *opBDD) askPromptHasFocus() error {
	m := s.model()
	if m.mode != modeAgent {
		return fmt.Errorf("not in AGENT mode (mode=%d)", m.mode)
	}
	if m.operatorPicker || m.operatorPlate != nil || m.agentPendingConfirm != nil {
		return fmt.Errorf("a modal owns the keys (picker=%v plate=%v)", m.operatorPicker, m.operatorPlate != nil)
	}
	if !m.agentIn.Focused() {
		return fmt.Errorf("the ask prompt lost focus")
	}
	return nil
}

func (s *opBDD) askPromptEchoes(text string) error {
	if got := s.model().agentIn.Value(); got != text {
		return fmt.Errorf("ask prompt echoes %q, want %q", got, text)
	}
	return nil
}

// --- desk_view steps ----------------------------------------------------------------------------

func (s *opBDD) landingRendersRoster() error {
	if s.rosterRaw() == "" {
		return fmt.Errorf("deskRosterBlock is empty:\n%s", s.agentViewText())
	}
	if !strings.Contains(s.agentViewText(), "THE DESK") {
		return fmt.Errorf("the AGENT view does not render THE DESK:\n%s", s.agentViewText())
	}
	return nil
}

func (s *opBDD) rosterHeadingReads(text string) error {
	if lines := s.rosterLines(); len(lines) == 0 || !strings.Contains(lines[0], text) {
		return fmt.Errorf("roster heading lacks %q:\n%s", text, s.roster())
	}
	return nil
}

func (s *opBDD) rosterSubtitleReads(text string) error { return s.rosterHeadingReads(text) }

func (s *opBDD) rosterSubtitleNamesNoModel() error {
	if lines := s.rosterLines(); len(lines) == 0 || strings.Contains(lines[0], "who can take the mic on") {
		return fmt.Errorf("roster subtitle names a model:\n%s", s.roster())
	}
	return nil
}

func (s *opBDD) agentViewNeverContains(text string) error {
	if strings.Contains(s.agentViewText(), text) {
		return fmt.Errorf("the AGENT view contains %q:\n%s", text, s.agentViewText())
	}
	return nil
}

// agentViewByteIdenticalPreDesk executes the permanent zero-guest pin: the desk chrome
// must be PURELY ADDITIVE. Rendering the SAME model with a guest detected and removing
// exactly the strip line + roster block must reproduce the zero-guest view byte-for-byte
// - so the zero-guest screen IS the pre-desk screen, with zero new chrome.
func (s *opBDD) agentViewByteIdenticalPreDesk() error {
	m := s.model()
	if len(m.operatorDetections) != 0 {
		return fmt.Errorf("the byte-identity pin is a zero-guest claim")
	}
	w := s.tuiWidth()
	zero := m.agentView(w)
	wg := m
	wg.operatorDetections = []operator.Detection{{Guest: operator.Registry()[0], Path: "/fake/opencode", Version: "1.17.11"}}
	gv := wg.agentView(w)
	expect := gv
	if strip := wg.deskStripLine(w); strip != "" {
		expect = strings.Replace(expect, strip, "", 1)
	}
	if roster := wg.deskRosterBlock(w); roster != "" {
		expect = strings.Replace(expect, roster, "", 1)
	}
	if cnt := wg.deskCompactCount(); cnt != "" {
		expect = strings.Replace(expect, cnt, "", 1)
	}
	if zero != expect {
		return fmt.Errorf("the desk chrome is not purely additive - the zero-guest view is not the pre-desk view:\nzero-guest:\n%s\nwith-guest minus desk chrome:\n%s",
			stripANSI(zero), stripANSI(expect))
	}
	return nil
}

func (s *opBDD) landingRendersNoDeskChrome() error {
	v := s.agentViewText()
	if strings.Contains(v, "THE DESK") || strings.Contains(v, "at the desk") {
		return fmt.Errorf("desk chrome rendered:\n%s", v)
	}
	return nil
}

func (s *opBDD) firstRosterRowIsDJ() error {
	lines := s.rosterLines()
	if len(lines) < 3 {
		return fmt.Errorf("roster too short:\n%s", s.roster())
	}
	// lines[0] heading, lines[1] column header, lines[2] the first row.
	if !strings.Contains(lines[2], "DJ") || !strings.Contains(lines[2], "resident") {
		return fmt.Errorf("the first roster row is not the DJ row: %q", lines[2])
	}
	return nil
}

func (s *opBDD) djRowCarriesRedMark() error {
	var block, token string
	withTrueColor(func() {
		block = s.rosterRaw()
		token = stRed.Render(glyphOnAir)
	})
	for _, ln := range strings.Split(block, "\n") {
		if strings.Contains(stripANSI(ln), "resident") {
			if !strings.Contains(ln, token) {
				return fmt.Errorf("the DJ row does not carry the red %s mark: %q", glyphOnAir, ln)
			}
			return nil
		}
	}
	return fmt.Errorf("no DJ row found in the roster:\n%s", stripANSI(block))
}

func (s *opBDD) djRowReads(text string) error {
	row, err := s.rosterRowFor("DJ")
	if err != nil {
		return err
	}
	if !strings.Contains(row, text) {
		return fmt.Errorf("DJ row %q lacks %q", row, text)
	}
	return nil
}

func (s *opBDD) rosterRowShowsWire(name, wire string) error {
	row, err := s.rosterRowFor(name)
	if err != nil {
		return err
	}
	if !strings.Contains(row, wire) {
		return fmt.Errorf("roster row %q lacks wire %q", row, wire)
	}
	return nil
}

func (s *opBDD) rosterRowShowsStatus(name, status string) error {
	return s.rosterRowShowsWire(name, status)
}

func (s *opBDD) rosterGuestRowsInOrder(names ...string) error {
	last := -1
	for _, n := range names {
		found := -1
		for i, ln := range s.rosterLines() {
			t := strings.TrimSpace(ln)
			if strings.HasPrefix(t, n+" ") && strings.Contains(ln, "guest ·") {
				found = i
				break
			}
		}
		if found < 0 {
			return fmt.Errorf("no guest roster row for %q:\n%s", n, s.roster())
		}
		if found <= last {
			return fmt.Errorf("guest row %q out of order:\n%s", n, s.roster())
		}
		last = found
	}
	return nil
}

func (s *opBDD) rosterGuestRowsTwo(a, b string) error      { return s.rosterGuestRowsInOrder(a, b) }
func (s *opBDD) rosterGuestRowsThree(a, b, c string) error { return s.rosterGuestRowsInOrder(a, b, c) }

func (s *opBDD) rosterRowVersionUnproven(name string) error {
	row, err := s.rosterRowFor(name)
	if err != nil {
		return err
	}
	if !strings.Contains(row, "unproven") {
		return fmt.Errorf("roster row %q carries no version-unproven note", row)
	}
	return nil
}

func (s *opBDD) rosterExactlyOneNotInstalled() error {
	if n := strings.Count(s.roster(), "not at the desk"); n != 1 {
		return fmt.Errorf("not-installed rows = %d, want exactly 1:\n%s", n, s.roster())
	}
	return nil
}

func (s *opBDD) notInstalledRowIsLast() error {
	lines := s.rosterLines()
	if len(lines) == 0 || !strings.Contains(lines[len(lines)-1], "not at the desk") {
		return fmt.Errorf("the last roster row is not the not-installed row:\n%s", s.roster())
	}
	return nil
}

func (s *opBDD) notInstalledRowShowsHint() error {
	for _, ln := range s.rosterLines() {
		if !strings.Contains(ln, "not at the desk · get it: ") {
			continue
		}
		for _, g := range operator.Registry() {
			if strings.Contains(ln, g.Name) && strings.Contains(ln, g.InstallHint) {
				return nil
			}
		}
		return fmt.Errorf("the not-installed row carries no registry install hint: %q", ln)
	}
	return fmt.Errorf("no not-installed row in:\n%s", s.roster())
}

func (s *opBDD) rosterNoNotInstalledRow() error {
	if strings.Contains(s.roster(), "not at the desk") {
		return fmt.Errorf("a healthy desk must advertise nothing:\n%s", s.roster())
	}
	return nil
}

func (s *opBDD) noRosterRowCarat() error {
	if strings.Contains(s.roster(), "▸") {
		return fmt.Errorf("a roster row carries a carat - the roster is a static preview:\n%s", s.roster())
	}
	return nil
}

func (s *opBDD) noRosterRowReverseVideo() error {
	var block string
	withTrueColor(func() { block = s.rosterRaw() })
	if strings.Contains(block, "\x1b[7m") {
		return fmt.Errorf("a roster row is rendered reverse-video")
	}
	return nil
}

func (s *opBDD) rosterCollapses() error { return s.landingRendersNoRoster() }

func (s *opBDD) landingRendersNoRoster() error {
	if strings.Contains(s.agentViewText(), "THE DESK") {
		return fmt.Errorf("the roster still renders:\n%s", s.agentViewText())
	}
	return nil
}

func (s *opBDD) rosterAtMostOnce() error {
	if n := strings.Count(s.agentViewText(), "THE DESK"); n > 1 {
		return fmt.Errorf("THE DESK rendered %d times", n)
	}
	return nil
}

func (s *opBDD) stagedPaintNoRoster() error {
	if h := s.model().operatorHandoff; h == nil {
		return fmt.Errorf("no handoff is staged/execing")
	}
	return s.landingRendersNoRoster()
}

func (s *opBDD) noAgentViewLineExceedsWidth() error {
	m := s.model()
	w := s.tuiWidth()
	for _, ln := range strings.Split(m.agentView(w), "\n") {
		if lw := lipgloss.Width(ln); lw > w {
			return fmt.Errorf("line %d cols > width %d: %q", lw, w, stripANSI(ln))
		}
	}
	return nil
}

func (s *opBDD) rosterNoANSI() error {
	if strings.Contains(s.rosterRaw(), "\x1b") {
		return fmt.Errorf("the roster emitted ANSI with color disabled")
	}
	return nil
}

func (s *opBDD) djRowPlainMark() error {
	row, err := s.rosterRowFor("DJ")
	if err != nil {
		return err
	}
	if !strings.Contains(row, glyphOnAir) {
		return fmt.Errorf("the DJ row lost the %s mark under NO_COLOR: %q", glyphOnAir, row)
	}
	return nil
}

// --- desk_strip steps -----------------------------------------------------------------------------

func (s *opBDD) headingShowsStrip() error {
	if s.stripRaw() == "" {
		return fmt.Errorf("deskStripLine is empty")
	}
	if !strings.Contains(s.agentViewText(), "at the desk:") {
		return fmt.Errorf("the AGENT view does not carry the desk strip:\n%s", s.agentViewText())
	}
	return nil
}

func (s *opBDD) stripReads(text string) error {
	if !strings.Contains(s.stripLine(), text) {
		return fmt.Errorf("the desk strip %q lacks %q", s.stripLine(), text)
	}
	return nil
}

func (s *opBDD) stripLeadsWithRedMark() error {
	var raw, token string
	withTrueColor(func() {
		raw = s.stripRaw()
		token = stRed.Render(glyphOnAir)
	})
	if !strings.HasPrefix(raw, "  "+token) {
		return fmt.Errorf("the strip does not lead with the red %s mark: %q", glyphOnAir, stripANSI(raw))
	}
	return nil
}

func (s *opBDD) stripDoesNotName(name string) error {
	if strings.Contains(s.stripLine(), name) {
		return fmt.Errorf("the desk strip names %q: %q", name, s.stripLine())
	}
	return nil
}

func (s *opBDD) switchToBandBrowser() error {
	s.pressKey("esc") // esc exits AGENT back to the band browser
	if s.model().mode == modeAgent {
		return fmt.Errorf("still in AGENT mode after esc")
	}
	return nil
}

func (s *opBDD) bandBrowserNeverContains(text string) error {
	if v := stripANSI(s.model().View()); strings.Contains(v, text) {
		return fmt.Errorf("the band browser view contains %q:\n%s", text, v)
	}
	return nil
}

func (s *opBDD) compactHeadingFirstLine() string {
	return strings.SplitN(s.model().agentView(s.tuiWidth()), "\n", 2)[0]
}

func (s *opBDD) compactHeadingReads(text string) error {
	if head := stripANSI(s.compactHeadingFirstLine()); !strings.Contains(head, text) {
		return fmt.Errorf("compact heading %q lacks %q", head, text)
	}
	return nil
}

func (s *opBDD) compactNoFullStrip() error {
	if strings.Contains(s.agentViewText(), "the DJ has the mic") {
		return fmt.Errorf("the compact view renders the full desk strip:\n%s", s.agentViewText())
	}
	return nil
}

func (s *opBDD) compactHeadingNeverContains(text string) error {
	if head := stripANSI(s.compactHeadingFirstLine()); strings.Contains(head, text) {
		return fmt.Errorf("compact heading contains %q: %q", text, head)
	}
	return nil
}

// compactHeadingByteIdentical executes the windowshade half of the zero-guest pin: the
// compact heading with zero guests must equal the with-guest heading minus exactly the
// " · N at the desk" fold.
func (s *opBDD) compactHeadingByteIdentical() error {
	m := s.model()
	if len(m.operatorDetections) != 0 {
		return fmt.Errorf("the byte-identity pin is a zero-guest claim")
	}
	zero := s.compactHeadingFirstLine()
	wg := m
	wg.operatorDetections = []operator.Detection{{Guest: operator.Registry()[0], Path: "/fake/opencode", Version: "1.17.11"}}
	gv := strings.SplitN(wg.agentView(s.tuiWidth()), "\n", 2)[0]
	expect := strings.Replace(gv, wg.deskCompactCount(), "", 1)
	if zero != expect {
		return fmt.Errorf("the compact count is not purely additive:\nzero:  %q\nminus: %q", stripANSI(zero), stripANSI(expect))
	}
	return nil
}

func (s *opBDD) stripNoANSI() error {
	if strings.Contains(s.stripRaw(), "\x1b") {
		return fmt.Errorf("the desk strip emitted ANSI with color disabled")
	}
	return nil
}

// --- band_gate steps --------------------------------------------------------------------------

// setStation puts a station offer on the OPEN CHANNEL (m.connected) - the station the
// gate reads and the guest would be patched into.
func (s *opBDD) setStation(ctx int, est bool) error {
	s.mutate(func(m *model) {
		mdl := "qwen3-32b-fp8"
		if m.proxyHolder != nil {
			mdl = m.proxyHolder.Get().Model
		}
		m.connected = &offer{NodeID: "KDGPU-7", Model: mdl, Online: true, TPS: 62,
			PriceIn: 0.2, PriceOut: 0.3, PriceTier: 1, Ctx: ctx, CtxEstimated: est}
	})
	return nil
}

func (s *opBDD) stationCtx(n int) error          { return s.setStation(n, false) }
func (s *opBDD) stationCtxEstimated(n int) error { return s.setStation(n, true) }
func (s *opBDD) stationCtxUnknown() error        { return s.setStation(0, false) }

func (s *opBDD) stationServes(tps int, in, out string) error {
	pin, _ := strconv.ParseFloat(in, 64)
	pout, _ := strconv.ParseFloat(out, 64)
	s.mutate(func(m *model) {
		if m.connected == nil {
			return
		}
		m.connected.TPS = float64(tps)
		m.connected.PriceIn, m.connected.PriceOut = pin, pout
	})
	if s.model().connected == nil {
		return fmt.Errorf("no open-channel station to describe - declare a context window first")
	}
	return nil
}

func (s *opBDD) bandHasAnotherStation(ctx int) error {
	s.mutate(func(m *model) {
		mdl := "qwen3-32b-fp8"
		if m.proxyHolder != nil {
			mdl = m.proxyHolder.Get().Model
		}
		m.offers = append(m.offers, offer{NodeID: "BIG-1", Model: mdl, Online: true, Ctx: ctx})
	})
	return nil
}

func (s *opBDD) plateShownFor(name string) error {
	p := s.model().operatorPlate
	if p == nil {
		return fmt.Errorf("no pre-launch plate is up (transcript: %s)", s.view())
	}
	if p.det.Guest.Name != name {
		return fmt.Errorf("the plate is for %q, want %q", p.det.Guest.Name, name)
	}
	if v := s.agentViewText(); !strings.Contains(v, "HAND-OFF CHECK") {
		return fmt.Errorf("the AGENT view does not render the plate:\n%s", v)
	}
	return nil
}

func (s *opBDD) noPlateShown() error {
	if s.model().operatorPlate != nil {
		return fmt.Errorf("a pre-launch plate is up")
	}
	if strings.Contains(s.agentViewText(), "HAND-OFF CHECK") {
		return fmt.Errorf("plate chrome rendered with no plate state")
	}
	return nil
}

func (s *opBDD) plateNoContextWarning() error {
	v := s.plateText()
	if strings.Contains(v, "context window unknown") || strings.Contains(v, "too small") {
		return fmt.Errorf("the plate carries a context warning:\n%s", v)
	}
	return nil
}

func (s *opBDD) handoffRefused() error {
	m := s.model()
	if m.operatorPlate != nil {
		return fmt.Errorf("a plate opened despite the gate")
	}
	if m.operatorHandoff != nil {
		return fmt.Errorf("staging began despite the gate")
	}
	if !strings.Contains(s.view(), "too small for a guest") {
		return fmt.Errorf("no honest refusal in the transcript:\n%s", s.view())
	}
	return nil
}

func (s *opBDD) refusalNames(window, floor string) error {
	v := s.view()
	if !strings.Contains(v, window) || !strings.Contains(v, floor) {
		return fmt.Errorf("the refusal does not name window %q and floor %q:\n%s", window, floor, v)
	}
	return nil
}

func (s *opBDD) refusalPointsAtRetune() error {
	v := s.view()
	if !strings.Contains(v, "re-tune") || !strings.Contains(v, "larger band") {
		return fmt.Errorf("the refusal does not point at re-tuning to a larger band:\n%s", v)
	}
	return nil
}

func (s *opBDD) pickerRowDisabled(name string) error {
	for _, r := range s.model().operatorRows {
		if r.label == name {
			if !r.disabled {
				return fmt.Errorf("picker row %q is not disabled", name)
			}
			return nil
		}
	}
	return fmt.Errorf("no picker row %q", name)
}

func (s *opBDD) disabledRowReason(reason string) error {
	found := false
	for _, r := range s.model().operatorRows {
		if r.disabled && r.reason == reason {
			found = true
		}
	}
	if !found {
		return fmt.Errorf("no disabled row carries the reason %q", reason)
	}
	if !strings.Contains(s.view(), reason) {
		return fmt.Errorf("the rendered picker does not show the reason %q:\n%s", reason, s.view())
	}
	return nil
}

func (s *opBDD) plateCtxEstimate(text string) error {
	if !strings.Contains(s.plateText(), text) {
		return fmt.Errorf("the plate does not render the estimate %q:\n%s", text, s.plateText())
	}
	return nil
}

func (s *opBDD) refusalRendersEstimate(text string) error {
	if !strings.Contains(s.view(), text) {
		return fmt.Errorf("the refusal does not render the estimate %q:\n%s", text, s.view())
	}
	return nil
}

func (s *opBDD) plateWarns(text string) error {
	if !strings.Contains(s.plateText(), text) {
		return fmt.Errorf("the plate does not warn %q:\n%s", text, s.plateText())
	}
	return nil
}

func (s *opBDD) handoffWasRefused() error {
	if err := s.userRuns("/operator opencode"); err != nil {
		return err
	}
	return s.handoffRefused()
}

func (s *opBDD) retuneChannel(ctx int) error { return s.setStation(ctx, false) }

func (s *opBDD) plateAccepted(name string) error {
	if err := s.userRuns("/operator " + name); err != nil {
		return err
	}
	if s.model().operatorPlate == nil {
		return fmt.Errorf("no plate opened for %s (transcript: %s)", name, s.view())
	}
	s.update(keyMsg("y"))
	if s.model().operatorHandoff == nil {
		return fmt.Errorf("accepting the plate did not stage the handoff")
	}
	return nil
}

func (s *opBDD) retuneDuringStagingBeat(ctx int) error {
	if h := s.model().operatorHandoff; h == nil || h.execing {
		return fmt.Errorf("no staging beat is in progress")
	}
	return s.setStation(ctx, false)
}

func (s *opBDD) execAborted() error {
	s.fireExec()
	if len(s.execCmds) != 0 {
		return fmt.Errorf("the exec was issued: %v", s.execCmds[len(s.execCmds)-1].Args)
	}
	if s.model().operatorHandoff != nil {
		return fmt.Errorf("the aborted handoff left staging state behind")
	}
	return nil
}

// --- prelaunch_plate steps ----------------------------------------------------------------------

func (s *opBDD) noHandoffStaging() error {
	if s.model().operatorHandoff != nil {
		return fmt.Errorf("handoff staging has begun")
	}
	return nil
}

func (s *opBDD) plateShows(text string) error {
	if !strings.Contains(s.plateText(), text) {
		return fmt.Errorf("the plate does not show %q:\n%s", text, s.plateText())
	}
	return nil
}

func (s *opBDD) plateShowsGuest(name string) error { return s.plateShows(name) }

func (s *opBDD) plateShowsGuestVersion() error {
	p := s.model().operatorPlate
	if p == nil {
		return fmt.Errorf("no plate is up")
	}
	if p.det.Version == "" {
		return fmt.Errorf("the detection carries no version to show")
	}
	return s.plateShows(p.det.Version)
}

func (s *opBDD) plateShowsBandModel(mdl string) error { return s.plateShows(mdl) }

func (s *opBDD) plateShowsCallsign() error {
	o := s.model().connected
	if o == nil || o.NodeID == "" {
		return fmt.Errorf("the open channel carries no station callsign")
	}
	return s.plateShows("@" + o.NodeID)
}

func (s *opBDD) plateShowsCtx(text string) error { return s.plateShows(text) }

func (s *opBDD) plateShowsPrice(text string) error { return s.plateShows(text) }

func (s *opBDD) plateShowsPriceTier() error {
	o := s.model().connected
	if o == nil {
		return fmt.Errorf("no open-channel station")
	}
	bars, chip := pricetier.Render(o.PriceTier, o.PriceOut)
	if bars == "" || bars == "FREE" {
		return fmt.Errorf("the scenario station carries no $-tier to show (tier=%d)", o.PriceTier)
	}
	want := bars
	if chip != "" {
		want += " " + chip
	}
	return s.plateShows(want)
}

func (s *opBDD) fetchedBalance(amount string) error {
	v, err := strconv.ParseFloat(amount, 64)
	if err != nil {
		return err
	}
	s.mutate(func(m *model) { m.balance, m.haveBal = v, true })
	return nil
}

func (s *opBDD) noBalanceFetched() error {
	s.mutate(func(m *model) { m.balance, m.haveBal = 0, false })
	return nil
}

func (s *opBDD) plateShowsBalance(text string) error { return s.plateShows(text) }

func (s *opBDD) plateBalanceDash() error {
	for _, ln := range strings.Split(s.plateText(), "\n") {
		if strings.Contains(ln, "balance") {
			if !strings.Contains(ln, "-") {
				return fmt.Errorf("the balance line is not an honest dash: %q", ln)
			}
			return nil
		}
	}
	return fmt.Errorf("no balance line on the plate:\n%s", s.plateText())
}

func (s *opBDD) plateNoFabricatedZero() error {
	if strings.Contains(s.plateText(), "$0.00") {
		return fmt.Errorf("the plate fabricated a $0.00 balance:\n%s", s.plateText())
	}
	return nil
}

func (s *opBDD) plateShowsBudget(text string) error { return s.plateShows("session budget " + text) }

func (s *opBDD) plateShowsBudgetRaise() error { return s.plateShows("b raises the ceiling") }

func (s *opBDD) plateShowsWorkdir() error {
	p := s.model().operatorPlate
	if p == nil {
		return fmt.Errorf("no plate is up")
	}
	if !filepath.IsAbs(p.workdir) {
		return fmt.Errorf("plate workdir %q is not absolute", p.workdir)
	}
	return s.plateShows(p.workdir)
}

func (s *opBDD) plateShowsExpectationLine() error {
	v := s.plateText()
	if !strings.Contains(v, "heads up · ") || !strings.Contains(v, "community band quality") {
		return fmt.Errorf("no community-model expectation line on the plate:\n%s", v)
	}
	return nil
}

func (s *opBDD) expectationNamesModel(mdl string) error {
	return s.plateShows("runs on " + mdl + " here")
}

// expectationNoVendorAttribution: the P1 pin - the line must carry the explicit
// anti-attribution ("not <guest>'s house models"), never crediting the guest's vendor.
func (s *opBDD) expectationNoVendorAttribution(name string) error {
	for _, ln := range strings.Split(s.plateText(), "\n") {
		if strings.Contains(ln, "heads up · ") {
			if !strings.Contains(ln, "not "+name+"'s house models") {
				return fmt.Errorf("the expectation line does not disclaim %s's models: %q", name, ln)
			}
			return nil
		}
	}
	return fmt.Errorf("no expectation line on the plate:\n%s", s.plateText())
}

func (s *opBDD) plateWarnsVersionUnproven() error {
	p := s.model().operatorPlate
	if p == nil {
		return fmt.Errorf("no plate is up")
	}
	v := s.plateText()
	if !strings.Contains(v, p.det.Version) || !strings.Contains(v, "version "+p.det.Version+" is unproven") {
		return fmt.Errorf("the plate does not warn version %q is unproven:\n%s", p.det.Version, v)
	}
	return nil
}

func (s *opBDD) pickerCursorOn(name string) error {
	m := s.model()
	if !m.operatorPicker {
		return fmt.Errorf("the picker is not open")
	}
	if m.operatorCursor >= len(m.operatorRows) || m.operatorRows[m.operatorCursor].label != name {
		return fmt.Errorf("cursor on %v, want %q", s.pickerRowLabels(), name)
	}
	return nil
}

func (s *opBDD) holderBudgetUnchanged() error {
	if s.holder == nil {
		return fmt.Errorf("no holder was seeded")
	}
	if got := s.holder.Get().Budget; got != s.budgetAtSeed {
		return fmt.Errorf("holder budget changed: %v -> %v", s.budgetAtSeed, got)
	}
	return nil
}

func (s *opBDD) remoteTurnWhilePlateUp() error {
	if s.model().operatorPlate == nil {
		return fmt.Errorf("no plate is up")
	}
	s.update(remoteInboundMsg(protocol.RCInbound{Kind: protocol.RCInTurn, Text: "roadside ask", Origin: "phone"}))
	return nil
}

func (s *opBDD) plateClosed() error {
	if s.model().operatorPlate != nil {
		return fmt.Errorf("the plate is still up")
	}
	return nil
}

func (s *opBDD) notesDJPickedUpTurn() error { return s.transcriptShows("the DJ picked up a turn") }

// viewerSendsConfirmFrame drives a REAL remote confirm through the bridge and the host's
// real dispatch (onRemoteInbound) - the B6 pin: the plate can never be accepted remotely.
func (s *opBDD) viewerSendsConfirmFrame() error {
	s.rcQueue <- protocol.RCInbound{Kind: protocol.RCInConfirm, Approve: true, ConfirmID: "any", Origin: "phone"}
	select {
	case in := <-s.bridge.Inbound():
		s.update(remoteInboundMsg(in))
	case <-time.After(2 * time.Second):
		return fmt.Errorf("the bridge never delivered the confirm frame")
	}
	return nil
}

func (s *opBDD) plateShowsYNGate() error {
	v := s.plateText()
	for _, want := range []string{"[ enter / y ]", "[ esc / n ]", "deny=default"} {
		if !strings.Contains(v, want) {
			return fmt.Errorf("the plate gate lacks %q:\n%s", want, v)
		}
	}
	return nil
}

func (s *opBDD) plateNoANSI() error {
	if strings.Contains(s.plateRaw(), "\x1b") {
		return fmt.Errorf("the plate emitted ANSI with color disabled")
	}
	return nil
}

// --- plate_budget steps ---------------------------------------------------------------------------

func (s *opBDD) pressesBTimes(n int) error {
	for i := 0; i < n; i++ {
		s.update(keyMsg("b"))
	}
	return nil
}

func (s *opBDD) plateNoMissingCeilingWarn() error {
	if strings.Contains(s.plateText(), "no ceiling") {
		return fmt.Errorf("the plate warns about a missing ceiling:\n%s", s.plateText())
	}
	return nil
}

func (s *opBDD) holderBudgetReads(amount string) error {
	if err := s.ensureHandoff("opencode"); err != nil {
		return err
	}
	want, err := strconv.ParseFloat(amount, 64)
	if err != nil {
		return err
	}
	if got := s.holder.Get().Budget; got != want {
		return fmt.Errorf("holder budget = %v, want %v", got, want)
	}
	return nil
}

func (s *opBDD) holderBudgetUncapped() error {
	if err := s.ensureHandoff("opencode"); err != nil {
		return err
	}
	if got := s.holder.Get().Budget; got != 0 {
		return fmt.Errorf("holder budget = %v, want uncapped (0)", got)
	}
	return nil
}

func (s *opBDD) guestSpendsToCeiling(amount string) error {
	if err := s.ensureHandoff("opencode"); err != nil {
		return err
	}
	v, err := strconv.ParseFloat(amount, 64)
	if err != nil {
		return err
	}
	c := fmt.Sprintf("%.2f", v*0.4) // three real proxy calls; the third crosses the ceiling
	return s.proxyCallsCosts([]string{c, c, c})
}

func (s *opBDD) summaryNamesCeiling(amount string) error { return s.transcriptShows(amount) }

func (s *opBDD) plateWarnsCeilingAboveBalance() error {
	if !strings.Contains(s.plateText(), "above your balance") {
		return fmt.Errorf("no ceiling-above-balance warn on the plate:\n%s", s.plateText())
	}
	return nil
}

// --- plate_workdir steps ----------------------------------------------------------------------------

func (s *opBDD) workdirIsProjectDir() error {
	s.launchWorkdir = s.workdir
	return nil
}

func (s *opBDD) workdirIsHome() error {
	s.launchWorkdir = s.home
	return nil
}

func (s *opBDD) workdirInsideHome() error {
	dir := filepath.Join(s.home, "proj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	s.launchWorkdir = dir
	return nil
}

func (s *opBDD) homePointsAtSandbox() error {
	if got := os.Getenv("HOME"); got != s.home {
		return fmt.Errorf("HOME = %q, want the scenario sandbox %q", got, s.home)
	}
	return nil
}

func (s *opBDD) plateWorkdirNeverDot() error {
	p := s.model().operatorPlate
	if p == nil {
		return fmt.Errorf("no plate is up")
	}
	if p.workdir == "." || !filepath.IsAbs(p.workdir) {
		return fmt.Errorf("plate workdir %q is not a resolved absolute path", p.workdir)
	}
	return nil
}

func (s *opBDD) plateWorkdirNoTilde() error {
	p := s.model().operatorPlate
	if p == nil {
		return fmt.Errorf("no plate is up")
	}
	if strings.Contains(p.workdir, "~") {
		return fmt.Errorf("plate workdir %q carries an unexpanded ~", p.workdir)
	}
	return nil
}

func (s *opBDD) plateWarnsHomeWorkdir() error {
	if !strings.Contains(s.plateText(), "home directory") {
		return fmt.Errorf("the plate does not warn about the home workdir:\n%s", s.plateText())
	}
	return nil
}

func (s *opBDD) homeSecondGateShown() error {
	p := s.model().operatorPlate
	if p == nil || !p.homeGate {
		return fmt.Errorf("the home-directory second gate is not up")
	}
	if !strings.Contains(s.plateText(), "your whole home directory") {
		return fmt.Errorf("the second gate is not rendered:\n%s", s.plateText())
	}
	return nil
}

func (s *opBDD) plateNoHomeWarn() error {
	if strings.Contains(s.plateText(), "home directory") {
		return fmt.Errorf("the plate warns about the home directory for a child dir:\n%s", s.plateText())
	}
	return nil
}

func (s *opBDD) childWorkdirIsHome() error {
	if len(s.execCmds) == 0 {
		s.fireExec()
	}
	if len(s.execCmds) == 0 {
		return fmt.Errorf("no exec was issued")
	}
	if got := s.execCmds[len(s.execCmds)-1].Dir; got != s.home {
		return fmt.Errorf("child dir = %q, want the home directory %q", got, s.home)
	}
	return nil
}

func (s *opBDD) plateShowsAiderNote() error { return s.plateShows("--no-auto-commits") }

func (s *opBDD) plateNoAiderNote() error {
	if strings.Contains(s.plateText(), "no-auto-commits") {
		return fmt.Errorf("a non-aider plate carries the aider note:\n%s", s.plateText())
	}
	return nil
}

// --- suite wiring -----------------------------------------------------------------------------------

// initializePhase3Steps registers the Phase 3 spec set's steps (called from
// initializeOperatorScenarios so the whole operator suite shares one opBDD state).
func initializePhase3Steps(st *opBDD, sc *godog.ScenarioContext) {
	// desk_view
	sc.Step(`^detected guests "([^"]*)", "([^"]*)" and "([^"]*)"$`, st.detectedGuestsThree)
	sc.Step(`^a detected guest "([^"]*)" with an unproven version "([^"]*)"$`, st.detectedGuestUnproven)
	sc.Step(`^a detected guest "([^"]*)" that is launchable$`, st.detectedGuestLaunchable)
	sc.Step(`^no model is tuned in$`, st.noModelTuned)
	sc.Step(`^the desk scan has not landed yet$`, st.deskScanNotLanded)
	sc.Step(`^every guest disappears from PATH and the desk is re-scanned$`, st.allGuestsDisappearRescan)
	sc.Step(`^"([^"]*)" disappears from PATH and the desk is re-scanned$`, st.guestDisappearsRescan)
	sc.Step(`^the transcript already has lines$`, st.transcriptHasLines)
	sc.Step(`^a mutating-tool confirm is pending$`, st.confirmPending)
	sc.Step(`^the terminal is (\d+) columns wide$`, st.terminalWidth)
	sc.Step(`^color is disabled$`, st.colorDisabled)
	sc.Step(`^the user submits the prompt "([^"]*)"$`, st.userSubmitsPrompt)
	sc.Step(`^the ask prompt still has focus$`, st.askPromptHasFocus)
	sc.Step(`^the ask prompt echoes "([^"]*)"$`, st.askPromptEchoes)
	sc.Step(`^the AGENT landing renders THE DESK roster$`, st.landingRendersRoster)
	sc.Step(`^the roster heading reads "([^"]*)"$`, st.rosterHeadingReads)
	sc.Step(`^the roster heading subtitle reads "([^"]*)"$`, st.rosterSubtitleReads)
	sc.Step(`^the roster heading subtitle does not name a model$`, st.rosterSubtitleNamesNoModel)
	sc.Step(`^the AGENT view is byte-identical to the pre-desk AGENT view$`, st.agentViewByteIdenticalPreDesk)
	sc.Step(`^the AGENT view never contains "([^"]*)"$`, st.agentViewNeverContains)
	sc.Step(`^the AGENT landing renders no desk chrome$`, st.landingRendersNoDeskChrome)
	sc.Step(`^the first roster row is the DJ row$`, st.firstRosterRowIsDJ)
	sc.Step(`^the DJ row carries the red ◉ on-air mark$`, st.djRowCarriesRedMark)
	sc.Step(`^the DJ row reads "([^"]*)"$`, st.djRowReads)
	sc.Step(`^the roster row for "([^"]*)" shows wire "([^"]*)"$`, st.rosterRowShowsWire)
	sc.Step(`^the roster row for "([^"]*)" shows status "([^"]*)"$`, st.rosterRowShowsStatus)
	sc.Step(`^the roster guest rows are "([^"]*)", "([^"]*)" in that order$`, st.rosterGuestRowsTwo)
	sc.Step(`^the roster guest rows are "([^"]*)", "([^"]*)", "([^"]*)" in that order$`, st.rosterGuestRowsThree)
	sc.Step(`^the roster row for "([^"]*)" notes the version is unproven$`, st.rosterRowVersionUnproven)
	sc.Step(`^the roster shows exactly one not-installed row$`, st.rosterExactlyOneNotInstalled)
	sc.Step(`^the not-installed row is the last roster row$`, st.notInstalledRowIsLast)
	sc.Step(`^the not-installed row shows status "not at the desk · get it:" with the install hint$`, st.notInstalledRowShowsHint)
	sc.Step(`^the roster shows no not-installed row$`, st.rosterNoNotInstalledRow)
	sc.Step(`^no roster row carries a carat$`, st.noRosterRowCarat)
	sc.Step(`^no roster row is rendered reverse-video$`, st.noRosterRowReverseVideo)
	sc.Step(`^the roster collapses$`, st.rosterCollapses)
	sc.Step(`^the AGENT landing renders no desk roster$`, st.landingRendersNoRoster)
	sc.Step(`^the AGENT view renders the roster at most once$`, st.rosterAtMostOnce)
	sc.Step(`^the staged paint does not render THE DESK roster$`, st.stagedPaintNoRoster)
	sc.Step(`^no AGENT view line exceeds the terminal width$`, st.noAgentViewLineExceedsWidth)
	sc.Step(`^the roster renders without ANSI color$`, st.rosterNoANSI)
	sc.Step(`^the DJ row still carries the ◉ mark as a plain rune$`, st.djRowPlainMark)

	// desk_strip
	sc.Step(`^the AGENT heading area shows the desk strip$`, st.headingShowsStrip)
	sc.Step(`^the desk strip reads "([^"]*)"$`, st.stripReads)
	sc.Step(`^the desk strip leads with the red ◉ on-air mark$`, st.stripLeadsWithRedMark)
	sc.Step(`^the desk strip does not name "([^"]*)"$`, st.stripDoesNotName)
	sc.Step(`^the user switches to the band browser$`, st.switchToBandBrowser)
	sc.Step(`^the band browser view never contains "([^"]*)"$`, st.bandBrowserNeverContains)
	sc.Step(`^the windowshade compact view is active$`, st.compactActive)
	sc.Step(`^the windowshade is expanded$`, st.compactExpanded)
	sc.Step(`^the compact AGENT heading reads "([^"]*)"$`, st.compactHeadingReads)
	sc.Step(`^the compact view does not render the full desk strip$`, st.compactNoFullStrip)
	sc.Step(`^the compact view does not render THE DESK roster$`, st.landingRendersNoRoster)
	sc.Step(`^the compact AGENT heading never contains "([^"]*)"$`, st.compactHeadingNeverContains)
	sc.Step(`^the compact AGENT heading is byte-identical to the pre-desk compact heading$`, st.compactHeadingByteIdentical)
	sc.Step(`^the desk strip renders without ANSI color$`, st.stripNoANSI)

	// band_gate
	sc.Step(`^the open channel's station reports a context window of (\d+) tokens$`, st.stationCtx)
	sc.Step(`^the open channel's station reports an estimated context window of (\d+) tokens$`, st.stationCtxEstimated)
	sc.Step(`^the open channel's station reports no context window$`, st.stationCtxUnknown)
	sc.Step(`^the open channel's station serves at (\d+) t/s for \$([0-9.]+)·\$([0-9.]+) per 1M$`, st.stationServes)
	sc.Step(`^the band has another station with a context window of (\d+) tokens$`, st.bandHasAnotherStation)
	sc.Step(`^the pre-launch plate is shown for "([^"]*)"$`, st.plateShownFor)
	sc.Step(`^no pre-launch plate is shown$`, st.noPlateShown)
	sc.Step(`^the plate carries no context warning$`, st.plateNoContextWarning)
	sc.Step(`^the handoff is refused before any plate or staging$`, st.handoffRefused)
	sc.Step(`^the refusal names the window "([^"]*)" and the floor "([^"]*)"$`, st.refusalNames)
	sc.Step(`^the refusal points at re-tuning to a larger band$`, st.refusalPointsAtRetune)
	sc.Step(`^the picker row for "([^"]*)" is disabled$`, st.pickerRowDisabled)
	sc.Step(`^the disabled row carries the reason "([^"]*)"$`, st.disabledRowReason)
	sc.Step(`^the plate renders the context window as an estimate "([^"]*)"$`, st.plateCtxEstimate)
	sc.Step(`^the refusal renders the window as an estimate "([^"]*)"$`, st.refusalRendersEstimate)
	sc.Step(`^the plate warns "([^"]*)"$`, st.plateWarns)
	sc.Step(`^the handoff was refused for the small window$`, st.handoffWasRefused)
	sc.Step(`^the user re-tunes the channel to a station with a context window of (\d+) tokens$`, st.retuneChannel)
	sc.Step(`^the plate for "([^"]*)" was accepted$`, st.plateAccepted)
	sc.Step(`^the channel is re-tuned to a station with a context window of (\d+) tokens during the staging beat$`, st.retuneDuringStagingBeat)
	sc.Step(`^the exec is aborted$`, st.execAborted)
	sc.Step(`^the transcript notes the band changed under the patch$`,
		func() error { return st.transcriptShows("the band changed under the patch") })

	// prelaunch_plate
	sc.Step(`^no handoff staging has begun$`, st.noHandoffStaging)
	sc.Step(`^the plate shows "([^"]*)"$`, st.plateShows)
	sc.Step(`^the plate shows the guest "([^"]*)"$`, st.plateShowsGuest)
	sc.Step(`^the plate shows the guest version from the detection$`, st.plateShowsGuestVersion)
	sc.Step(`^the plate shows the band model "([^"]*)"$`, st.plateShowsBandModel)
	sc.Step(`^the plate shows the open channel's station callsign$`, st.plateShowsCallsign)
	sc.Step(`^the plate shows the context window "([^"]*)"$`, st.plateShowsCtx)
	sc.Step(`^the plate shows the price "([^"]*)"$`, st.plateShowsPrice)
	sc.Step(`^the plate shows the band's price tier$`, st.plateShowsPriceTier)
	sc.Step(`^the fetched balance is "\$([0-9.]+)"$`, st.fetchedBalance)
	sc.Step(`^no balance has been fetched$`, st.noBalanceFetched)
	sc.Step(`^the plate shows the balance "([^"]*)"$`, st.plateShowsBalance)
	sc.Step(`^the plate shows the balance as "-"$`, st.plateBalanceDash)
	sc.Step(`^the plate does not show "\$0\.00" as the balance$`, st.plateNoFabricatedZero)
	sc.Step(`^the plate shows the session budget "([^"]*)"$`, st.plateShowsBudget)
	sc.Step(`^the plate shows how to raise the budget$`, st.plateShowsBudgetRaise)
	sc.Step(`^the plate shows the resolved absolute workdir$`, st.plateShowsWorkdir)
	sc.Step(`^the plate shows the community-model expectation line$`, st.plateShowsExpectationLine)
	sc.Step(`^the expectation line names the band model "([^"]*)"$`, st.expectationNamesModel)
	sc.Step(`^the expectation line does not attribute the model to "([^"]*)"$`, st.expectationNoVendorAttribution)
	sc.Step(`^the plate warns the guest version is unproven$`, st.plateWarnsVersionUnproven)
	sc.Step(`^the picker cursor is on "([^"]*)"$`, st.pickerCursorOn)
	sc.Step(`^the holder budget is unchanged$`, st.holderBudgetUnchanged)
	sc.Step(`^a remote turn starts a DJ reply while the plate is up$`, st.remoteTurnWhilePlateUp)
	sc.Step(`^the plate is closed$`, st.plateClosed)
	sc.Step(`^the transcript notes the DJ picked up a turn$`, st.notesDJPickedUpTurn)
	sc.Step(`^a viewer sends a confirm frame$`, st.viewerSendsConfirmFrame)
	sc.Step(`^the plate shows the y/N gate$`, st.plateShowsYNGate)
	sc.Step(`^the plate renders without ANSI color$`, st.plateNoANSI)

	// plate_budget
	sc.Step(`^the user presses "b" (\d+) times$`, st.pressesBTimes)
	sc.Step(`^the plate does not warn about a missing ceiling$`, st.plateNoMissingCeilingWarn)
	sc.Step(`^the holder budget reads \$([0-9.]+)$`, st.holderBudgetReads)
	sc.Step(`^the holder budget reads uncapped$`, st.holderBudgetUncapped)
	sc.Step(`^the guest spends up to a "\$([0-9.]+)" ceiling during the handoff$`, st.guestSpendsToCeiling)
	sc.Step(`^the summary names the ceiling "([^"]*)"$`, st.summaryNamesCeiling)
	sc.Step(`^the plate warns the ceiling is above the balance$`, st.plateWarnsCeilingAboveBalance)

	// plate_workdir
	sc.Step(`^the session workdir is a project directory$`, st.workdirIsProjectDir)
	sc.Step(`^the session workdir is the user's home directory$`, st.workdirIsHome)
	sc.Step(`^the session workdir is a directory inside the user's home$`, st.workdirInsideHome)
	sc.Step(`^HOME points at the sandbox home for this scenario$`, st.homePointsAtSandbox)
	sc.Step(`^the plate workdir is never "\."$`, st.plateWorkdirNeverDot)
	sc.Step(`^the plate workdir contains no "~"$`, st.plateWorkdirNoTilde)
	sc.Step(`^the plate warns the workdir is the home directory$`, st.plateWarnsHomeWorkdir)
	sc.Step(`^the home-directory second gate is shown$`, st.homeSecondGateShown)
	sc.Step(`^the plate does not warn about the home directory$`, st.plateNoHomeWarn)
	sc.Step(`^the child working directory is the user's home directory$`, st.childWorkdirIsHome)
	sc.Step(`^the plate shows the aider no-auto-commits note$`, st.plateShowsAiderNote)
	sc.Step(`^the plate does not show the aider no-auto-commits note$`, st.plateNoAiderNote)
}
