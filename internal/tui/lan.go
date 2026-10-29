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

	"rogerai.fm/roger/v7/internal/client"
	"rogerai.fm/roger/v7/internal/protocol"
)

var (
	lanMu       sync.RWMutex
	lanDial     func() client.LANDial
	lanEdgeOnly bool // `roger --lan --edge-only`: the badge and the dial say so
)

// SetLANEdgeOnly marks LAN mode as edge-only for this process (features/lan/edge_only.feature).
func SetLANEdgeOnly(on bool) {
	lanMu.Lock()
	defer lanMu.Unlock()
	lanEdgeOnly = on
}

// lanModeName is the mode's name on the badge and the dial: "LAN", or "LAN EDGE-ONLY".
func lanModeName() string {
	lanMu.RLock()
	defer lanMu.RUnlock()
	if lanEdgeOnly {
		return "LAN EDGE-ONLY"
	}
	return "LAN"
}

// SetLANDial puts the TUI in LAN mode for this process (fn non-nil) with the shared dial, or
// back in the default mode (nil). The mode is decided once per process by the CLI.
func SetLANDial(fn func() client.LANDial) {
	lanMu.Lock()
	defer lanMu.Unlock()
	lanDial = fn
}

// lanSpeak speaks a line through a LOCAL voice in LAN mode (features/lan/voice.feature): this
// machine's, then an Edge member's. nil outside LAN mode, where a preview goes to the network.
var lanSpeak func(band, text string) ([]byte, error)

// SetLANSpeak installs (or clears, with nil) the LAN voice the preview speaks through.
func SetLANSpeak(fn func(band, text string) ([]byte, error)) {
	lanMu.Lock()
	defer lanMu.Unlock()
	lanSpeak = fn
}

func lanSpeaker() func(band, text string) ([]byte, error) {
	lanMu.RLock()
	defer lanMu.RUnlock()
	return lanSpeak
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
		return stKey.Render(lanModeName())
	}
	return stKey.Render(lanModeName()) + stDim.Render(" · local, free")
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
		modality := protocol.ModalityChat
		if b.Voice {
			modality = protocol.ModalityTTS // a voice band opens the voice preview, never a chat channel
		}
		out = append(out, offer{Model: b.Model, NodeID: lanSourceText(b), Online: true, FreeNow: true,
			Modality: modality, LANSource: b.SourceLabel(), LANSources: b.Sources})
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

// lanSourceText is a dial row's source column: each source, with an Edge member or a Tower pinned
// to its TLS key marked verified (its key was checked), and a plain-http Tower marked unverified.
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
			if src.Verified {
				label += " (verified)"
			} else {
				label += " (unverified)"
			}
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
				m.status = stDim.Render("adding from the dial is not available here: run roger join " + addr)
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
// plain-http Tower, "verified" when it is served by a verified Edge member or a pinned Tower,
// nothing for this machine or outside LAN mode. A word, so it reads with NO_COLOR.
func lanTrustTag(bd band) string {
	if !LANMode() || bd.cheapest == nil {
		return ""
	}
	tag := ""
	for _, src := range bd.cheapest.LANSources {
		switch {
		case src.Kind == client.LANTower && !src.Verified:
			return "  unverified"
		case (src.Kind == client.LANEdge || src.Kind == client.LANTower) && src.Verified:
			tag = "  verified"
		}
	}
	return tag
}

// lanFlagsW is what a LAN dial row's flags cell can take: a badge, "FREE" and "unverified".
const lanFlagsW = 20

// lanSourceW is the wide LAN dial's source column: the room left after the band, on-air and
// flags cells (a LAN row has no price, signal, ctx or t/s column: none of them carries data on
// the LAN), at most enough for "tower: 192.168.100.200".
func (m model) lanSourceW(w int) int {
	n := w - 2 - m.bandNameW() - 2 - 9 - 2 - 2 - lanFlagsW
	switch {
	case n > 23:
		return 23
	case n < 6:
		return 6
	}
	return n
}

// lanSourceCell is a row's source column: where the band is served, by label.
func lanSourceCell(bd band) string {
	if bd.cheapest == nil {
		return ""
	}
	return bd.cheapest.LANSource
}

