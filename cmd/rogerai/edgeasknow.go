package main

// ASK NOW (features/edge/fleet_window.feature §3): ask a unit to report at once over its face, not on
// the next heartbeat, and say how long the round trip took. It is the same dial and the same checks a
// discovery pass makes - the certificate must match the unit's pin - and the answer is recorded the
// way a discovery pass records it. Asks closer together than a floor are answered from the last one,
// saying so, so a monitor cannot become the load.

import (
	"context"
	"fmt"
	"sync"
	"time"

	"rogerai.fm/roger/v6/internal/edge"
)

// edgeAskFloor is the closest two asks of one unit may be.
const edgeAskFloor = 5 * time.Second

var edgeAsked = struct {
	sync.Mutex
	at   map[string]time.Time
	says map[string]string
}{at: map[string]time.Time{}, says: map[string]string{}}

// edgeAskNow asks one unit to report now and returns what to tell the owner.
func edgeAskNow(id string, now time.Time) (string, error) {
	edgeAsked.Lock()
	if at, ok := edgeAsked.at[id]; ok && now.Sub(at) < edgeAskFloor {
		says := edgeAsked.says[id]
		edgeAsked.Unlock()
		return fmt.Sprintf("asked %s ago - %s", edge.Age(now.Sub(at)), says), nil
	}
	edgeAsked.Unlock()

	st, n, ok, err := edgeLookup(id)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", errNoSuchNode(id)
	}
	addr := ""
	for _, t := range n.Transports {
		if t.Kind == "lan" && t.Addr != "" && t.Fingerprint != "" {
			addr = t.Addr
			break
		}
	}
	if addr == "" {
		return "", fmt.Errorf("%s has no face this machine can dial, so it can only be heard, not asked", n.Name)
	}
	start := time.Now()
	peer, err := edgeDialPeer(context.Background(), addr)
	rtt := time.Since(start)
	if err != nil {
		return "", fmt.Errorf("%s did not answer within %s: %v", n.Name, edgeAskWithin, err)
	}
	if n.Pin != "" && peer.Fingerprint != n.Pin {
		return "", fmt.Errorf("%s answered with a certificate that is not the one it was adopted with, so nothing was recorded", n.Name)
	}
	caps := make([]edge.Capability, 0, len(peer.Describe.Caps))
	for _, c := range peer.Describe.Caps {
		if cp := edge.Capability(c); cp.Valid() {
			caps = append(caps, cp)
		}
	}
	insts := peer.Describe.Instances
	truncated := peer.Describe.InstancesTruncated
	if len(insts) > edge.MaxInstances {
		insts, truncated = insts[:edge.MaxInstances], true
	}
	if _, err := st.fleet.Observe(n.ID, edge.Observation{Name: n.Name, Kind: peer.Describe.Kind, Caps: caps, Addr: addr,
		Fingerprint: peer.Fingerprint, Instances: insts, InstancesTruncated: truncated}); err != nil {
		return "", err
	}
	if err := st.save(); err != nil {
		return "", err
	}
	says := fmt.Sprintf("%s answered in %d ms", n.Name, rtt.Milliseconds())
	edgeAsked.Lock()
	edgeAsked.at[id], edgeAsked.says[id] = now, says
	edgeAsked.Unlock()
	return says, nil
}

// edgeAskWithin is what the owner is told the bound was: the dial's own timeout.
const edgeAskWithin = "3s"
