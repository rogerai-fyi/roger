package tui

// THE BAND CARD'S TOGGLES AND THE PRIVATE TAB'S ON-AIR KEY against a real broker - an httptest
// server speaking the register/heartbeat API, so the controller starts and stops real sessions.
// Each test asserts what the operator sees (status line, mode) and what the controller now holds.

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/agent"
	"rogerai.fm/roger/v6/internal/node"
)

const cardModel = "grok-4.6"

// cardBroker answers register with a freshly minted private band (or refuses it with refuse) and
// acknowledges everything else.
func cardBroker(t *testing.T, refuse string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/nodes/register" && refuse != "" {
			http.Error(w, refuse, http.StatusForbidden)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true,
			"band_id": "band_new", "band_code": "8F3K-QX7P", "band_display": "145.225 MHz"})
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func cardRig(t *testing.T, broker string, loggedIn bool) model {
	t.Helper()
	var tm tea.Model = New(broker, "tester")
	tm, _ = tm.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m := asModel(tm)
	m.loggedIn = loggedIn
	m.ctrl.SetLoggedIn(loggedIn)
	m.ctrl.Rename("eager-puma-54")
	m.station = "eager-puma-54"
	m.ctrl.SetRows([]node.ShareRow{{Model: cardModel, Upstream: "http://127.0.0.1:0/v1/chat/completions"}})
	m.syncShareCache()
	t.Cleanup(m.ctrl.StopAll)
	return m
}

func openCard(t *testing.T, m model) model {
	t.Helper()
	mm, _ := m.openBandConfig(cardModel, modeBrowse)
	return asModel(mm)
}

func TestTheCardArmsAndDisarmsAutoStart(t *testing.T) {
	m := openCard(t, cardRig(t, cardBroker(t, ""), true))
	require.Contains(t, stripANSI(m.View()), "arms itself when you put", "undecided renders as absent, never as off")

	got, _ := send(m, keyRunes("s"))
	on, set := got.ctrl.AutoStartFor(cardModel)
	require.True(t, on && set)
	require.Contains(t, stripANSI(got.status), "will go on air when roger starts")
	require.Contains(t, stripANSI(got.View()), "goes on air when")

	got, _ = send(got, keyRunes("s"))
	on, set = got.ctrl.AutoStartFor(cardModel)
	require.True(t, set)
	require.False(t, on)
	require.Contains(t, stripANSI(got.status), "will no longer auto-start")
	require.Contains(t, stripANSI(got.View()), "stays off until")
}

func TestTheCardPutsItsModelOnAndOffAir(t *testing.T) {
	m := openCard(t, cardRig(t, cardBroker(t, ""), true))
	got, _ := send(m, keyRunes("a"))
	require.True(t, got.ctrl.IsOnAir(cardModel), "a on the card is the SAME toggle [2] SHARE makes")
	got, _ = send(got, keyRunes("a"))
	require.False(t, got.ctrl.IsOnAir(cardModel))
}

func TestTheCardHidesItsModelOntoAPrivateBand(t *testing.T) {
	m := openCard(t, cardRig(t, cardBroker(t, ""), true))
	got, _ := send(m, keyRunes("h"))
	require.Equal(t, modeBandCard, got.mode, "a fresh mint shows its one-time code card")
	require.Equal(t, "8F3K-QX7P", got.bandCardCode)
	require.True(t, got.bandCardReturnSet && got.bandCardReturn == modeBandConfig,
		"the code card returns to the band card, not to a share table the operator never opened")
	require.True(t, got.ctrl.Private()[cardModel])
}

func TestTheCardsPrivateToggleNeedsALogin(t *testing.T) {
	m := openCard(t, cardRig(t, cardBroker(t, ""), false))
	got, _ := send(m, keyRunes("h"))
	require.Equal(t, modeBandConfig, got.mode)
	require.Contains(t, stripANSI(got.status), "log in to go private")
	require.False(t, got.ctrl.IsOnAir(cardModel))
}

