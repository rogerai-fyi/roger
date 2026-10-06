package tui

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"
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
	require.NotEqual(t, modeConnectConfirm, asModel(out).mode, "a band gone from the scan is not offered for accept")
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
