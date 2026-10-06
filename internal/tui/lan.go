package tui

// LAN mode in the TUI (features/lan/): the dial reads the ONE merged LAN dial the CLI and the
// console also read (cmd/rogerai lanDial), so no surface can show a different LAN.

import (
	"sort"
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