func TestAQuotaRefusalOffersTheMoveAndYAcceptsIt(t *testing.T) {
	m := cardRig(t, cardBroker(t, `{"error":"private band limit reached (free plan allows 1)"}`), true)
	m.mode = modeShare
	got, _ := send(m, keyRunes("h"))
	require.Equal(t, cardModel, got.bandMoveOffer, "the refusal offers the fix right here")
	require.Contains(t, stripANSI(got.status), "band limit reached")

	var moved []string
	got.hooks.BandList = func(string) ([]BandRow, error) { return []BandRow{{ID: "band_1"}}, nil }
	got.hooks.BandMove = func(_, id, nodeID string) error { moved = append(moved, id+"->"+nodeID); return nil }
	after, cmd := send(got, keyRunes("y"))
	require.Equal(t, "", after.bandMoveOffer, "the offer is consumed")
	require.Contains(t, stripANSI(after.status), "moving your band to "+cardModel)
	require.NotNil(t, cmd)
	msg := cmd().(bandActionMsg)
	require.True(t, msg.moved)
	require.Equal(t, []string{"band_1->" + agent.ShareNodeID("eager-puma-54", cardModel, 0)}, moved,
		"the node id is the one the share path registers with")

	// y with no standing offer is inert.
	_, cmd = send(after, keyRunes("y"))
	require.Nil(t, cmd)
}

func TestAcceptBandMoveRefusesHonestly(t *testing.T) {
	m := cardRig(t, "http://broker.local", true)
	m.bandMoveOffer = cardModel
	run := func(m model) bandActionMsg { return m.acceptBandMove()().(bandActionMsg) }

	require.Contains(t, run(m).err, "unavailable in this build")

	m.hooks.BandMove = func(string, string, string) error { return errors.New("broker says no") }
	m.hooks.BandList = func(string) ([]BandRow, error) { return nil, errors.New("bands down") }
	require.Equal(t, "bands down", run(m).err)

	m.hooks.BandList = func(string) ([]BandRow, error) { return nil, nil }
	require.Contains(t, run(m).err, "no band to move")

	m.hooks.BandList = func(string) ([]BandRow, error) { return []BandRow{{ID: "a"}, {ID: "b"}}, nil }
	require.Contains(t, run(m).err, "pick one in BASE STATION")

	m.hooks.BandList = func(string) ([]BandRow, error) { return []BandRow{{ID: "a"}}, nil }
	require.Equal(t, "broker says no", run(m).err)
}

// privTabRow parks the PRIVATE tab cursor on the band with the given id.
func privTabRow(t *testing.T, m model, id string) model {
	t.Helper()
	for i, r := range m.privRows() {
		if r.band.ID == id {
			m.privCursor = i
			return m
		}
	}
	t.Fatalf("no private row for %s", id)
	return m
}

func TestThePrivateTabsOnAirKeyExplainsEveryRefusal(t *testing.T) {
	m := privateTab(t)
	got, _ := send(privTabRow(t, m, "band_dead"), keyRunes("a"))
	require.Contains(t, stripANSI(got.status), "this band is revoked")

	got, _ = send(privTabRow(t, m, "band_away"), keyRunes("a"))
	require.Contains(t, stripANSI(got.status), "on another machine - put it on air over there")

	got, _ = send(privTabRow(t, privateTabNoServer(t), "band_here"), keyRunes("a"))
	require.Contains(t, stripANSI(got.status), "nothing on this machine is serving")
}

