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
