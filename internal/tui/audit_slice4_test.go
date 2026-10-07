package tui

// audit_slice4_test.go - regressions from the slice-4 pre-push audit: the live proxy carries
// the WHOLE tuned profile (as chat and agent turns do), and every dial toggle that binds
// routing re-points the live proxy at once, not on the next tune.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/client"
)

func auditProfileModel(t *testing.T, body map[string]any) model {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	b, _ := json.Marshal(map[string]any{"profiles": map[string]any{"p": body}})
	require.NoError(t, os.WriteFile(path, b, 0o600))
	m := NewWith("http://127.0.0.1:1", "u", &LimitStore{Profiles: client.NewProfileStore(path)})
	m.connected = &offer{Model: "m", NodeID: "n1"}
	m.tunedProfile = "p"
	return m
}

func TestLiveProxyCarriesTheWholeTunedProfile(t *testing.T) {
	m := auditProfileModel(t, map[string]any{
		"models": []any{"m", "m2"},
		"provider": map[string]any{
			"sort": "price", "only": []any{"n1", "n2"}, "order": []any{"n2"},
			"ignore": []any{"n9"}, "allow_fallbacks": false, "require_parameters": true,
		},
	})
	o := m.liveProxyOpts(*m.connected, m.alert)
	require.Equal(t, "price", o.Sort)
	require.Equal(t, []string{"m", "m2"}, o.Models)
	require.Equal(t, []string{"n1", "n2"}, o.Only)
	require.Equal(t, []string{"n2"}, o.Prefer)
	require.True(t, o.NoFallbacks)
	require.True(t, o.RequireParams)
	require.Contains(t, o.ExcludeNodes, "n9")
}

func TestDialTogglesRepointTheLiveProxy(t *testing.T) {
	m := auditProfileModel(t, map[string]any{})
	m.mode = modeBrowse
	m.proxyHolder = client.NewProxyOptionsHolder(m.liveProxyOpts(*m.connected, m.alert))
	press := func(k string) {
		nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)})
		m = nm.(model)
	}
	press("U")
	require.True(t, m.proxyHolder.Get().SelfHostedOnly, "U hides curated on the live proxy now")
	press("C")
	require.True(t, m.proxyHolder.Get().Confidential, "C binds confidential on the live proxy now")
	press("F")
	require.True(t, m.proxyHolder.Get().FreeOnly, "F binds :free on the live proxy now")
	press("C")
	require.False(t, m.proxyHolder.Get().Confidential)

	nm, _ := m.run("confidential")
	m = nm.(model)
	require.True(t, m.proxyHolder.Get().Confidential, "/confidential (command line) re-points the proxy")
	nm, _ = m.runSession("/confidential")
	m = nm.(model)
	require.False(t, m.proxyHolder.Get().Confidential, "/confidential (session) re-points the proxy")
}

func TestGuestHandoffPinsTheTunedProfile(t *testing.T) {
	m := auditProfileModel(t, map[string]any{"provider": map[string]any{"sort": "price"}})
	opts := m.liveProxyOpts(*m.connected, m.alert)
	require.Same(t, m.limits.Profiles, opts.Profiles, "the live proxy resolves @profile/ against the booth's store")

	s := m.operatorSession(opts, "/w", false)
	require.Equal(t, "p", s.Profile, "the guest pins the profile the band was tuned under")
	require.Equal(t, "m", s.Model)

	require.Empty(t, m.operatorSession(opts, "/w", true).Profile, "a bandless guest has no band to route")
	m.tunedProfile = ""
	require.Empty(t, m.operatorSession(opts, "/w", false).Profile, "no profile tuned, none pinned")
}

func TestProfileSwitchReRunsTheOverLimitCheck(t *testing.T) {
	m := auditProfileModel(t, map[string]any{"provider": map[string]any{"max_price": map[string]any{"completion": 2.0}}})
	m.connected = nil
	m.mode = modeConnectConfirm
	m.q = quote{b: band{model: "m", minOut: 5, online: true, cheapest: &offer{NodeID: "n1", Model: "m", Online: true}}, limit: m.confirmLimit("m")}
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("p")})
	m = nm.(model)
	require.Equal(t, "p", m.confirmProfile)
	require.True(t, m.q.overLimit, "the band's $5 is over profile p's $2 cap")
	require.NotContains(t, m.confirmView(100), "under your", "the plate never claims a cap it breaks")
	require.Contains(t, m.confirmView(100), "over your $2/1M cap")

	nm, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = nm.(model)
	require.Equal(t, modeConnectConfirm, m.mode, "accepting is not offered over the profile cap")
	require.Nil(t, m.connected)

	nm, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("p")}) // back to the default (no cap)
	m = nm.(model)
	require.Empty(t, m.confirmProfile)
	require.False(t, m.q.overLimit)
}