func TestThePrivateTabsOnAirKeyAgainstARealBroker(t *testing.T) {
	m := cardRig(t, cardBroker(t, ""), true)
	m.mode, m.tuneTab = modeBrowse, tabPrivate
	m.rcBands = []BandRow{{ID: "band_here", Display: "145.225 MHz", Status: "active",
		NodeID: agent.ShareNodeID("eager-puma-54", cardModel, 0)}}
	m = privTabRow(t, m, "band_here")

	// Not hidden on this controller, so it comes back PUBLIC - and the screen says so loudly.
	got, _ := send(m, keyRunes("a"))
	require.True(t, got.ctrl.IsOnAir(cardModel))
	require.Contains(t, stripANSI(got.status), "went on air PUBLICLY")

	got, _ = send(got, keyRunes("a"))
	require.False(t, got.ctrl.IsOnAir(cardModel))
	require.Contains(t, stripANSI(got.status), "off air - "+cardModel+" is no longer reachable")

	// Hidden first (h in SHARE mints the band), then off and back on: it resumes PRIVATE.
	got.mode = modeShare
	got, _ = send(got, keyRunes("h"))
	require.True(t, got.ctrl.Private()[cardModel])
	got.mode, got.tuneTab = modeBrowse, tabPrivate
	got = privTabRow(t, got, "band_here")
	got, _ = send(got, keyRunes("a")) // off
	got, _ = send(got, keyRunes("a")) // on, at the remembered visibility
	require.True(t, got.ctrl.IsOnAir(cardModel))
	require.Contains(t, stripANSI(got.status), "hidden, reachable only with its code")

	// Logged out, a private start is refused rather than going public.
	got, _ = send(got, keyRunes("a")) // off
	got.loggedIn = false
	got.ctrl.Logout() // SetLoggedIn is raise-only; signing out is explicit
	got, _ = send(got, keyRunes("a"))
	require.False(t, got.ctrl.IsOnAir(cardModel))
	require.True(t, strings.Contains(stripANSI(got.status), "log in to put a private band on air"))
}

func sendMsg(m model, msg tea.Msg) (model, tea.Cmd) {
	tm, cmd := m.Update(msg)
	return asModel(tm), cmd
}

// A band action that lands goes back to where it was STARTED and says what happened.
func TestABandActionLandsWhereItStartedAndSaysWhatHappened(t *testing.T) {
	m := cardRig(t, "http://broker.local", true)
	for _, tc := range []struct {
		msg  bandActionMsg
		want string
	}{
		{bandActionMsg{err: "broker on fire"}, "! broker on fire"},
		{bandActionMsg{labeled: true, model: "home gpu"}, "named home gpu"},
		{bandActionMsg{labeled: true}, "name cleared"},
		{bandActionMsg{forgotten: true}, "gone from your list for good"},
		{bandActionMsg{moved: true, model: cardModel}, "moved - " + cardModel + " now answers on the same frequency code"},
		{bandActionMsg{revoked: true, node: "somewhere-else-llama"}, "revoked - that frequency code no longer resolves"},
	} {
		m.mode, m.cfgModel = modeBandManage, ""
		got, _ := sendMsg(m, tc.msg)
		require.Equal(t, modePrivate, got.mode, "%+v lands on BASE STATION", tc.msg)
		require.Contains(t, stripANSI(got.status), tc.want)

		m.cfgModel = cardModel
		got, _ = sendMsg(m, tc.msg)
		require.Equal(t, modeBandConfig, got.mode, "%+v started on the band card returns to it", tc.msg)
	}
}

// Revoking the band a PRIVATE on-air model hides behind takes that model off air - never back onto
// the open market.
func TestRevokingAHiddenModelsBandTakesItOffAir(t *testing.T) {
	m := cardRig(t, cardBroker(t, ""), true)
	m.mode = modeShare
	m, _ = send(m, keyRunes("h"))
	require.True(t, m.ctrl.IsOnAir(cardModel))
	require.True(t, m.ctrl.Private()[cardModel])

	got, cmd := sendMsg(m, bandActionMsg{revoked: true, node: agent.ShareNodeID("eager-puma-54", cardModel, 0)})
	require.False(t, got.ctrl.IsOnAir(cardModel))
	require.Contains(t, stripANSI(got.status), cardModel+" is off air")
	require.NotNil(t, cmd, "the roster is re-fetched so the list shows what happened")
}
