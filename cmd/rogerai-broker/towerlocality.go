package main

// towerlocality.go: where Core measures a Tower's hub and a Station to be
// (features/tower/placement_locality.feature). Inputs are addresses Core observes itself,
// bucketed by coarseNetBucket and mapped by the operator's continent table; nothing a Tower or
// a Station says about itself is read, and no address or bucket is ever logged.

import (
	"context"
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

// addrLocality is the locality of one observed address.
func (b *broker) addrLocality(addr string) placement.Locality {
	return placement.FromAddr(addr, coarseNetBucket, b.netTable.continentOf)
}

// measureTowerLocality resolves a Tower's hub endpoint and records its locality on the
// session it was measured for. linkAddr is the address the link connected from, used only to
// cross-check the continent. A failed lookup records nothing, so the sweep tries again.
func (b *broker) measureTowerLocality(towerID, sessionID, endpoint, linkAddr string) {
	ts := b.tower
	if endpoint == "" {
		ts.link.SetLocality(towerID, sessionID, placement.Locality{})
		return
	}
	host, _, err := net.SplitHostPort(endpoint)
	if err != nil {
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
	linkCont := b.addrLocality(linkAddr).Continent
	loc, conflict := placement.FromAddrs(addrs, linkCont, coarseNetBucket, b.netTable.continentOf)
	if conflict {
		hub, _ := placement.FromAddrs(addrs, "", coarseNetBucket, b.netTable.continentOf)
		log.Printf("tower-locality conflict tower=%s hub=%s link=%s", towerID, hub.Continent, linkCont)
	}
	ts.link.SetLocality(towerID, sessionID, loc)
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
