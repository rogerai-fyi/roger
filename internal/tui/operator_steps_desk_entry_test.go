package tui

// operator_steps_desk_entry_test.go - step definitions for the AGENT [0] desk-entry
// redesign specs (features/operator/desk_entry.feature + auto_tune.feature). They drive
// the REAL bubbletea model: a FRESH session is New(...) with NO proxy holder (nothing
// tuned in), the desk scan is delivered as the real operatorDetectedMsg, and the silent
// auto-tune is delivered as the real autoTuneMsg - no mocks. pickAutoBand is exercised
// directly (it is pure). Registered from initializeOperatorScenarios (operator_bdd_test).

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/cucumber/godog"
	"github.com/rogerai-fyi/roger/internal/operator"
)

// --- fresh-session seeding (no proxy holder = nothing tuned in) --------------------------

// freshAgent builds a fresh AGENT session: New(broker), sized, fed the given market +
// login state, then [0] enters AGENT with NO holder (the genuinely-fresh landing).
func (s *opBDD) freshAgent(offers []offer, loggedIn bool) {
	var tm tea.Model = New("http://broker.local", "tester")
	tm, _ = tm.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	tm, _ = tm.Update(offersMsg(offers))
	tm, _ = tm.Update(balanceMsg{loggedIn: loggedIn, balance: 10})
	tm, _ = tm.Update(keyMsg("0"))
	s.tm = asModel(tm)
}

func (s *opBDD) freshNothingTuned() error { s.freshAgent(nil, true); return nil }

func offerFree(model string) offer {
	return offer{NodeID: "n-" + model, Model: model, Online: true, FreeNow: true, Signal: 50, Ctx: 32768}
}
func offerPaid(model string, out float64) offer {
	return offer{NodeID: "n-" + model, Model: model, Online: true, PriceIn: out / 2, PriceOut: out, Signal: 50, Ctx: 32768}
}

func (s *opBDD) freshFreeBand(mdl string) error {
	s.freshAgent([]offer{offerFree(mdl)}, true)
	return nil
}
func (s *opBDD) freshPaidOnly() error {
	s.freshAgent([]offer{offerPaid("paid-model", 0.30)}, true)
	return nil
}
func (s *opBDD) freshPaidOnlyLoggedIn() error {
	s.freshAgent([]offer{offerPaid("paid-model", 0.30)}, true)
	return nil
}
func (s *opBDD) freshPaidOnlyLoggedOut() error {
	s.freshAgent([]offer{offerPaid("paid-model", 0.30)}, false)
	return nil
}
func (s *opBDD) freshEmptyMarket() error { s.freshAgent(nil, true); return nil }

// freshMixedFreeAndCheaperPaid seeds ONE band ("mixed") of two online stations: a FreeNow
// promo station carrying a NONZERO nominal price, and a genuinely PAID station whose price
// is CHEAPER. groupBands flags the band free (the FreeNow station) but sets cheapest to the
// paid station (lower PriceOut) - the R1 money-safety trap: binding cheapest silently spends.
func (s *opBDD) freshMixedFreeAndCheaperPaid() error {
	free := offer{NodeID: "n-free", Model: "mixed", Online: true, FreeNow: true, PriceIn: 0.25, PriceOut: 0.50, Signal: 60, Ctx: 32768}
	paid := offer{NodeID: "n-paid", Model: "mixed", Online: true, PriceIn: 0.05, PriceOut: 0.10, Signal: 90, Ctx: 32768}
	s.freshAgent([]offer{free, paid}, true)
	return nil
}

// staleFreeFlaggedAllPaid seeds a hand-built band whose `free` flag is set but whose only
// station is PAID (a stale/mixed signal groupBands cannot produce, injected directly to pin
// the defensive fallback). Auto-tune must bind nothing and land on the honest paid state.
func (s *opBDD) staleFreeFlaggedAllPaid() error {
	s.freshAgent(nil, true)
	s.mutate(func(m *model) {
		paid := offer{NodeID: "n-stale-paid", Model: "stale", Online: true, PriceIn: 0.05, PriceOut: 0.10, Signal: 50, Ctx: 32768}
		b := band{model: "stale", online: true, free: true, stations: 1, minIn: 0.05, minOut: 0.10, maxOut: 0.10, all: []offer{paid}}
		b.cheapest = &b.all[0]
		m.bands = []band{b}
	})
	return nil
}

