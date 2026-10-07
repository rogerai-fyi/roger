package tui

// LAN mode in the TUI (features/lan/): the dial reads the ONE merged LAN dial the CLI and the
// console also read (cmd/rogerai lanDial), so no surface can show a different LAN.

import (
	"fmt"
	tea "github.com/charmbracelet/bubbletea"
	"net"
	"sort"
	"strings"
	"sync"

	"rogerai.fm/roger/v6/internal/client"
)

var (
	lanMu   sync.RWMutex
	lanDial func() client.LANDial
)

// SetLANDial puts the TUI in LAN mode for this process (fn non-nil) with the shared dial, or
// back in the default mode (nil). The mode is decided once per process by the CLI.
func SetLANDial(fn func() client.LANDial) {
	lanMu.Lock()
	defer lanMu.Unlock()
	lanDial = fn
}

// LANMode reports whether this TUI runs in LAN mode.
func LANMode() bool {
	lanMu.RLock()
	defer lanMu.RUnlock()
	return lanDial != nil
}

// lanBadge is the LAN mark the header carries in place of the balance tag (ruled 2026-10-06): a
// word, readable with NO_COLOR, in bright ink and never the brand red.
func lanBadge(compact bool) string {
	if compact {
		return stKey.Render("LAN")
	}
	return stKey.Render("LAN") + stDim.Render(" · local, free")
}

// headerTag is the header's right-hand tag: the LAN badge in LAN mode, the account tag otherwise.
// The account section still shows the balance (accountTag), one keystroke away.
func (m model) headerTag(compact bool) string {
	if LANMode() {
		return lanBadge(compact)
	}
	return m.accountTag(compact)
}

// lanTowerStatus is the last LAN dial's Tower lines, for the empty-LAN status.
var (
	lanTowerStatus []client.LANTowerStatus
	lanEdgeUnread  string // why the last dial could not read this Edge's record; "" = read
)

// lanOffers reads the LAN dial as dial rows: free, online, one per band, naming its sources.
func lanOffers() offersMsg {
	lanMu.RLock()
	fn := lanDial
	lanMu.RUnlock()
	d := fn()
	bands := d.Bands
	lanMu.Lock()
	lanTowerStatus, lanEdgeUnread = d.Towers, d.EdgeUnread
	lanMu.Unlock()
	out := make([]offer, 0, len(bands))
	for _, b := range bands {
		out = append(out, offer{Model: b.Model, NodeID: lanSourceText(b), Online: true, FreeNow: true,
			Modality: "chat", LANSource: b.SourceLabel(), LANSources: b.Sources})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Model < out[j].Model })
	return offersMsg(out)
}

// lanShareView is SHARE in LAN mode (features/lan/share_in_lan_mode.feature): where the station is
// on air (each LAN Tower, and the Edge), and each row on or off. Sharing on the LAN is free and
// registers nothing, so no price, earnings or payout text appears.
func (m model) lanShareView(w int) string {
	var b strings.Builder
	b.WriteString("  " + stSelBar.Render("▌") + " " + stBrand.Render("SHARE") + stDim.Render("   LAN · free, registers with nothing") + "\n")
	b.WriteString("  " + stDim.Render("station ") + stKey.Render(m.station) + "\n")
	if m.anyLiveShare() {
		var targets []string
		if m.hooks.LANTowers != nil {
			for _, base := range m.hooks.LANTowers() {
				targets = append(targets, "tower: "+lanTowerHost(base))
			}
		}
		targets = append(targets, "edge")
		b.WriteString("  " + truncVisible(stKey.Render("on air: LAN")+stDim.Render(" · "+strings.Join(targets, " · ")), w-4) + "\n")
	}
	b.WriteString("\n")
	for i, r := range m.shareRows {
		mark := "  "
		if i == m.shareCursor {
			mark = stSelBar.Render("▌") + " "
		}
		status := stDim.Render("off")
		if m.shares[r.model] != nil {
			status = stKey.Render("ON AIR")
		}
		b.WriteString("  " + mark + truncVisible(fmt.Sprintf("%-24s  ", r.model), 26) + status + "\n")
	}
	b.WriteString("\n  " + truncVisible(stKey.Render("enter")+stDim.Render("/")+stKey.Render("a")+stDim.Render(" toggles on/off air on the LAN"), w-4) + "\n")
	return b.String()
}

// lanTowerHost is a Tower's host for display: "http://192.168.1.20:8787" reads "192.168.1.20".
func lanTowerHost(base string) string {
	h := strings.TrimPrefix(strings.TrimPrefix(base, "http://"), "https://")
	if host, _, err := net.SplitHostPort(h); err == nil {
		return host
	}
	return h
}

