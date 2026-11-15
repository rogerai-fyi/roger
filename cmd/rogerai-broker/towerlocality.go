package main

// towerlocality.go: where Core measures a Tower's hub and a Station to be
// (features/tower/placement_locality.feature). Inputs are addresses Core observes itself,
// bucketed by coarseNetBucket and mapped by the operator's continent table; nothing a Tower or
// a Station says about itself is read, and no address or bucket is ever logged.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"log"
	"net"
	"sync/atomic"
	"time"

	"rogerai.fm/roger/v7/internal/towercore/placement"
)

// hubResolver resolves a hub host within a bounded time, so DNS can never hold a measurement
// open. Held atomically: a lookup runs off the request path while a test may swap it.
type hubResolver struct {
	lookup  func(ctx context.Context, host string) ([]string, error)
	timeout time.Duration
}

var hubResolve atomic.Pointer[hubResolver]

func init() {
	hubResolve.Store(&hubResolver{lookup: net.DefaultResolver.LookupHost, timeout: 2 * time.Second})
}

// addrLocality is the locality of one observed address. Its same-network key is stored only as
// a keyed hash (homeTag): two observations still compare equal, and no full address is kept
// (placement_locality.feature, "Locality is kept coarse and never published").
func (b *broker) addrLocality(addr string) placement.Locality {
	l := placement.FromAddr(addr, coarseNetBucket, b.netTable.continentOf)
	l.Home = b.homeTag(l.Home)
	return l
}

// homeTag is the keyed hash of a same-network key ("" stays ""), keyed by a secret every broker
// instance derives from the same key, so instances agree without any address being stored.
func (b *broker) homeTag(home string) string {
	if home == "" {
		return ""
	}
	mac := hmac.New(sha256.New, b.deriveSecret("rogerai home network v1"))
	mac.Write([]byte(home))
	return hex.EncodeToString(mac.Sum(nil)[:16])
}

// measureTowerLocality resolves a Tower's hub endpoint and records its locality on the
// session it was measured for. linkAddr is the address the link connected from, used only to
// cross-check the continent. A failed lookup records nothing, so the sweep tries again.
func (b *broker) measureTowerLocality(towerID, sessionID, endpoint, linkAddr string) {
	ts := b.tower
	// The link's locality is kept FIRST, before anything can return early: a Tower controls
	// its own DNS, and a lookup it makes fail must not cost the session its cross-check.
	linkLoc, linkKnown := b.addrLocality(linkAddr), linkAddr != ""
	if linkKnown {
		ts.link.SetLinkLocality(towerID, sessionID, linkLoc)
	} else {
		linkLoc, linkKnown = ts.link.LinkLocality(towerID) // a retry keeps the session's cross-check
	}
	if endpoint == "" {
		ts.link.SetHubClass(towerID, sessionID, placement.HubNone)
		ts.link.SetLocality(towerID, sessionID, placement.Locality{})
		return
	}
	host, _, err := net.SplitHostPort(endpoint)
	if err != nil {
		ts.link.SetHubClass(towerID, sessionID, placement.HubNone)
		ts.link.SetLocality(towerID, sessionID, placement.Locality{})
		return
	}
	addrs := []string{host}
	if net.ParseIP(host) == nil {
		res := hubResolve.Load()
		ctx, cancel := context.WithTimeout(context.Background(), res.timeout)
		addrs, err = res.lookup(ctx, host)
		cancel()
		if err != nil {
			return
		}
	}
	class := b.hubClassOf(addrs)
	ts.link.SetHubClass(towerID, sessionID, class)
	if class != placement.HubPublic {
		// A private hub's address says nothing about where the Tower is: its location is the
		// address its link is observed from (private_hub_placement.feature). A loopback or
		// unusable one is placed nowhere, so it has no location worth recording.
		if class == placement.HubPrivate && linkKnown {
			ts.link.SetLocality(towerID, sessionID, linkLoc)
		} else {
			ts.link.SetLocality(towerID, sessionID, placement.Locality{})
		}
		return
	}
	loc, conflict := placement.FromAddrs(addrs, linkLoc.Continent, coarseNetBucket, b.netTable.continentOf)
	if conflict {
		hub, _ := placement.FromAddrs(addrs, "", coarseNetBucket, b.netTable.continentOf)
		log.Printf("tower-locality conflict tower=%s hub=%s link=%s", towerID, hub.Continent, linkLoc.Continent)
	}
	loc.Home = "" // a public hub is compared by bucket and continent only
	ts.link.SetLocality(towerID, sessionID, loc)
}