// TestTunedProfileCapsComposeWithTheBandLimit: the tuned profile's price caps and min-tps
// floor reach the live proxy and the chat/agent routing object, composed with the band's
// own rule the stricter way (the lower cap, the higher floor) - and opening a channel
// (refreshLiveRouting) does not drop them back to the band limit.
func TestTunedProfileCapsComposeWithTheBandLimit(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		bandOut, bandIn, bandTPS float64
		profOut, profIn, profTPS float64
		wantOut, wantIn, wantTPS float64
	}{
		{"profile stricter", 5, 1, 10, 2, 0.5, 30, 2, 0.5, 30},
		{"band stricter", 2, 0.5, 30, 5, 1, 10, 2, 0.5, 30},
		{"profile only", 0, 0, 0, 3, 0.4, 25, 3, 0.4, 25},
		{"band only", 4, 0.3, 15, 0, 0, 0, 4, 0.3, 15},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prof := map[string]any{"roger": map[string]any{}, "provider": map[string]any{"max_price": map[string]any{}}}
			mp := prof["provider"].(map[string]any)["max_price"].(map[string]any)
			if tc.profOut > 0 {
				mp["completion"] = tc.profOut
			}
			if tc.profIn > 0 {
				mp["prompt"] = tc.profIn
			}
			if tc.profTPS > 0 {
				prof["roger"].(map[string]any)["min_tps"] = tc.profTPS
			}
			m := auditProfileModel(t, prof)
			m.limits.Models = map[string]Limit{"m": {MaxOut: tc.bandOut, MaxIn: tc.bandIn, MinTPS: tc.bandTPS}}
			m.proxyHolder = client.NewProxyOptionsHolder(m.liveProxyOpts(*m.connected, m.alert))
			m.refreshLiveRouting() // what openChannel does

			o := m.proxyHolder.Get()
			require.Equal(t, tc.wantOut, o.MaxPriceOut, "live proxy out cap")
			require.Equal(t, tc.wantIn, o.MaxPriceIn, "live proxy in cap")
			require.Equal(t, tc.wantTPS, o.MinTPS, "live proxy min-tps floor")

			rt := m.routing("m", "")
			require.Equal(t, tc.wantOut, rt.MaxOut, "chat/agent routing out cap")
			require.Equal(t, tc.wantIn, rt.MaxIn, "chat/agent routing in cap")
			require.Equal(t, tc.wantTPS, rt.MinTPS, "chat/agent routing min-tps floor")
		})
	}
}

// TestTUIResolvesLimitsByThePerKeyMerge: the TUI resolves a band's rule exactly as the CLI
// does - the Default with each key the band sets laid over it - so a default rule key the
// band leaves unset still binds in the booth.
func TestTUIResolvesLimitsByThePerKeyMerge(t *testing.T) {
	for _, tc := range LimitMergeCases() {
		t.Run(tc.Name, func(t *testing.T) {
			s := &LimitStore{Default: tc.Default, Models: map[string]Limit{"m": tc.Model}}
			require.Equal(t, tc.Want, s.Resolve("m"))
		})
	}
}

// TestTunedProfileDoesNotOutliveItsChannel: the tuned profile belongs to the channel opened
// through the confirm. Disconnecting, opening a local direct channel, or an auto-tune that
// bypasses the confirm all start with no profile, so a later tune never inherits a stale one.
func TestTunedProfileDoesNotOutliveItsChannel(t *testing.T) {
	m := auditProfileModel(t, map[string]any{"roger": map[string]any{"pref": "fast"}})
	dm, _ := m.disconnect()
	require.Empty(t, asModel(dm).tunedProfile, "disconnect clears the tuned profile")

	m = auditProfileModel(t, map[string]any{"roger": map[string]any{"pref": "fast"}})
	m = m.openLocalChannel(privRow{model: "m", chat: "http://127.0.0.1:1/v1/chat/completions"})
	require.Empty(t, m.tunedProfile, "a local direct channel carries no tuned profile")

	a := freshDeskAgent(t, []offer{freeOffer("gpt-oss-20b", 32768)})
	a.tunedProfile = "p"
	_ = a.runAutoTune()
	require.NotNil(t, a.connected, "the auto-tune bound a band")
	require.Empty(t, a.tunedProfile, "an auto-tune bypasses the confirm, so it binds no profile")
}

// TestRefreshLiveRoutingSkipsALocalChannel: a CONFIG edit or an F/C/U toggle while a local
// direct channel is open must not re-point (and re-connect) the broker proxy, which the
// disconnect before it left refusing.
func TestRefreshLiveRoutingSkipsALocalChannel(t *testing.T) {
	m := auditProfileModel(t, map[string]any{})
	m.proxyHolder = client.NewProxyOptionsHolder(m.liveProxyOpts(*m.connected, m.alert))
	m.proxyHolder.Disconnect()
	m = m.openLocalChannel(privRow{model: "m", chat: "http://127.0.0.1:1/v1/chat/completions"})
	m.refreshLiveRouting()
	require.False(t, m.proxyHolder.Connected(), "the broker proxy stays disconnected under a local channel")
}

// TestConfirmLimitKeepsTheStricterCap: the confirm prices against the stricter of the band's
// out cap and the offered profile's, the way the proxy enforces them; a looser profile cap
// never shows on the plate.
func TestConfirmLimitKeepsTheStricterCap(t *testing.T) {
	for _, tc := range []struct{ band, prof, want float64 }{
		{2, 5, 2}, // the profile is looser: the band's cap stands
		{5, 2, 2}, // the profile is stricter: it wins
		{0, 3, 3}, // the band sets none: the profile's
	} {
		m := auditProfileModel(t, map[string]any{"provider": map[string]any{"max_price": map[string]any{"completion": tc.prof}}})
		m.limits.Models = map[string]Limit{"m": {MaxOut: tc.band}}
		m.confirmProfile = "p"
		require.Equal(t, tc.want, m.confirmLimit("m").MaxOut, "band %v, profile %v", tc.band, tc.prof)
	}
}
