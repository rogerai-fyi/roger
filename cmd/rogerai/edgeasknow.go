package main

// ASK NOW (features/edge/fleet_window.feature §3): ask a unit to report at once over its face, not on
// the next heartbeat, and say how long the round trip took. It is the same dial and the same
// edge.VerifyPeer a discovery pass makes - expiry, revocation, the chain to this Edge's authority,
// the node id its key derives, and the pin - and only then is the answer recorded, the way a
// discovery pass records it. Asks of one unit closer together than a floor - answered or not - are
// answered from the last attempt, saying so, so a monitor cannot become the load.

import (
	"context"
	"fmt"
	"sync"
	"time"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/towercore/cert"
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
	// The attempt is the floor, not the answer: a dead unit asked again and again must not be dialled
	// again and again, and two asks at once dial once.
	edgeAsked.at[id], edgeAsked.says[id] = now, "still waiting for its answer"
	edgeAsked.Unlock()
	says, err := edgeAskOnce(id, now)
	edgeAsked.Lock()
	if err != nil {
		edgeAsked.says[id] = "it did not answer: " + err.Error()
	} else {
		edgeAsked.says[id] = says
	}
	edgeAsked.Unlock()
	return says, err
}

// edgeAskTrust is this Edge's authority, the root a dialled unit must chain to (a seam for tests).
var edgeAskTrust = func(now time.Time) (*cert.Authority, error) {
	auth, _, err := edgeIdentityStore().Trust(now)
	return auth, err
}

func edgeAskOnce(id string, now time.Time) (string, error) {
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
	if addr == "" || n.Pin == "" {
		return "", fmt.Errorf("%s has no pinned face this machine can dial, so it can only be heard, not asked", edgeDisplayName(n.ID))
	}
	auth, err := edgeAskTrust(now)
	if err != nil || auth == nil {
		return "", fmt.Errorf("this machine holds no Edge authority to check %s's certificate against", edgeDisplayName(n.ID))
	}
	start := time.Now()
	peer, err := edgeDialPeer(context.Background(), addr)
	rtt := time.Since(start)
	if err != nil {
		return "", fmt.Errorf("%s did not answer within %s: %v", edgeDisplayName(n.ID), edgeAskWithin, err)
	}
	// The same verdict a discovery pass reaches, and nothing is recorded without it.
	if reason := edge.VerifyPeer(edge.Advert{NodeID: n.ID, Fingerprint: n.Pin}, peer, auth, n.Pin, now); reason != "" {
		return "", fmt.Errorf("%s answered, but its certificate did not check out (%s), so nothing was recorded", edgeDisplayName(n.ID), reason)
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
	return fmt.Sprintf("%s answered in %d ms", edgeDisplayName(n.ID), rtt.Milliseconds()), nil
}

// edgeAskWithin is what the owner is told the bound was: the dial's own timeout (edge.DialTLS).
var edgeAskWithin = edge.DialTimeout.String()
