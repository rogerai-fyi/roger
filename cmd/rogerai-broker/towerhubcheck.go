package main

// towerhubcheck.go: Core's Station-free hub check (features/tower/tower_trust_ramp.feature,
// "the hub check"). Each live Tower with a hub is checked by the one instance holding its
// link, at most once per canary interval, and every result lands in the shared health store
// the trust ramp counts and placement reads. Station canaries land there too.

import (
	"context"
	"time"

	"rogerai.fm/roger/v7/internal/towercore/admit"
	"rogerai.fm/roger/v7/internal/towercore/reputation"
	"rogerai.fm/roger/v7/internal/towerhub"
)

// hubCheckTimeout bounds one check: a hub that does not answer within it is unreachable.
const hubCheckTimeout = 5 * time.Second

// towerHubCheckOnce checks every Tower this instance holds whose last check is at least a
// canary interval old.
func (b *broker) towerHubCheckOnce(now time.Time) {
	ts := b.tower
	if ts == nil {
		return
	}
	for _, tw := range ts.registry.All() {
		if !ts.link.HoldsLocally(tw.ID) {
			continue
		}
		plane, has := ts.link.RelayPlane(tw.ID)
		if !has || plane.Endpoint == "" {
			continue
		}
		b.hubCheckMu.Lock()
		last := b.hubCheckAt[tw.ID]
		due := last.IsZero() || now.Sub(last) >= canaryInterval
		if due {
			if b.hubCheckAt == nil {
				b.hubCheckAt = map[string]time.Time{}
			}
			b.hubCheckAt[tw.ID] = now
		}
		b.hubCheckMu.Unlock()
		if due {
			b.hubCheck(tw, plane.Endpoint, plane.TLSSPKI, now)
		}
	}
}

func (b *broker) hubCheck(tw admit.Tower, endpoint, pin string, now time.Time) {
	reason := "unreachable"
	base, hc, err := towerhub.ReachVetted(endpoint, pin, b.canaryVet)
	if err == nil {
		hc.Timeout = hubCheckTimeout
		ctx, cancel := context.WithTimeout(context.Background(), hubCheckTimeout)
		_, reason = towerhub.CheckHub(ctx, hc, base, tw.ID, tw.KeyHash)
		cancel()
	}
	b.recordHealthResult(tw.ID, reason == "", now)
	if reason != "" {
		b.towerEvent(admit.Event{TowerID: tw.ID, Kind: evHubCheckFailed, Actor: admit.ActorSystem, Reason: reason})
	}
}

// recordTowerHealth files a Station canary's verdict as a health check of the Tower that
// carried it. A Station's own fault, or an aborted probe, says nothing about the Tower.
func (b *broker) recordTowerHealth(towerID string, outcome reputation.Outcome) {
	switch outcome {
	case reputation.CanaryPass:
		b.recordHealthResult(towerID, true, time.Now())
	case reputation.CanaryFail:
		b.recordHealthResult(towerID, false, time.Now())
	}
}

func (b *broker) recordHealthResult(towerID string, ok bool, at time.Time) {
	if err := b.tower.health.Record(towerID, ok, at); err != nil {
		b.towerGovUnavailable(err)
	}
}
