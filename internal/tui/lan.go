package tui

// LAN mode in the TUI (features/lan/): the dial reads the ONE merged LAN dial the CLI and the
// console also read (cmd/rogerai lanDial), so no surface can show a different LAN.

import (
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"

	"rogerai.fm/roger/v6/internal/client"
)

var (
	lanMu   sync.RWMutex
	lanDial func() ([]client.LANBand, []client.LANTowerStatus)
)

// SetLANDial puts the TUI in LAN mode for this process (fn non-nil) with the shared dial, or
// back in the default mode (nil). The mode is decided once per process by the CLI.
func SetLANDial(fn func() ([]client.LANBand, []client.LANTowerStatus)) {
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
var lanTowerStatus []client.LANTowerStatus

// lanOffers reads the LAN dial as dial rows: free, online, one per band, naming its sources.
func lanOffers() offersMsg {
	lanMu.RLock()
	fn := lanDial
	lanMu.RUnlock()
	bands, status := fn()
	lanMu.Lock()
	lanTowerStatus = status
	lanMu.Unlock()
	out := make([]offer, 0, len(bands))
	for _, b := range bands {
		out = append(out, offer{Model: b.Model, NodeID: b.SourceLabel(), Online: true, FreeNow: true,
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