// lastBandStillOnAir seeds a session that last tuned band `model`, still on air: a live
// holder + lastConnected on that model + the market carrying it. resolveAgentModel then
// reuses it, so entering AGENT never arms an auto-tune (the sticky-when-online path).
func (s *opBDD) lastBandStillOnAir(mdl string) error {
	var tm tea.Model = New("http://broker.local", "tester")
	tm, _ = tm.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	tm, _ = tm.Update(offersMsg([]offer{offerFree(mdl)}))
	tm, _ = tm.Update(balanceMsg{loggedIn: true, balance: 10})
	m := asModel(tm)
	o := offerFree(mdl)
	m.proxyHolder = nil
	m.connected = &o
	m.lastConnected = &o
	var mm tea.Model = m
	mm, _ = mm.Update(keyMsg("0"))
	s.tm = asModel(mm)
	return nil
}

// --- desk scan delivery ------------------------------------------------------------------

func (s *opBDD) deskScanLandsGuest(name string) error {
	g, err := registryGuest(name)
	if err != nil {
		return err
	}
	s.tuiPaths[g.Bin] = "/fake/" + g.Bin
	s.update(operatorDetectedMsg{ds: []operator.Detection{{Guest: g, Path: "/fake/" + g.Bin, Version: g.KnownGood}}})
	return nil
}
func (s *opBDD) deskScanLandsNoGuests() error {
	s.update(operatorDetectedMsg{ds: nil})
	return nil
}

// theDeskAutoTunes delivers the real autoTuneMsg (what autoTuneCmd produces once a scan is
// in hand) and folds any drained turn cmd back in.
func (s *opBDD) theDeskAutoTunes() error {
	cmd := s.update(autoTuneMsg{})
	for _, msg := range collectCmdMsgs(cmd) {
		if msg != nil {
			s.update(msg)
		}
	}
	return nil
}

// channelOpenedBeforeScan opens a real channel on `model` (deliberate tune) before the
// auto-tune resolves, to pin that auto-tune never overrides it.
func (s *opBDD) channelOpenedBeforeScan(mdl string) error {
	s.mutate(func(m *model) {
		o := offerFree(mdl)
		m.bindChannel(o)
	})
	return nil
}

// channelOpened opens a real channel on `model`. Same as channelOpenedBeforeScan but with
// no ordering implied in the phrasing - used to open a channel AFTER a guest scan has
// focused the desk (the already-connected focus-steal regression).
func (s *opBDD) channelOpened(mdl string) error { return s.channelOpenedBeforeScan(mdl) }

// userReEntersAgent presses [0] again after an esc-exit. The async desk scan cmd it emits
// is deliberately NOT drained here, so no fresh scan re-arms the desk: the focus state on
// re-entry is exactly what the exit left behind (the dual-focus regression).
func (s *opBDD) userReEntersAgent() error {
	s.update(keyMsg("0"))
	return nil
}

// userPressesRealEsc delivers a REAL Escape key (tea.KeyEsc), not the multi-rune keyMsg
// helper "esc" - which isPrintableKey would treat as a type-through and de-focus the desk
// as a side effect. Only a real Esc exercises the esc-EXIT path (the dual-focus fix).
func (s *opBDD) userPressesRealEsc() error {
	cmd := s.update(tea.KeyMsg{Type: tea.KeyEsc})
	for _, msg := range collectCmdMsgs(cmd) {
		if msg != nil {
			s.update(msg)
		}
	}
	return nil
}

// brokerUnreachableColdAutoTune delivers the errMsg that fetchOffers emits when the cold
// AGENT [0] auto-tune cannot reach the broker - the real message, straight into Update.
func (s *opBDD) brokerUnreachableColdAutoTune() error {
	s.update(errMsg("broker unreachable: http://broker.local"))
	return nil
}

func (s *opBDD) transcriptNoLongerShows(text string) error {
	if strings.Contains(s.view(), text) {
		return fmt.Errorf("transcript still shows %q:\n%s", text, s.view())
	}
	return nil
}

// sessionClearedWith runs an in-AGENT slash command (e.g. /clear) through the REAL
// runAgentCommand path and folds any emitted cmd back in.
func (s *opBDD) sessionClearedWith(cmd string) error {
	tm, c := s.model().runAgentCommand(cmd)
	s.tm = tm
	for _, msg := range collectCmdMsgs(c) {
		if msg != nil {
			s.update(msg)
		}
	}
	return nil
}

// nonUnreachableErr delivers a NON-broker-unreachable errMsg (fetchBalance's errMsg("") in
// the cold-fetch window is the reported case) - it must NOT disarm an armed auto-tune.
func (s *opBDD) nonUnreachableErr() error {
	s.update(errMsg(""))
	return nil
}

