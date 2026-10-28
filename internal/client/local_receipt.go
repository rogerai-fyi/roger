package client

import (
	"time"

	"rogerai.fm/roger/v7/internal/lanmode"
	"rogerai.fm/roger/v7/internal/protocol"
)

// localTowerReceipt is the one record of a free answer from a standalone Tower (features/
// packaging/one_binary.feature section 7.7), whether it came through the market path (the
// broker setting is a Tower) or the LAN ladder. tower is the Tower's URL or host:port; both name
// the node the same way. lan is set only on the LAN ladder.
func localTowerReceipt(tower, model string, lan bool) protocol.UsageReceipt {
	name := tower
	if a, err := lanmode.ParseAddr(tower); err == nil {
		name = a.String()
	}
	return protocol.UsageReceipt{RequestID: newRequestID(), NodeID: "tower:" + name,
		Model: model, Local: true, LAN: lan, TS: time.Now().Unix()}
}
