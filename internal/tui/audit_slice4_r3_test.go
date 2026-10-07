package tui

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/client"
)

// TestAgentTurnReadsLiveRouting: the F/C toggles flipped AFTER the agent runtime was built
// reach the next agent turn's request body (the completer must not run on the routing
// captured when the runtime was created).
func TestAgentTurnReadsLiveRouting(t *testing.T) {
	var mu sync.Mutex
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var b map[string]any
		_ = json.Unmarshal(raw, &b)
		mu.Lock()
		bodies = append(bodies, b)
		mu.Unlock()
		http.Error(w, `{"error":{"code":"no_match","message":"none"}}`, http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	m := browseSeed(120)
	m.broker = srv.URL
	m.agent = m.newAgentRuntime()
	m.agent.model = "m1"
	m.agent.localChat = ""

	// Toggled after the runtime exists, as the booth does mid-session.
	m.fFree, m.fConf = true, true

	cmd := m.startAgentTurn("hello")
	cmd()
	select {
	case <-m.agent.turnDone:
	case <-time.After(10 * time.Second):
		t.Fatal("agent turn did not finish")
	}
	mu.Lock()
	defer mu.Unlock()
	require.NotEmpty(t, bodies, "the turn reached the broker")
	require.Equal(t, "m1:free", bodies[0]["model"], "F on: the agent turn asks for the free variant")
	rg, _ := bodies[0]["roger"].(map[string]any)
	require.Equal(t, true, rg["confidential"], "C on: the agent turn is confidential-only")
}

// TestRoutingProfileMaxCostComposesStricter: a profile's max cost never loosens the band's.
func TestRoutingProfileMaxCostComposesStricter(t *testing.T) {
	m := auditProfileModel(t, map[string]any{"provider": map[string]any{"max_price": map[string]any{"request": 1.0}}})
	m.limits.Models = map[string]Limit{"m": {MaxCost: 0.02}}
	require.InDelta(t, 0.02, m.routing("m", "").MaxReq, 1e-12, "the band's tighter max cost holds")
	m.limits.Models = map[string]Limit{"m": {MaxCost: 5}}
	require.InDelta(t, 1.0, m.routing("m", "").MaxReq, 1e-12, "the profile's tighter max cost holds")
}

// TestBandCardLimitEditDoesNotFreezeTheDefault: opening the band card's spend editor on a
// band with no entry of its own and pressing enter stores nothing from the default.
func TestBandCardLimitEditDoesNotFreezeTheDefault(t *testing.T) {
	m := auditProfileModel(t, map[string]any{})
	m.limits.Default = Limit{MaxOut: 5}
	m.bands = []band{{model: "m"}}
	m.cfgModel = "m"
	out, _ := m.cfgEditLimit(0)
	m = asModel(out)
	require.Equal(t, modeLimits, m.mode, "the band has a spend row")
	require.Empty(t, m.editBuf, "the editor starts from the band's own entry, not the default's cap")
	out, _ = m.Update(keyMsg("enter"))
	require.Zero(t, asModel(out).limits.own("m").MaxOut, "the default's cap is not written into the band")
}

// TestBandCardRefusedOnTheDefaultRow: b on the default row opens no band card (it would
// write a limits entry named "default").
func TestBandCardRefusedOnTheDefaultRow(t *testing.T) {
	m := auditProfileModel(t, map[string]any{})
	m.mode = modeLimits
	m.limModels = []string{"m", defaultLimitRow}
	m.editField = -1
	out, _ := m.Update(keyMsg("b"))
	require.Equal(t, modeBandConfig, asModel(out).mode, "b on a band row opens its card")
	m.limCursor = 1
	out, _ = m.Update(keyMsg("b"))
	require.NotEqual(t, modeBandConfig, asModel(out).mode)
}

// autoTunedModel is a booth that has silently auto-tuned to a free band (the real
// autoTuneMsg), with a quote limit left over from an earlier confirm.
func autoTunedModel(t *testing.T) model {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var tm tea.Model = NewWith("http://broker.local", "tester", &LimitStore{Models: map[string]Limit{}})
	tm, _ = tm.Update(tea.WindowSizeMsg{Width: 96, Height: 30})
	tm, _ = tm.Update(offersMsg([]offer{capOffer("gpt-oss-20b", 32768, true, nil, 0, 72)}))
	tm, _ = tm.Update(balanceMsg{loggedIn: true, balance: 12.50})
	tm, _ = tm.Update(keyMsg("0"))
	m := asModel(tm)
	m.q.limit = Limit{MaxOut: 0.5} // a prior confirm's cap for another band
	tm, _ = m.Update(autoTuneMsg{})
	m = asModel(tm)
	require.NotNil(t, m.proxyHolder, "the auto-tune bound a channel")
	return m
}

// TestAutoTuneBindDropsAStaleQuoteLimit: an auto-tune bypasses the confirm, so a quote limit
// from an earlier confirm never binds the auto-tuned band.
func TestAutoTuneBindDropsAStaleQuoteLimit(t *testing.T) {
	m := autoTunedModel(t)
	require.Zero(t, m.proxyHolder.Get().MaxPriceOut, "the earlier confirm's cap leaked into the auto-tuned band")
}

// TestSaveQuantRuleRepointsTheLiveProxy: a quant rule saved on the connected band binds the
// live proxy's next turn at once.
func TestSaveQuantRuleRepointsTheLiveProxy(t *testing.T) {
	m := autoTunedModel(t)
	m.cfgModel = m.connected.Model
	out, _ := m.saveQuantRule([]string{"Q8_0"})
	require.Contains(t, asModel(out).proxyHolder.Get().Quantizations, "Q8_0")
}

// TestTunedProfileFreqBindsOwnerTurns: a tuned profile's roger.freq reaches the owner's own
// turns (the live proxy and the agent), not only guest bodies naming @profile/; a private
// band tuned with ~ still wins.
func TestTunedProfileFreqBindsOwnerTurns(t *testing.T) {
	m := auditProfileModel(t, map[string]any{"roger": map[string]any{"freq": "ABC123"}})
	require.Equal(t, "ABC123", m.liveProxyOpts(*m.connected, m.alert).Freq)
	require.Equal(t, "ABC123", m.agentFreqFor("m"))
	require.Equal(t, "", m.agentFreqFor("other"), "the profile binds only the band it was tuned on")
	m.tuneFreq = "TUNED"
	require.Equal(t, "TUNED", m.liveProxyOpts(*m.connected, m.alert).Freq)
}

// TestLimPlateShowsTheResolvedRule: a band that sets no pref of its own shows the default's
// pref on its CONFIG plate (marked as the default's), as the band card does, never a
// "balanced" the band does not run.
func TestLimPlateShowsTheResolvedRule(t *testing.T) {
	m := auditProfileModel(t, map[string]any{})
	m.limits.Default = Limit{Pref: "cheap"}
	m.limits.Models = map[string]Limit{"m": {MaxOut: 2}}
	m.limField = lfMaxIn
	plate := stripANSI(strings.Join(m.limPlate("m", 200), "\n"))
	require.Contains(t, plate, "pref cheap (default)")
	require.NotContains(t, plate, "pref balanced")
	def := stripANSI(strings.Join(m.limPlate(defaultLimitRow, 200), "\n"))
	require.Contains(t, def, "pref cheap")
	require.NotContains(t, def, "(default)", "the default row is the default: no marker")
}

// TestConsoleLimitEditReachesTheLiveProxy: a limit written to the shared store by another
// front-end (the browser console) re-points the live proxy on the booth's next tick.
func TestConsoleLimitEditReachesTheLiveProxy(t *testing.T) {
	m := autoTunedModel(t)
	band := m.connected.Model
	m.limits.Update(band, func(cur Limit) Limit { cur.MaxOut = 0.3; return cur }) // the console's write
	out, _ := m.Update(tickMsg{gen: m.tickGen})
	require.InDelta(t, 0.3, asModel(out).proxyHolder.Get().MaxPriceOut, 1e-12)
}

// TestConfirmRescanRebuildsTheQuote: r on the connect confirm re-scans, and the quote the
// operator then accepts is priced from the fresh scan, not the station list they saw before.
func TestConfirmRescanRebuildsTheQuote(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var tm tea.Model = NewWith("http://broker.local", "tester", &LimitStore{Models: map[string]Limit{}})
	tm, _ = tm.Update(tea.WindowSizeMsg{Width: 96, Height: 30})
	tm, _ = tm.Update(offersMsg([]offer{capOffer("m1", 32768, false, nil, 1.0, 72)}))
	tm, _ = tm.Update(balanceMsg{loggedIn: true, balance: 12.50})
	m := asModel(tm)
	out, _ := m.connect()
	m = asModel(out)
	require.Equal(t, modeConnectConfirm, m.mode)
	require.InDelta(t, 1.0, m.q.b.minOut, 1e-9)
	out, _ = m.Update(keyMsg("r"))
	out, _ = asModel(out).Update(offersMsg([]offer{capOffer("m1", 32768, false, nil, 3.0, 72)}))
	m = asModel(out)
	require.InDelta(t, 3.0, m.q.b.minOut, 1e-9, "the quote is rebuilt from the fresh scan")

	out, _ = m.Update(offersMsg([]offer{capOffer("other", 32768, false, nil, 1.0, 72)}))
	require.True(t, asModel(out).q.stale, "a band gone from the scan is not offered for accept")
	out, _ = asModel(out).Update(keyMsg("enter"))
	require.Nil(t, asModel(out).connected, "accept refuses it")
}

// TestTunedRowQuantWinsOverTheProfileList: the dial row the operator connected to is the
// quant the turn asks for; a tuned profile's quantizations never silently replace it.
func TestTunedRowQuantWinsOverTheProfileList(t *testing.T) {
	m := auditProfileModel(t, map[string]any{"provider": map[string]any{"quantizations": []any{"Q4_K_M"}}})
	require.Equal(t, []string{"Q8_0"}, m.routing("m", "Q8_0").Quantizations)
	require.Equal(t, []string{"Q4_K_M"}, m.routing("m", "").Quantizations, "with no row quant the profile's list applies")
}

// TestConfirmRefusesARowOutsideTheProfileQuants: accepting a row whose quant the chosen
// profile excludes is refused at the confirm, as a row outside the band rule is.
func TestConfirmRefusesARowOutsideTheProfileQuants(t *testing.T) {
	m := auditProfileModel(t, map[string]any{"provider": map[string]any{"quantizations": []any{"Q4_K_M"}}})
	m.mode = modeConnectConfirm
	m.confirmProfile = "p"
	m.q = quote{b: band{model: "m", quant: "Q8_0"}}
	out, _ := m.Update(keyMsg("enter"))
	got := asModel(out)
	require.Equal(t, modeConnectConfirm, got.mode, "the row is not accepted")
	require.Contains(t, stripANSI(got.status), "Q8_0")
}

// TestBegunFieldEnterWithNothingTypedKeepsTheValue: enter begins a fresh value (the spec's
// flow); a second enter with nothing typed keeps the stored value instead of clearing it,
// while a typed value or an explicit backspace is committed.
func TestBegunFieldEnterWithNothingTypedKeepsTheValue(t *testing.T) {
	m := auditProfileModel(t, map[string]any{})
	m.limits.Models = map[string]Limit{"m": {MinCtx: 32768}}
	m.mode = modeLimits
	m.limModels = []string{"m", defaultLimitRow}
	m.limCursor, m.editField = 0, 0
	m.focusLimitField(lfMinCtx)
	out, _ := m.Update(keyMsg("enter")) // begins a fresh value
	out, _ = asModel(out).Update(keyMsg("enter"))
	m = asModel(out)
	require.Equal(t, 32768, m.limits.own("m").MinCtx, "nothing typed: the stored value is kept")
	require.Equal(t, -1, m.editField)

	m.editField = 0
	m.focusLimitField(lfMinCtx)
	out, _ = m.Update(keyMsg("enter"))
	out, _ = asModel(out).Update(keyMsg("8"))
	out, _ = asModel(out).Update(keyMsg("k"))
	out, _ = asModel(out).Update(keyMsg("enter"))
	require.Equal(t, 8192, asModel(out).limits.own("m").MinCtx, "a typed value is committed")
}

// TestRefreshLiveRoutingLeavesAnOpenQuoteAlone: re-pointing the connected band's proxy never
// rewrites the quote a confirm for another band is showing.
func TestRefreshLiveRoutingLeavesAnOpenQuoteAlone(t *testing.T) {
	m := autoTunedModel(t)
	m.q = quote{b: band{model: "other"}, limit: Limit{MaxOut: 0.7}}
	m.mode = modeConnectConfirm
	m.limits.Update(m.connected.Model, func(cur Limit) Limit { cur.MaxOut = 0.3; return cur })
	out, _ := m.Update(tickMsg{gen: m.tickGen})
	got := asModel(out)
	require.InDelta(t, 0.7, got.q.limit.MaxOut, 1e-12, "the open confirm's cap is untouched")
	require.InDelta(t, 0.3, got.proxyHolder.Get().MaxPriceOut, 1e-12, "the connected band's proxy follows its rule")
}

// TestBegunFieldTabWithNothingTypedKeepsTheValue: tab off a begun field with nothing typed
// keeps the stored value (as enter does); it never commits the empty draft as a clear.
func TestBegunFieldTabWithNothingTypedKeepsTheValue(t *testing.T) {
	m := auditProfileModel(t, map[string]any{})
	m.limits.Models = map[string]Limit{"m": {MinCtx: 32768}}
	m.mode = modeLimits
	m.limModels = []string{"m", defaultLimitRow}
	m.limCursor, m.editField = 0, 0
	m.focusLimitField(lfMinCtx)
	out, _ := m.Update(keyMsg("enter")) // begins a fresh value
	out, _ = asModel(out).Update(keyMsg("tab"))
	require.Equal(t, 32768, asModel(out).limits.own("m").MinCtx, "tab with nothing typed keeps the stored value")
}

// TestClearingAnAbsentLimitWritesNothing: clearing a band with no stored rule neither
// re-persists the config nor counts as an edit.
func TestClearingAnAbsentLimitWritesNothing(t *testing.T) {
	saves := 0
	s := &LimitStore{Models: map[string]Limit{"a": {MaxOut: 1}}, Save: func(map[string]Limit, Limit) { saves++ }}
	g := s.Gen()
	s.clear("absent")
	require.Zero(t, saves)
	require.Equal(t, g, s.Gen())
	s.clear("a")
	require.Equal(t, 1, saves)
}

// TestBackgroundRescanNeverTurnsAcceptIntoARaise: a periodic scan that finds the price above
// the cap while the confirm is open keeps the confirm (accept refuses, nothing is pre-filled);
// only an explicit r goes to the raise-the-cap screen.
func TestBackgroundRescanNeverTurnsAcceptIntoARaise(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var tm tea.Model = NewWith("http://broker.local", "tester", &LimitStore{Models: map[string]Limit{"m1": {MaxOut: 2}}})
	tm, _ = tm.Update(tea.WindowSizeMsg{Width: 96, Height: 30})
	tm, _ = tm.Update(offersMsg([]offer{capOffer("m1", 32768, false, nil, 1.0, 72)}))
	tm, _ = tm.Update(balanceMsg{loggedIn: true, balance: 12.50})
	m := asModel(tm)
	out, _ := m.connect()
	m = asModel(out)
	require.Equal(t, modeConnectConfirm, m.mode)

	out, _ = m.Update(offersMsg([]offer{capOffer("m1", 32768, false, nil, 3.0, 72)})) // a periodic scan
	m = asModel(out)
	require.Equal(t, modeConnectConfirm, m.mode, "a background scan never moves the operator off the confirm")
	require.True(t, m.q.overLimit)
	require.Empty(t, m.editBuf, "nothing is pre-filled to raise")
	out, _ = m.Update(keyMsg("enter"))
	m = asModel(out)
	require.Equal(t, modeConnectConfirm, m.mode, "accept refuses a quote over the cap")
	require.InDelta(t, 2.0, m.limits.own("m1").MaxOut, 1e-9, "the cap is not raised")

	out, _ = m.Update(keyMsg("r"))
	// the reply to r (a rescanMsg, not a periodic offersMsg)
	out, _ = asModel(out).Update(rescanMsg{offers: []offer{capOffer("m1", 32768, false, nil, 3.0, 72)}, seq: asModel(out).confirmSeq})
	require.Equal(t, modeOverLimit, asModel(out).mode, "an explicit re-scan may offer the raise")
}

// TestBindCarriesTheTunedProfileOnTheFirstBind: the options a bind hands the live proxy
// already carry the profile tuned for that band, before any later re-point.
func TestBindCarriesTheTunedProfileOnTheFirstBind(t *testing.T) {
	m := auditProfileModel(t, map[string]any{"provider": map[string]any{"sort": "price"}})
	m.connected = nil // not yet connected: the bind is what connects it
	m.proxyAddr = "127.0.0.1:0"
	_, err := m.bindChannel(offer{Model: "m", NodeID: "n1"})
	require.NoError(t, err)
	require.Equal(t, "price", m.proxyHolder.Get().Sort)
}

// TestProfileCtxAndTTFTComposeStricter: the context floor and the first-token ceiling are
// limits, so a profile never loosens the band's (the higher floor, the lower ceiling).
func TestProfileCtxAndTTFTComposeStricter(t *testing.T) {
	m := auditProfileModel(t, map[string]any{"roger": map[string]any{"min_ctx": 8192.0, "max_ttft_ms": 3000.0}})
	m.limits.Models = map[string]Limit{"m": {MinCtx: 32768, MaxTTFTMs: 1500}}
	rt := m.routing("m", "")
	require.Equal(t, 32768, rt.MinCtx)
	require.Equal(t, 1500, rt.MaxTTFT)
	m.limits.Models = map[string]Limit{"m": {MinCtx: 4096, MaxTTFTMs: 5000}}
	rt = m.routing("m", "")
	require.Equal(t, 8192, rt.MinCtx)
	require.Equal(t, 3000, rt.MaxTTFT)
}

// TestBackgroundScanMissingTheBandKeepsTheConfirm: a periodic scan that does not list the
// band (deploy churn, a discovery flicker) keeps the operator on the confirm with the quote
// marked stale, so accept refuses; only an explicit r that still finds nothing goes back.
func TestBackgroundScanMissingTheBandKeepsTheConfirm(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var tm tea.Model = NewWith("http://broker.local", "tester", &LimitStore{Models: map[string]Limit{}})
	tm, _ = tm.Update(tea.WindowSizeMsg{Width: 96, Height: 30})
	tm, _ = tm.Update(offersMsg([]offer{capOffer("m1", 32768, false, nil, 1.0, 72), capOffer("m2", 32768, false, nil, 1.0, 72)}))
	tm, _ = tm.Update(balanceMsg{loggedIn: true, balance: 12.50})
	m := asModel(tm)
	m.cursor = 0
	out, _ := m.connect()
	m = asModel(out)
	require.Equal(t, modeConnectConfirm, m.mode)
	band := m.q.b.model
	other := "m2"
	if band == "m2" {
		other = "m1"
	}
	out, _ = m.Update(offersMsg([]offer{capOffer(other, 32768, false, nil, 1.0, 72)})) // the band is missing
	m = asModel(out)
	require.Equal(t, modeConnectConfirm, m.mode, "a background scan never dismisses the confirm")
	out, _ = m.Update(keyMsg("enter"))
	require.Equal(t, modeConnectConfirm, asModel(out).mode, "accept refuses a stale quote")
	require.Nil(t, asModel(out).connected)

	out, _ = asModel(out).Update(keyMsg("r"))
	// the reply to r (a rescanMsg, not a periodic offersMsg)
	out, _ = asModel(out).Update(rescanMsg{offers: []offer{capOffer(other, 32768, false, nil, 1.0, 72)}, seq: asModel(out).confirmSeq})
	require.Equal(t, modeBrowse, asModel(out).mode, "an explicit re-scan that still finds nothing goes back")
}

// TestRescanUnderAProfileKeepsTheProfile: an explicit re-scan that finds the price above the
// cap while a profile is chosen stays on the confirm (the raise path would drop the profile).
func TestRescanUnderAProfileKeepsTheProfile(t *testing.T) {
	m := auditProfileModel(t, map[string]any{"provider": map[string]any{"max_price": map[string]any{"completion": 2.0}}})
	m.connected = nil
	m.bands = []band{{model: "m", online: true, minOut: 3, cheapest: &offer{Model: "m", NodeID: "n1", PriceOut: 3}}}
	m.mode = modeConnectConfirm
	m.confirmProfile = "p"
	m.q = quote{b: band{model: "m"}}
	m.requote(true)
	require.Equal(t, modeConnectConfirm, m.mode)
	require.Equal(t, "p", m.confirmProfile)
	require.True(t, m.q.overLimit)
}

// TestQuantPickerStartsFromTheBandsOwnRule: opening the band card's quant picker on a band
// with no quant rule of its own and saving it untouched stores nothing from the default.
func TestQuantPickerStartsFromTheBandsOwnRule(t *testing.T) {
	m := auditProfileModel(t, map[string]any{})
	m.limits.Default = Limit{Quants: []string{"Q8_0"}}
	m.bands = []band{{model: "m", quant: "Q8_0", online: true}, {model: "m", quant: "Q4_K_M", online: true}}
	m.cfgModel, m.mode = "m", modeBandConfig
	out, _ := m.Update(keyMsg("Q"))
	m = asModel(out)
	require.Equal(t, modeBandQuants, m.mode)
	for i := range m.quantOpts {
		require.False(t, m.quantSel[i], "nothing is pre-checked from the default's rule")
	}
	require.Empty(t, m.limits.own("m").Quants)
}

// TestRescanFlagNeverOutlivesItsConfirm: r then esc leaves no pending "explicit" re-scan for
// the next confirm, so that confirm's periodic scan still never offers a raise.
func TestRescanFlagNeverOutlivesItsConfirm(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var tm tea.Model = NewWith("http://broker.local", "tester", &LimitStore{Models: map[string]Limit{"m1": {MaxOut: 2}}})
	tm, _ = tm.Update(tea.WindowSizeMsg{Width: 96, Height: 30})
	tm, _ = tm.Update(offersMsg([]offer{capOffer("m1", 32768, false, nil, 1.0, 72)}))
	tm, _ = tm.Update(balanceMsg{loggedIn: true, balance: 12.50})
	m := asModel(tm)
	out, _ := m.connect()
	out, _ = asModel(out).Update(keyMsg("r"))   // re-scan requested...
	out, _ = asModel(out).Update(keyMsg("esc")) // ...then left before it landed
	m = asModel(out)
	out, _ = m.connect() // a new confirm
	m = asModel(out)
	require.Equal(t, modeConnectConfirm, m.mode)
	out, _ = m.Update(offersMsg([]offer{capOffer("m1", 32768, false, nil, 3.0, 72)})) // periodic
	require.Equal(t, modeConnectConfirm, asModel(out).mode, "a periodic scan never offers the raise")
}

// TestAFailedConfigSaveIsShown: when the host cannot write config.json (a lock timeout, an
// unreadable file), the booth says so on its next tick instead of dropping the error.
func TestAFailedConfigSaveIsShown(t *testing.T) {
	m := auditProfileModel(t, map[string]any{})
	m.limits.Save = func(map[string]Limit, Limit) {
		m.limits.ReportSaveErr(fmt.Errorf("config.json is locked by another roger command"))
	}
	m.limits.Set("m", Limit{MaxOut: 1})
	// In browse, with an older toast due to be dismissed on this very tick.
	m.mode, m.status, m.frame, m.statusFrame = modeBrowse, "an old toast", 1000, 1
	out, _ := m.Update(tickMsg{gen: m.tickGen})
	require.Contains(t, stripANSI(asModel(out).status), "not saved", "the dismiss on the same tick must not erase it")
	require.NoError(t, asModel(out).limits.TakeSaveErr(), "shown once")
}

// TestTabAcrossUntouchedFieldsWritesNothing: moving across fields without typing leaves
// config.json alone (no write, no edit counted).
func TestTabAcrossUntouchedFieldsWritesNothing(t *testing.T) {
	m := auditProfileModel(t, map[string]any{})
	saves := 0
	m.limits.Models = map[string]Limit{"m": {MaxOut: 5, MinTPS: 10}}
	m.limits.Save = func(map[string]Limit, Limit) { saves++ }
	m.mode = modeLimits
	m.limModels = []string{"m", defaultLimitRow}
	m.limCursor, m.editField = 0, 0
	m.focusLimitField(lfMaxOut)
	g := m.limits.Gen()
	for i := 0; i < 3; i++ {
		out, _ := m.Update(keyMsg("tab"))
		m = asModel(out)
	}
	require.Zero(t, saves)
	require.Equal(t, g, m.limits.Gen())
}

// TestOverCapConfirmWithoutAProfileNamesNone: the over-cap line names a profile and offers p
// only when one is chosen; otherwise it points at r and esc.
func TestOverCapConfirmWithoutAProfileNamesNone(t *testing.T) {
	m := auditProfileModel(t, map[string]any{})
	m.mode = modeConnectConfirm
	m.confirmProfile = ""
	m.q = quote{b: band{model: "m", minOut: 3, online: true, cheapest: &offer{Model: "m", PriceOut: 3}}, limit: Limit{MaxOut: 2}, overLimit: true}
	v := stripANSI(m.View())
	require.NotContains(t, v, "(profile )")
	require.NotContains(t, v, "p for another")
	require.Contains(t, v, "r to re-scan")
}

// TestBandCardLimitEditTypesOverTheSeedAndSaves: from the band card, typing replaces the
// shown cap (not appends to it) and enter saves what was typed, as on the CONFIG plate.
func TestBandCardLimitEditTypesOverTheSeedAndSaves(t *testing.T) {
	m := auditProfileModel(t, map[string]any{})
	m.limits.Models = map[string]Limit{"m": {MaxOut: 2}}
	m.bands = []band{{model: "m"}}
	m.cfgModel = "m"
	out, _ := m.cfgEditLimit(0)
	m = asModel(out)
	require.Equal(t, "2", m.editBuf)
	out, _ = m.Update(keyMsg("3"))
	require.Equal(t, "3", asModel(out).editBuf, "the first digit replaces the seed")
	out, _ = asModel(out).Update(keyMsg("enter"))
	require.InDelta(t, 3.0, asModel(out).limits.own("m").MaxOut, 1e-9, "enter saves what was typed")
}

// TestOnlyTheExplicitRescanReplyMayOfferARaise: after r, a periodic scan that lands first is
// still periodic (no raise screen); the reply to r itself is what may offer it.
func TestOnlyTheExplicitRescanReplyMayOfferARaise(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var tm tea.Model = NewWith("http://broker.local", "tester", &LimitStore{Models: map[string]Limit{"m1": {MaxOut: 2}}})
	tm, _ = tm.Update(tea.WindowSizeMsg{Width: 96, Height: 30})
	tm, _ = tm.Update(offersMsg([]offer{capOffer("m1", 32768, false, nil, 1.0, 72)}))
	tm, _ = tm.Update(balanceMsg{loggedIn: true, balance: 12.50})
	m := asModel(tm)
	out, _ := m.connect()
	out, _ = asModel(out).Update(keyMsg("r"))
	pricier := []offer{capOffer("m1", 32768, false, nil, 3.0, 72)}
	out, _ = asModel(out).Update(offersMsg(pricier)) // a periodic scan, in flight before r
	require.Equal(t, modeConnectConfirm, asModel(out).mode, "a periodic reply never offers the raise")
	out, _ = asModel(out).Update(rescanMsg{offers: pricier, seq: asModel(out).confirmSeq}) // the reply to r
	require.Equal(t, modeOverLimit, asModel(out).mode)
}

// TestCONFIGNavigationDoesNotRepointTheProxy: moving around [3] CONFIG changes no rule, so it
// re-points nothing; only a key that changed the limit store re-points the live proxy.
func TestCONFIGNavigationDoesNotRepointTheProxy(t *testing.T) {
	m := autoTunedModel(t)
	m.mode = modeLimits
	m.limModels = []string{m.connected.Model, defaultLimitRow}
	m.editField = -1
	m.proxyHolder.SetBand(client.ProxyOptions{Model: "sentinel"}) // marks the last re-point
	out, _ := m.Update(keyMsg("down"))
	require.Equal(t, "sentinel", asModel(out).proxyHolder.Get().Model, "a navigation key re-pointed the proxy")
}

// TestRetypingTheSameValueWritesNothing: an edit that ends on the value already stored (here
// backspace over the seed, then the same number) writes nothing.
func TestRetypingTheSameValueWritesNothing(t *testing.T) {
	m := auditProfileModel(t, map[string]any{})
	saves := 0
	m.limits.Models = map[string]Limit{"m": {MaxOut: 5}}
	m.limits.Save = func(map[string]Limit, Limit) { saves++ }
	m.mode = modeLimits
	m.limModels = []string{"m", defaultLimitRow}
	m.limCursor, m.editField = 0, 0
	m.focusLimitField(lfMaxOut)
	for _, k := range []string{"backspace", "5", "enter"} {
		out, _ := m.Update(keyMsg(k))
		m = asModel(out)
	}
	require.Zero(t, saves)
	require.InDelta(t, 5.0, m.limits.own("m").MaxOut, 1e-9)
}

// TestFailedBindKeepsThePreviousProfile: accepting under a profile whose bind then fails
// leaves the booth on the profile it had, never on one that bound nothing.
func TestFailedBindKeepsThePreviousProfile(t *testing.T) {
	m := auditProfileModel(t, map[string]any{})
	m.tunedProfile = "before"
	m.confirmProfile = "p"
	m.proxyUp, m.proxyHolder = false, nil
	m.proxyAddr = "256.0.0.1:1" // cannot bind
	m.q = quote{b: band{model: "m", online: true, cheapest: &offer{Model: "m", NodeID: "n1"}}}
	out, _ := m.openChannel()
	require.Equal(t, "before", asModel(out).tunedProfile)
}

// TestDefaultRowEditCountsAsAnEdit: editing the default row changes what every band resolves,
// so it counts as a store write (the live proxy re-points on it like on a band edit).
func TestDefaultRowEditCountsAsAnEdit(t *testing.T) {
	m := auditProfileModel(t, map[string]any{})
	g := m.limits.Gen()
	m.putRowLimit(defaultLimitRow, Limit{MaxOut: 4})
	require.NotEqual(t, g, m.limits.Gen())
}

// TestALateRescanReplyNeverReachesANewConfirm: the reply to an r pressed on an earlier
// confirm is a periodic scan to the confirm now open: it never offers a raise there.
func TestALateRescanReplyNeverReachesANewConfirm(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var tm tea.Model = NewWith("http://broker.local", "tester", &LimitStore{Models: map[string]Limit{"m1": {MaxOut: 2}}})
	tm, _ = tm.Update(tea.WindowSizeMsg{Width: 96, Height: 30})
	tm, _ = tm.Update(offersMsg([]offer{capOffer("m1", 32768, false, nil, 1.0, 72)}))
	tm, _ = tm.Update(balanceMsg{loggedIn: true, balance: 12.50})
	m := asModel(tm)
	out, _ := m.connect()
	old := asModel(out).confirmSeq
	out, _ = asModel(out).Update(keyMsg("r"))
	out, _ = asModel(out).Update(keyMsg("esc"))
	out, _ = asModel(out).connect() // a new confirm
	out, _ = asModel(out).Update(rescanMsg{offers: []offer{capOffer("m1", 32768, false, nil, 3.0, 72)}, seq: old})
	require.Equal(t, modeConnectConfirm, asModel(out).mode, "the earlier confirm's reply offers nothing here")
}

// TestRescanOnAPrivateFrequencyIsRefused: r re-scans the open market, which a private
// frequency confirm never reads; it says so instead of waiting on a scan that cannot answer.
func TestRescanOnAPrivateFrequencyIsRefused(t *testing.T) {
	m := auditProfileModel(t, map[string]any{})
	m.mode, m.tuneFreq = modeConnectConfirm, "CODE"
	m.q = quote{b: band{model: "m"}}
	out, cmd := m.Update(keyMsg("r"))
	require.Nil(t, cmd, "no scan is started")
	require.NotContains(t, stripANSI(asModel(out).status), "re-scanning")
}