// lanSourceText is a dial row's source column: each source, with an Edge member marked verified
// (its certificate was checked) and a Tower marked unverified (trust-the-LAN, ruled 2026-10-06).
// Words, so the difference reads with NO_COLOR.
func lanSourceText(b client.LANBand) string {
	var out []string
	for _, src := range b.Sources {
		label := src.Label()
		switch src.Kind {
		case client.LANEdge:
			if src.Verified {
				label += " (verified)"
			}
		case client.LANTower:
			label += " (unverified)"
		}
		out = append(out, label)
	}
	return strings.Join(out, ", ")
}

// lanAddNote is the add confirm's one sentence on the trust-the-LAN exposure.
const lanAddNote = "a Tower speaks plain http: any device on this network can pose as it, and it can read what you send to it"

// lanCandidates is the last dial's candidate Tower lines: advertised, not added, never used.
func (m model) lanCandidates() []client.LANTowerStatus {
	lanMu.RLock()
	defer lanMu.RUnlock()
	var out []client.LANTowerStatus
	for _, st := range lanTowerStatus {
		if st.State == client.LANTowerCandidate {
			out = append(out, st)
		}
	}
	return out
}

// lanCandidateView is the dial's candidate lines and, when the owner pressed +, the add confirm.
func (m model) lanCandidateView(w int) string {
	cands := m.lanCandidates()
	if len(cands) == 0 {
		return ""
	}
	var b strings.Builder
	for _, c := range cands {
		if c.Addr == "" { // the flood summary
			b.WriteString("  " + truncVisible(stDim.Render(c.Detail), w-4) + "\n")
			continue
		}
		mark := "  "
		if c.Addr == m.lanAddSel {
			mark = stKey.Render("▸ ")
		}
		b.WriteString("  " + truncVisible(mark+stDim.Render("candidate Tower ")+stKey.Render(c.Addr)+
			stDim.Render(" · "+c.Detail+" · + to add"), w-4) + "\n")
	}
	if m.lanAddSel != "" {
		b.WriteString("\n  " + stKey.Render("add the Tower at "+m.lanAddSel+"?") + "\n")
		b.WriteString("  " + truncVisible(stDim.Render(lanAddNote+"."), w-4) + "\n")
		b.WriteString("  " + stKey.Render("y") + stDim.Render(" add · ") + stKey.Render("n") + stDim.Render(" cancel · ") +
			stKey.Render("+") + stDim.Render(" next candidate") + "\n")
	}
	return b.String()
}

// lanAddedMsg is the result of adding a candidate Tower from the dial.
type lanAddedMsg struct {
	line string
	err  error
}

// lanCandidateKey handles the dial's add flow in LAN mode; ok=false hands the key back.
func (m model) lanCandidateKey(key string) (model, tea.Cmd, bool) {
	if !LANMode() {
		return m, nil, false
	}
	if m.lanAddSel != "" {
		switch key {
		case "y", "Y":
			addr, add := m.lanAddSel, m.hooks.LANAddTower
			m.lanAddSel = ""
			if add == nil {
				m.status = stDim.Render("adding from the dial is not available here: run roger lan add " + addr)
				return m, nil, true
			}
			return m, func() tea.Msg { line, err := add(addr); return lanAddedMsg{line: line, err: err} }, true
		case "n", "N", "esc":
			m.lanAddSel = ""
			return m, nil, true
		}
	}
	if key != "+" {
		return m, nil, m.lanAddSel != "" // a confirm is open: other keys wait for y or n
	}
	var addrs []string
	for _, c := range m.lanCandidates() {
		if c.Addr != "" {
			addrs = append(addrs, c.Addr)
		}
	}
	if len(addrs) == 0 {
		m.status = stDim.Render("no Tower is advertising on this network")
		return m, nil, true
	}
	next := addrs[0]
	for i, a := range addrs {
		if a == m.lanAddSel && i+1 < len(addrs) {
			next = addrs[i+1]
		}
	}
	m.lanAddSel = next
	return m, nil, true
}

// lanTrustTag is a LAN dial row's trust word, after its flags: "unverified" when any source is a
// Tower (trust-the-LAN), "verified" when it is served by a verified Edge member, nothing for this
// machine or outside LAN mode. A word, so it reads with NO_COLOR.
func lanTrustTag(bd band) string {
	if !LANMode() || bd.cheapest == nil {
		return ""
	}
	tag := ""
	for _, src := range bd.cheapest.LANSources {
		switch {
		case src.Kind == client.LANTower:
			return "  unverified"
		case src.Kind == client.LANEdge && src.Verified:
			tag = "  verified"
		}
	}
	return tag
}