func (s *opBDD) autoTuneIsArmed() error {
	if !s.model().autoTuning {
		return fmt.Errorf("the auto-tune is not armed (a non-unreachable error disarmed it)")
	}
	return nil
}

// --- focus assertions --------------------------------------------------------------------

func (s *opBDD) deskHasFocus() error {
	m := s.model()
	if !m.deskFocused {
		return fmt.Errorf("THE DESK does not have focus")
	}
	if m.agentIn.Focused() {
		return fmt.Errorf("the ask box unexpectedly has focus while the desk is focused")
	}
	return nil
}
func (s *opBDD) deskNoFocus() error {
	if s.model().deskFocused {
		return fmt.Errorf("THE DESK unexpectedly has focus")
	}
	return nil
}
func (s *opBDD) askBoxHasFocus() error {
	if !s.model().agentIn.Focused() {
		return fmt.Errorf("the ask box does not have focus")
	}
	return nil
}
func (s *opBDD) askBoxNotFocused() error {
	if s.model().agentIn.Focused() {
		return fmt.Errorf("the ask box unexpectedly has focus")
	}
	return nil
}
func (s *opBDD) deskCursorOnDJ() error {
	if c := s.model().deskCursor; c != 0 {
		return fmt.Errorf("desk cursor is %d, want 0 (DJ)", c)
	}
	return nil
}
func (s *opBDD) deskCursorOn(name string) error {
	m := s.model()
	ds := deskGuests(m.operatorDetections)
	idx := m.deskCursor - 1
	if idx < 0 || idx >= len(ds) || ds[idx].Guest.Name != name {
		return fmt.Errorf("desk cursor %d is not on %q (guests=%d)", m.deskCursor, name, len(ds))
	}
	return nil
}
func (s *opBDD) userTypes(text string) error {
	s.tm = typeRunes(s.tm, text)
	return nil
}
func (s *opBDD) askBoxEchoes(text string) error { return s.askPromptEchoes(text) }

// --- auto-tune outcome assertions --------------------------------------------------------

func (s *opBDD) agentRunsOn(mdl string) error {
	if got := s.model().agent.model; got != mdl {
		return fmt.Errorf("agent runs on %q, want %q", got, mdl)
	}
	return nil
}
func (s *opBDD) channelIsOpen() error {
	if s.model().connected == nil {
		return fmt.Errorf("no channel is open")
	}
	return nil
}
func (s *opBDD) noChannelOpen() error {
	if s.model().connected != nil {
		return fmt.Errorf("a channel is unexpectedly open on %q", s.model().connected.Model)
	}
	return nil
}
func (s *opBDD) noCostConfirmShown() error {
	if s.model().agentPendingConfirm != nil {
		return fmt.Errorf("a confirm is unexpectedly pending")
	}
	if strings.Contains(s.view(), "cost confirm") || strings.Contains(s.view(), "[y/N]") {
		return fmt.Errorf("a cost confirm is on screen:\n%s", s.view())
	}
	return nil
}
// onlyFreeBandSmallBesidePaidLarge seeds the model's market with a SINGLE band whose free
// station has an 8k window and whose paid sibling has a 32k window - the mixed band the
// handoff auto-tune must NOT bind the 8k free station onto (finding 2026-07-08). The band
// ctx is 32k (the max), so a band-level known-small gate is fooled; the fix reads the
// station it is about to bind.
func (s *opBDD) onlyFreeBandSmallBesidePaidLarge() error {
	s.mutate(func(m *model) {
		m.offers = []offer{
			{NodeID: "FREE-8K", Model: "handoff-mix", Online: true, FreeNow: true, Signal: 80, Ctx: 8192},
			{NodeID: "PAID-32K", Model: "handoff-mix", Online: true, PriceIn: 0.15, PriceOut: 0.30, PriceTier: 1, Signal: 70, Ctx: 32768},
		}
		m.lastConnected = nil
		m.bands = groupBands(m.offers, m.limits)
	})
	return nil
}

