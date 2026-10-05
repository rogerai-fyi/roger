package tui

// PROFILES IN THE BOOTH (features/tui/routing_profiles.feature): the named routing profiles of
// config.json are listed under the [3] CONFIG table (read-only: `roger profile set` edits
// them), offered on the TUNE IN confirm (p cycles), and the one tuned under rides every
// in-booth path for the connected band.

import (
	"fmt"
	"strings"

	"rogerai.fm/roger/v6/internal/client"
)

// profiles is the current named-profile set (never nil).
func (m model) profiles() *client.Profiles {
	if m.limits != nil && m.limits.Profiles != nil {
		return m.limits.Profiles.Get()
	}
	ps, _ := client.ParseProfiles([]byte(`{}`))
	return ps
}

// profileBody is a named profile's body object (nil when there is none by that name).
func (m model) profileBody(name string) map[string]any {
	if name == "" {
		return nil
	}
	if p, err, ok := m.profiles().Get(name); ok && err == nil {
		return p.Body
	}
	return nil
}

// profileSummary is a profile's one-line summary in the routing vocabulary.
func profileSummary(b map[string]any) string {
	var parts []string
	for _, it := range client.RoutingPhrases(b) {
		parts = append(parts, it[1])
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, " · ")
}

// profileRowNames are the profile rows below the band table: default, then the named ones.
func (m model) profileRowNames() []string {
	names := m.profiles().Names()
	if len(names) == 0 {
		return nil
	}
	return append([]string{client.DefaultProfileName}, names...)
}

// onProfileRow reports the profile the CONFIG cursor is on ("" on a band row).
func (m model) onProfileRow() string {
	rows := m.profileRowNames()
	if i := m.limCursor - len(m.limModels); i >= 0 && i < len(rows) {
		return rows[i]
	}
	return ""
}

// profilesSection renders the profiles list and, for an opened row, its resolved keys with
// their sources (a band code shown only as "(set, hidden)").
func (m model) profilesSection(w int) []string {
	rows := m.profileRowNames()
	if len(rows) == 0 {
		return nil
	}
	lines := []string{"", "  " + stBrand.Render("profiles") + stDim.Render("   read-only here · roger profile set edits them")}
	sel := m.onProfileRow()
	for _, n := range rows {
		cur := " "
		if n == sel && !m.limOnBudget {
			cur = stSelBar.Render("▌")
		}
		summary := "built from limits.*"
		if n != client.DefaultProfileName {
			summary = profileSummary(m.profileBody(n))
		}
		lines = append(lines, truncVisibleTail(fmt.Sprintf("%s   %s %s", cur, pad(n, 14), stDim.Render(summary)), w))
	}
	if sel != "" && m.limProfileOpen {
		lines = append(lines, "", "    "+stDim.Render("resolved keys for "+sel+":"))
		body := map[string]any{}
		src := "limits.default"
		if sel != client.DefaultProfileName {
			body, src = m.profileBody(sel), "profile "+sel
		}
		for _, it := range client.RoutingPhrases(body) {
			text := it[1]
			if it[0] == "roger.freq" {
				text = "(set, hidden)"
			}
			lines = append(lines, truncVisibleTail("      "+stDim.Render(fmt.Sprintf("%-30s %s  (%s)", it[0], text, src)), w))
		}
		lines = append(lines, "    "+stDim.Render("edit with `roger profile set "+sel+" <key> <value>`"))
	}
	return lines
}

// nextProfile cycles the TUNE IN confirm's profile: default (none), then each named one.
func (m model) nextProfile(cur string) string {
	names := append([]string{""}, m.profiles().Names()...)
	for i, n := range names {
		if n == cur {
			return names[(i+1)%len(names)]
		}
	}
	return ""
}

// confirmRoutingLine is the confirm's profile line ("" when no named profile exists).
func (m model) confirmRoutingLine() string {
	if len(m.profiles().Names()) == 0 {
		return ""
	}
	name := m.confirmProfile
	if name == "" {
		name = client.DefaultProfileName
	}
	return stDim.Render("routing: ") + stKey.Render(name) + stDim.Render(" · p cycles profile")
}

// profileFor is the profile body that applies to a turn on model: the one tuned under, for
// the connected band only.
func (m model) profileFor(model string) map[string]any {
	if m.connected == nil || m.connected.Model != model {
		return nil
	}
	return m.profileBody(m.tunedProfile)
}

// confirmLimit is the rule the confirm prices against: the band's, with the stricter of its
// out cap and that of the profile being offered (the pair the proxy enforces).
func (m model) confirmLimit(model string) Limit {
	lim := m.limits.resolve(model)
	if v, ok := m.profileBody(m.confirmProfile)["provider"].(map[string]any); ok {
		if mp, ok := v["max_price"].(map[string]any); ok {
			if c, ok := mp["completion"].(float64); ok && c > 0 {
				lim.MaxOut = stricterCap(lim.MaxOut, c)
			}
		}
	}
	return lim
}

// profileStore is the booth's profile store, or nil (the proxy then reads config.json itself).
func (m model) profileStore() *client.ProfileStore {
	if m.limits == nil {
		return nil
	}
	return m.limits.Profiles
}