// lanWideRow is one wide LAN dial row: band, on air, source and flags (with the trust word).
func (m model) lanWideRow(bd band, w, nameW int, stationsLbl string, sel, connected bool, tableW int) string {
	srcW := m.lanSourceW(w)
	src := pad(truncVisible(lanSourceCell(bd), srcW), srcW)
	if sel {
		plain := fmt.Sprintf("%s  %s  %s  %s", bandNameCellAt(bd, nameW, m.marqueeOff()), pad(stationsLbl, 9), src,
			plainBandBadge(bd, m.limits, connected)+lanTrustTag(bd))
		return m.caratGutter() + rowSel(true, plain, tableW) + "\n"
	}
	nameCell, statCell := stDim.Render(bandNameCell(bd, nameW)), stDim.Render(pad(stationsLbl, 9))
	if connected {
		nameCell, statCell = stKey.Render(bandNameCell(bd, nameW)), stRed.Render(pad(stationsLbl, 9))
	}
	return selCarat(false) + " " + nameCell + "  " + statCell + "  " + stEmber.Render(src) + "  " +
		bandBadge(bd, m.limits, connected) + stDim.Render(lanTrustTag(bd)) + "\n"
}

// narrowPriceCell is the narrow dial's price column, or in LAN mode the source, cut to what the
// row has left after the band, on-air, FREE and trust cells so the row never overflows w.
func (m model) narrowPriceCell(bd band, w, nameW int) string {
	if !LANMode() {
		return rangeStr(bd)
	}
	left := w - 4 - nameW - 2 - 9 - 2 - len("  FREE") - len(lanTrustTag(bd))
	if left < 4 {
		return "" // no room left: the row fits first; the plate and [i] still name the source
	}
	if left > 23 {
		left = 23
	}
	return pad(truncVisible(lanSourceCell(bd), left), left)
}

// lanEdgeUnreadLine notes, under a LAN dial, that this Edge's record could not be read: its
// members' bands may be missing, so the dial never reads as "the Edge serves nothing".
func lanEdgeUnreadLine(w int) string {
	lanMu.RLock()
	why := lanEdgeUnread
	lanMu.RUnlock()
	if why == "" {
		return ""
	}
	return "  " + truncVisible(stEmber.Render("the Edge could not be read: ")+stDim.Render(why), w-4) + "\n"
}

// lanEmptyView is the empty LAN dial (features/lan/discovery_and_dial.feature §5): it says no
// local band is on air, what it checked, and the three ways to get one. It never falls back to
// the market. An Edge whose record could not be read is reported as unread, not as checked.
func (m model) lanEmptyView(w int) string {
	lanMu.RLock()
	unread := lanEdgeUnread
	towers := append([]client.LANTowerStatus(nil), lanTowerStatus...)
	lanMu.RUnlock()
	checked := []string{"this machine"}
	if unread == "" {
		checked = append(checked, "this Edge's members")
	}
	nTowers := 0
	for _, st := range towers {
		if st.State != client.LANTowerCandidate {
			nTowers++
		}
	}
	checked = append(checked, fmt.Sprintf("LAN Towers (%d configured)", nTowers))
	line := func(s string) string { return "  " + truncVisible(s, w-4) + "\n" }
	var b strings.Builder
	b.WriteString("\n" + line(stDim.Render("no local band is on air")))
	if m.compact {
		b.WriteString(line(stDim.Render("checked " + strings.Join(checked, ", "))))
		return b.String()
	}
	b.WriteString(line(stDim.Render("checked: " + strings.Join(checked, ", "))))
	if unread != "" {
		b.WriteString(lanEdgeUnreadLine(w))
	}
	for _, st := range towers {
		if st.State == client.LANTowerCandidate || st.State == client.LANTowerAnswering {
			continue
		}
		b.WriteString(line(stDim.Render("tower " + lanTowerHost(st.Addr) + ": " + string(st.State))))
	}
	b.WriteString(line(stDim.Render("to get one:")))
	for _, way := range [][2]string{
		{"serve a model here", "roger model serve"},
		{"add an Edge member", "roger edge invite, then roger join on the device"},
		{"add a Tower", "roger join with its owner's invite"},
	} {
		b.WriteString(line(stDim.Render("  · ") + stKey.Render(way[0]) + stDim.Render("  "+way[1])))
	}
	b.WriteString(m.lanCandidateView(w))
	return b.String()
}