func (s *opBDD) boundStationIsGenuinelyFree() error {
	c := s.model().connected
	if c == nil {
		return fmt.Errorf("no channel is open - nothing was bound")
	}
	if !(c.FreeNow || (c.PriceIn == 0 && c.PriceOut == 0)) {
		return fmt.Errorf("bound station %q is NOT genuinely free (FreeNow=%v in=%v out=%v) - auto-tune silently bound a PAID station (R1 violation)", c.NodeID, c.FreeNow, c.PriceIn, c.PriceOut)
	}
	return nil
}
func (s *opBDD) pointsAtLoggingIn() error { return s.transcriptShows("/login") }
func (s *opBDD) noHonestNoBandNote() error {
	v := s.view()
	if strings.Contains(v, "no free band on air") || strings.Contains(v, "no station on air") {
		return fmt.Errorf("an honest no-band note leaked:\n%s", v)
	}
	return nil
}
func (s *opBDD) autoTuneDidNotArm() error {
	if s.model().autoTuning {
		return fmt.Errorf("the auto-tune armed unexpectedly")
	}
	return nil
}
func (s *opBDD) transcriptShowsExactlyOnce(text string) error {
	if n := strings.Count(s.view(), text); n != 1 {
		return fmt.Errorf("transcript shows %q %d times, want exactly 1:\n%s", text, n, s.view())
	}
	return nil
}
func (s *opBDD) transcriptShowsAtMostOnce(text string) error {
	if n := strings.Count(s.view(), text); n > 1 {
		return fmt.Errorf("transcript shows %q %d times, want at most 1:\n%s", text, n, s.view())
	}
	return nil
}

// --- pickAutoBand (pure) -----------------------------------------------------------------

func (s *opBDD) mktFree(mdl string, signal int) error {
	s.deskMkt = append(s.deskMkt, offer{NodeID: "n-" + mdl, Model: mdl, Online: true, FreeNow: true, Signal: signal, Ctx: 32768})
	return nil
}
func (s *opBDD) mktFreeWindow(mdl string, signal, ctx int) error {
	s.deskMkt = append(s.deskMkt, offer{NodeID: "n-" + mdl, Model: mdl, Online: true, FreeNow: true, Signal: signal, Ctx: ctx})
	return nil
}
func (s *opBDD) mktPaid(mdl string, out float64) error {
	s.deskMkt = append(s.deskMkt, offer{NodeID: "n-" + mdl, Model: mdl, Online: true, PriceIn: out / 2, PriceOut: out, Signal: 50, Ctx: 32768})
	return nil
}
func (s *opBDD) mktEmpty() error { s.deskMkt = nil; return nil }

func (s *opBDD) pickAutoBandPicks(login, model string) error {
	bands := groupBands(s.deskMkt, &LimitStore{})
	got := pickAutoBand(bands, login == "in")
	if got == nil || got.model != model {
		name := "<nil>"
		if got != nil {
			name = got.model
		}
		return fmt.Errorf("pickAutoBand logged %s = %q, want %q", login, name, model)
	}
	return nil
}
func (s *opBDD) pickAutoBandPicksNothing(login string) error {
	bands := groupBands(s.deskMkt, &LimitStore{})
	if got := pickAutoBand(bands, login == "in"); got != nil {
		return fmt.Errorf("pickAutoBand logged %s = %q, want nothing", login, got.model)
	}
	return nil
}

// deskSurfacedForModel marks a model as already having surfaced the focused desk this
// session (operatorSeenModels), so a re-entry for it stays ask-focused - the once-per-model
// ruling. Seeded directly to pin the state without an esc/re-enter round-trip.
func (s *opBDD) deskSurfacedForModel(mdl string) error {
	s.mutate(func(m *model) {
		if m.operatorSeenModels == nil {
			m.operatorSeenModels = map[string]bool{}
		}
		m.operatorSeenModels[mdl] = true
	})
	return nil
}

// modelMarkedSurfaced asserts the model is now recorded in operatorSeenModels - the FIRST
// entry that surfaced the desk must mark it, so a second entry for it stays ask-focused.
func (s *opBDD) modelMarkedSurfaced(mdl string) error {
	if !s.model().operatorSeenModels[mdl] {
		return fmt.Errorf("model %q is not marked surfaced this session (operatorSeenModels=%v)", mdl, s.model().operatorSeenModels)
	}
	return nil
}