// hubClassOf classes a hub's resolved addresses. A broker with no dial vet (canaryVet nil, the
// permissive posture a test harness runs with) treats any address as one it may dial, exactly
// as its dials do; production vets with vetPublicIP and classes for real.
func (b *broker) hubClassOf(addrs []string) placement.HubClass {
	class := placement.HubClassOf(addrs)
	if b.canaryVet == nil && class != placement.HubNone {
		return placement.HubPublic
	}
	return class
}

// noteLinkAddr re-measures a Tower whose link is now observed from a different network: its link
// locality, and for a private hub its location too (private_hub_placement.feature, "A private-hub
// Tower whose link address changes is re-measured"). It writes only on a change.
func (b *broker) noteLinkAddr(towerID, addr string) {
	ts := b.tower
	if ts == nil || addr == "" {
		return
	}
	sid, ok := ts.link.Current(towerID)
	if !ok {
		return
	}
	loc := b.addrLocality(addr)
	if prev, known := ts.link.LinkLocality(towerID); known && prev == loc {
		return
	}
	ts.link.SetLinkLocality(towerID, sid, loc)
	if ts.link.HubClass(towerID) == placement.HubPrivate {
		ts.link.SetLocality(towerID, sid, loc)
	}
}

// relocateFromProof re-measures a Tower's hub from the address a passing hub check reached,
// cross-checked against the link continent, and records it when it differs.
func (b *broker) relocateFromProof(towerID, remote string) {
	ts := b.tower
	sid, ok := ts.link.Current(towerID)
	if !ok {
		return
	}
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		return
	}
	linkCont := ts.link.LinkContinent(towerID)
	loc, conflict := placement.FromAddrs([]string{host}, linkCont, coarseNetBucket, b.netTable.continentOf)
	if conflict {
		hub := b.addrLocality(host)
		log.Printf("tower-locality conflict tower=%s hub=%s link=%s", towerID, hub.Continent, linkCont)
	}
	loc.Home = "" // a public hub is compared by bucket and continent only
	if prev, known := ts.link.Locality(towerID); known && prev == loc {
		return
	}
	ts.link.SetLocality(towerID, sid, loc)
}

// remeasureTowerLocality is the sweep's retry: one lookup per live Tower whose hub has not
// been measured on its current session.
func (b *broker) remeasureTowerLocality(towerID string) {
	ts := b.tower
	if _, known := ts.link.Locality(towerID); known || !ts.link.Live(towerID) {
		return
	}
	sid, ok := ts.link.Current(towerID)
	if !ok {
		return
	}
	plane, _ := ts.link.RelayPlane(towerID)
	b.measureTowerLocality(towerID, sid, plane.Endpoint, "")
}

// towerLocalityView is what an admin sees of a Tower's locality: the continent and whether a
// bucket is known, never the bucket (placement_locality.feature, "Locality is kept coarse").
func towerLocalityView(ts *towerSubsystem, towerID string) map[string]any {
	loc, known := ts.link.Locality(towerID)
	if !known {
		return map[string]any{"measured": false}
	}
	return map[string]any{"measured": true, "continent": loc.Continent, "has_bucket": loc.Bucket != ""}
}

// towerHubView is what an admin sees of a Tower's hub (private_hub_placement.feature, "Admins see
// the hub class and the network Core measured"): its class, how work reaches it, whether Core has
// measured its network - never the network itself - and for a private hub, how many of its
// Stations are on that network.
func (b *broker) towerHubView(towerID string) map[string]any {
	ts := b.tower
	class := ts.link.HubClass(towerID)
	loc, measured := ts.link.Locality(towerID)
	out := map[string]any{"class": class.String(), "pulls": ts.link.Pulls(towerID),
		"network_measured": measured && (loc.Bucket != "" || loc.Home != "")}
	if class != placement.HubPrivate {
		return out
	}
	atts, err := ts.stationStore.ByTower(towerID)
	if err != nil {
		return out
	}
	placed, onNetwork := 0, 0
	for _, at := range atts {
		if !at.Live() {
			continue
		}
		placed++
		if ts.mobility == nil {
			continue
		}
		if f, ok, ferr := ts.mobility.Facts(at.StationID); ferr == nil && ok && placement.SameHome(f.Locality, loc) {
			onNetwork++
		}
	}
	out["stations_placed"], out["stations_on_network"] = placed, onNetwork
	return out
}
