package tui

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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