// initializeDeskEntryScenarios registers the desk_entry + auto_tune step definitions.
func initializeDeskEntryScenarios(st *opBDD, sc *godog.ScenarioContext) {
	sc.Step(`^the model "([^"]*)" has already surfaced the desk this session$`, st.deskSurfacedForModel)
	sc.Step(`^the model "([^"]*)" is marked surfaced this session$`, st.modelMarkedSurfaced)
	sc.Step(`^the AGENT view shows "([^"]*)"$`, st.transcriptShows)
	sc.Step(`^the AGENT view does not show "([^"]*)"$`, st.transcriptDoesNotShow)
	sc.Step(`^a fresh AGENT session with nothing tuned in$`, st.freshNothingTuned)
	sc.Step(`^a fresh AGENT session with a free band "([^"]*)" on air$`, st.freshFreeBand)
	sc.Step(`^a fresh AGENT session with only a paid band on air$`, st.freshPaidOnly)
	sc.Step(`^a fresh AGENT session logged in with only a paid band on air$`, st.freshPaidOnlyLoggedIn)
	sc.Step(`^a fresh AGENT session logged out with only a paid band on air$`, st.freshPaidOnlyLoggedOut)
	sc.Step(`^a fresh AGENT session with an empty market$`, st.freshEmptyMarket)
	sc.Step(`^a fresh AGENT session with a band mixing a free station and a cheaper paid station$`, st.freshMixedFreeAndCheaperPaid)
	sc.Step(`^a fresh AGENT session with a free-flagged band whose stations are all paid$`, st.staleFreeFlaggedAllPaid)
	sc.Step(`^the bound station is genuinely free$`, st.boundStationIsGenuinelyFree)
	sc.Step(`^the only free band pairs a free 8k station with a paid 32k sibling$`, st.onlyFreeBandSmallBesidePaidLarge)
	sc.Step(`^an AGENT session whose last band "([^"]*)" is still on air$`, st.lastBandStillOnAir)
	sc.Step(`^the desk scan lands guest "([^"]*)"$`, st.deskScanLandsGuest)
	sc.Step(`^the desk scan lands no guests$`, st.deskScanLandsNoGuests)
	sc.Step(`^the desk auto-tunes$`, st.theDeskAutoTunes)
	sc.Step(`^a channel is opened on "([^"]*)" before the scan lands$`, st.channelOpenedBeforeScan)
	sc.Step(`^a channel is opened on "([^"]*)"$`, st.channelOpened)
	sc.Step(`^the user re-enters AGENT$`, st.userReEntersAgent)
	sc.Step(`^the user presses the escape key$`, st.userPressesRealEsc)
	sc.Step(`^the broker is unreachable during the cold auto-tune$`, st.brokerUnreachableColdAutoTune)
	sc.Step(`^a non-unreachable error arrives during the cold auto-tune$`, st.nonUnreachableErr)
	sc.Step(`^the session is cleared with "([^"]*)"$`, st.sessionClearedWith)
	sc.Step(`^the auto-tune is still armed$`, st.autoTuneIsArmed)
	sc.Step(`^the transcript no longer shows "([^"]*)"$`, st.transcriptNoLongerShows)
	sc.Step(`^THE DESK has focus$`, st.deskHasFocus)
	sc.Step(`^THE DESK does not have focus$`, st.deskNoFocus)
	sc.Step(`^the ask box has focus$`, st.askBoxHasFocus)
	sc.Step(`^the ask box is not focused$`, st.askBoxNotFocused)
	sc.Step(`^the desk cursor is on the DJ row$`, st.deskCursorOnDJ)
	sc.Step(`^the desk cursor is on "([^"]*)"$`, st.deskCursorOn)
	sc.Step(`^the user types "([^"]*)"$`, st.userTypes)
	sc.Step(`^the ask box echoes "([^"]*)"$`, st.askBoxEchoes)
	sc.Step(`^the agent runs on "([^"]*)"$`, st.agentRunsOn)
	sc.Step(`^a channel is open$`, st.channelIsOpen)
	sc.Step(`^no channel is open$`, st.noChannelOpen)
	sc.Step(`^no cost confirm is shown$`, st.noCostConfirmShown)
	sc.Step(`^the transcript points at logging in$`, st.pointsAtLoggingIn)
	sc.Step(`^no honest "no band" note appears$`, st.noHonestNoBandNote)
	sc.Step(`^the auto-tune did not arm$`, st.autoTuneDidNotArm)
	sc.Step(`^the transcript shows "([^"]*)" exactly once$`, st.transcriptShowsExactlyOnce)
	sc.Step(`^the transcript shows "([^"]*)" at most once$`, st.transcriptShowsAtMostOnce)
	sc.Step(`^the market has a free band "([^"]*)" at signal (\d+)$`, st.mktFree)
	sc.Step(`^the market has a free band "([^"]*)" at signal (\d+) window (\d+)$`, st.mktFreeWindow)
	sc.Step(`^the market has a paid band "([^"]*)" out ([0-9.]+)$`, st.mktPaid)
	sc.Step(`^the market is empty$`, st.mktEmpty)
	sc.Step(`^pickAutoBand logged (in|out) picks "([^"]*)"$`, st.pickAutoBandPicks)
	sc.Step(`^pickAutoBand logged (in|out) picks nothing$`, st.pickAutoBandPicksNothing)
}
